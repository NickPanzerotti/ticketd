package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var ErrTooLarge = errors.New("file exceeds the upload size limit")

type Attachment struct {
	ID          int64
	TicketID    int64
	PostID      sql.NullInt64
	Filename    string
	StoredName  string
	ContentType string
	SizeBytes   int64
	CreatedBy   int64
	CreatedAt   string
}

func (a *Attachment) Path(dir string) string { return filepath.Join(dir, a.StoredName) }

// Inline reports whether the file can be safely shown in the browser.
// SVG is deliberately excluded (scripts), and is served as a download instead.
func (a *Attachment) Inline() bool {
	ct := strings.ToLower(a.ContentType)
	switch {
	case strings.HasPrefix(ct, "image/") && !strings.Contains(ct, "svg"):
		return true
	case ct == "application/pdf", ct == "text/plain", ct == "text/csv":
		return true
	case strings.HasPrefix(ct, "text/"):
		return true
	}
	return false
}

// SaveAttachment streams an upload into the uploads dir and records it.
// postID may be 0 to attach to the ticket itself (creation-time attachments).
func (s *Store) SaveAttachment(ticketID, userID, postID int64, filename string, r io.Reader, maxBytes int64) (*Attachment, error) {
	sanitized := sanitizeFilename(filename)
	if sanitized == "" {
		return nil, fmt.Errorf("%w: empty filename", ErrInvalidInput)
	}

	uid := make([]byte, 16)
	rand.Read(uid)
	stored := hex.EncodeToString(uid)
	if ext := safeExt(filepath.Ext(sanitized)); ext != "" {
		stored += "." + ext
	}
	path := filepath.Join(s.UploadDir, stored)

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.Remove(path)
		}
	}()

	// Sniff content type from the first bytes while copying.
	var head []byte
	buf := make([]byte, 512)
	n, rerr := io.ReadFull(r, buf)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		err = rerr
		return nil, err
	}
	head = buf[:n]
	if n > 0 {
		if _, err = f.Write(head); err != nil {
			return nil, err
		}
	}
	copied, err := io.Copy(f, io.LimitReader(r, maxBytes-int64(n)+1))
	if err != nil {
		return nil, err
	}
	if copied+int64(n) > maxBytes {
		err = ErrTooLarge
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	size := int64(n) + copied
	contentType := http.DetectContentType(head)

	att := &Attachment{
		TicketID: ticketID, PostID: sql.NullInt64{Int64: postID, Valid: postID > 0},
		Filename:   sanitized,
		StoredName: stored, ContentType: contentType, SizeBytes: size, CreatedBy: userID,
		CreatedAt: nowRFC(),
	}
	res, derr := s.DB.Exec(`INSERT INTO attachments (ticket_id, post_id, filename, stored_name, content_type, size_bytes, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		att.TicketID, att.PostID, att.Filename, att.StoredName, att.ContentType, att.SizeBytes, att.CreatedBy, att.CreatedAt)
	if derr != nil {
		err = derr
		return nil, derr
	}
	att.ID, _ = res.LastInsertId()
	return att, nil
}

func (s *Store) GetAttachment(id int64) (*Attachment, error) {
	row := s.DB.QueryRow(`SELECT id, ticket_id, post_id, filename, stored_name, content_type, size_bytes, created_by, created_at
		FROM attachments WHERE id = ?`, id)
	var a Attachment
	err := row.Scan(&a.ID, &a.TicketID, &a.PostID, &a.Filename, &a.StoredName, &a.ContentType, &a.SizeBytes, &a.CreatedBy, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAttachments returns all attachments for a ticket, keyed by post id (0 = on the ticket itself).
func (s *Store) ListAttachments(ticketID int64) (map[int64][]Attachment, error) {
	rows, err := s.DB.Query(`SELECT id, ticket_id, post_id, filename, stored_name, content_type, size_bytes, created_by, created_at
		FROM attachments WHERE ticket_id = ? ORDER BY id`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Attachment{}
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.TicketID, &a.PostID, &a.Filename, &a.StoredName, &a.ContentType, &a.SizeBytes, &a.CreatedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		key := int64(0)
		if a.PostID.Valid {
			key = a.PostID.Int64
		}
		out[key] = append(out[key], a)
	}
	return out, rows.Err()
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.NewReplacer("\r", "", "\n", "", "\"", "'", "\\", "/", ":", "-").Replace(name)
	name = strings.TrimSpace(name)
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

func safeExt(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if ext == "" || len(ext) > 8 {
		return ""
	}
	for _, r := range ext {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return ext
}
