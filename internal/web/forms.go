package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"ticketd/internal/store"
)

type uploadedFile struct {
	Filename string
	TempPath string
}

// parsePostForm reads a POST body as urlencoded or multipart form data.
// Multipart files are streamed to temp files inside the uploads dir; the
// caller must pass them to saveUploads and delete the temp copies.
// Multipart CSRF is validated by the handler from the returned values.
func (s *Server) parsePostForm(w http.ResponseWriter, r *http.Request) (url.Values, []uploadedFile, error) {
	maxFile := s.cfg.MaxUploadMB * 1024 * 1024
	maxTotal := 8*maxFile + 64*1024
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxTotal)

	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseForm(); err != nil {
			return nil, nil, err
		}
		return r.PostForm, nil, nil
	}

	vals := url.Values{}
	var files []uploadedFile
	mr, mrcErr := r.MultipartReader()
	if mrcErr != nil {
		return vals, files, mrcErr
	}
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return vals, files, err
		}
		name := p.FormName()
		if name == "" {
			p.Close()
			continue
		}
		if fn := p.FileName(); fn != "" {
			tmp, err := os.CreateTemp(s.store.UploadDir, "tmp-*.part")
			if err != nil {
				return vals, files, err
			}
			n, cerr := io.Copy(tmp, io.LimitReader(p, maxFile+1))
			p.Close()
			tmp.Close()
			if cerr != nil {
				os.Remove(tmp.Name())
				return vals, files, cerr
			}
			if n > maxFile {
				os.Remove(tmp.Name())
				return vals, files, fmt.Errorf("%w (limit %d MB)", store.ErrTooLarge, s.cfg.MaxUploadMB)
			}
			if n == 0 { // empty file input selection
				os.Remove(tmp.Name())
				continue
			}
			files = append(files, uploadedFile{Filename: fn, TempPath: tmp.Name()})
			continue
		}
		b, err := io.ReadAll(io.LimitReader(p, 1<<20))
		p.Close()
		if err != nil {
			return vals, files, err
		}
		vals.Add(name, string(b))
	}
	return vals, files, nil
}

// checkMultipartCSRF validates the CSRF field for multipart POSTs, which the
// regular CSRF middleware cannot see (streamed parsing).
func (s *Server) checkMultipartCSRF(r *http.Request, vals url.Values) bool {
	sess := sessionFrom(r.Context())
	return sess != nil && vals.Get("csrf") == sess.CSRF
}

// saveUploads attaches queued temp files to a ticket (postID 0 = the ticket itself).
// Returns a message describing partial failures, or "".
func (s *Server) saveUploads(ticketID, userID, postID int64, files []uploadedFile) string {
	var failed []string
	for _, f := range files {
		fh, err := os.Open(f.TempPath)
		if err != nil {
			failed = append(failed, f.Filename)
			continue
		}
		_, serr := s.store.SaveAttachment(ticketID, userID, postID, f.Filename, fh, s.cfg.MaxUploadMB*1024*1024)
		fh.Close()
		os.Remove(f.TempPath)
		if serr != nil {
			failed = append(failed, f.Filename)
		}
	}
	if len(failed) == 0 {
		return ""
	}
	return "Upload failed for: " + strings.Join(failed, ", ")
}
