package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type Ticket struct {
	ID              int64
	Number          int
	Title           string
	Description     string
	Status          string // open | in_progress | closed
	Priority        string // low | normal | high | urgent
	CategoryID      sql.NullInt64
	CategoryName    sql.NullString
	CategoryColor   sql.NullString
	CreatedBy       int64
	CreatorName     string
	AssignedTo      sql.NullInt64
	AssigneeName    sql.NullString
	ReopenRequested bool
	CreatedAt       string
	UpdatedAt       string
	ClosedAt        sql.NullString
	ReplyCount      int
	LastActivity    string
}

type TicketQuery struct {
	Q          string
	Status     string // "", "open", "in_progress", "closed", "unassigned", "reopen"
	Priority   string
	CategoryID int64
	AssigneeID int64  // 0 = any; -1 = unassigned; >0 = specific user
	OnlyMine   int64  // creator id filter (for users viewing their own tickets)
	Sort       string // created | updated | priority
	Offset     int
	Limit      int
}

const ticketSelect = `SELECT t.id, t.number, t.title, t.status, t.priority,
	t.category_id, c.name, c.color, t.created_by, u.username, t.assigned_to, a.display_name,
	t.reopen_requested, t.created_at, t.updated_at, t.closed_at,
	(SELECT COUNT(*) FROM posts p WHERE p.ticket_id = t.id AND p.internal = 0) AS replies,
	COALESCE((SELECT MAX(x.created_at) FROM (
		SELECT created_at FROM posts WHERE ticket_id = t.id
		UNION ALL
		SELECT created_at FROM events WHERE ticket_id = t.id) x), t.created_at) AS last_activity
	FROM tickets t
	LEFT JOIN categories c ON c.id = t.category_id
	JOIN users u ON u.id = t.created_by
	LEFT JOIN users a ON a.id = t.assigned_to`

func escapeLike(s string) string {
	return strings.NewReplacer("%", "\\%", "_", "\\_", "\\", "\\\\").Replace(s)
}

// ListTickets returns tickets matching the query plus the total match count.
func (s *Store) ListTickets(q TicketQuery) ([]Ticket, int, error) {
	var where []string
	var args []any

	if q.Q != "" {
		like := "%" + escapeLike(q.Q) + "%"
		where = append(where, `(t.title LIKE ? ESCAPE '\' OR t.description LIKE ? ESCAPE '\'
			OR EXISTS (SELECT 1 FROM posts p WHERE p.ticket_id = t.id AND p.internal = 0 AND p.body LIKE ? ESCAPE '\')
			OR t.number = CAST(? AS INTEGER))`)
		args = append(args, like, like, like, q.Q)
	}
	switch q.Status {
	case "open", "in_progress", "closed":
		where = append(where, "t.status = ?")
		args = append(args, q.Status)
	case "reopen":
		where = append(where, "t.reopen_requested = 1")
	}
	if q.Priority != "" {
		where = append(where, "t.priority = ?")
		args = append(args, q.Priority)
	}
	if q.CategoryID > 0 {
		where = append(where, "t.category_id = ?")
		args = append(args, q.CategoryID)
	}
	switch {
	case q.AssigneeID == -1:
		where = append(where, "t.assigned_to IS NULL")
	case q.AssigneeID > 0:
		where = append(where, "t.assigned_to = ?")
		args = append(args, q.AssigneeID)
	}
	if q.OnlyMine > 0 {
		where = append(where, "t.created_by = ?")
		args = append(args, q.OnlyMine)
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM tickets t LEFT JOIN categories c ON c.id = t.category_id`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderBy := "t.updated_at DESC"
	switch q.Sort {
	case "created":
		orderBy = "t.created_at DESC"
	case "priority":
		orderBy = "CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, t.updated_at DESC"
	}
	if q.Limit <= 0 {
		q.Limit = 25
	}
	rows, err := s.DB.Query(ticketSelect+clause+" ORDER BY "+orderBy+" LIMIT ? OFFSET ?", append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []Ticket
	for rows.Next() {
		var t Ticket
		var rr int
		if err := rows.Scan(&t.ID, &t.Number, &t.Title, &t.Status, &t.Priority,
			&t.CategoryID, &t.CategoryName, &t.CategoryColor, &t.CreatedBy, &t.CreatorName,
			&t.AssignedTo, &t.AssigneeName, &rr, &t.CreatedAt, &t.UpdatedAt, &t.ClosedAt, &t.ReplyCount, &t.LastActivity); err != nil {
			return nil, 0, err
		}
		t.ReopenRequested = rr == 1
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// nextTicketNumber mints the next never-reused ticket number inside tx.
func nextTicketNumber(tx *sql.Tx) (int, error) {
	var n int
	err := tx.QueryRow(`UPDATE ticket_seq SET next_value = next_value + 1 WHERE id = 1 RETURNING next_value - 1`).Scan(&n)
	return n, err
}

// CreateTicket inserts a ticket with a fresh number and logs the creation event.
func (s *Store) CreateTicket(title, description, priority string, categoryID int64, creatorID int64) (*Ticket, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("%w: title is required", ErrInvalidInput)
	}
	if description == "" {
		description = " "
	}
	if !validPriority(priority) {
		priority = "normal"
	}
	now := nowRFC()
	var ticket *Ticket
	err := s.tx(func(tx *sql.Tx) error {
		number, err := nextTicketNumber(tx)
		if err != nil {
			return fmt.Errorf("mint ticket number: %w", err)
		}
		res, err := tx.Exec(`INSERT INTO tickets (number, title, description, status, priority, category_id, created_by, created_at, updated_at)
			VALUES (?, ?, ?, 'open', ?, ?, ?, ?, ?)`,
			number, title, description, priority, nullInt64(categoryID), creatorID, now, now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if err := logEvent(tx, id, creatorID, "created", "Ticket created"); err != nil {
			return err
		}
		ticket = &Ticket{ID: id, Number: number, Title: title, Description: description,
			Status: "open", Priority: priority, CreatedBy: creatorID, CreatedAt: now, UpdatedAt: now}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ticket, nil
}

func nullInt64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func validPriority(p string) bool {
	switch p {
	case "low", "normal", "high", "urgent":
		return true
	}
	return false
}

func validStatus(st string) bool {
	switch st {
	case "open", "in_progress", "closed":
		return true
	}
	return false
}

// GetTicketByNumber loads one ticket including category and people names.
func (s *Store) GetTicketByNumber(number int) (*Ticket, error) {
	row := s.DB.QueryRow(ticketSelect+` WHERE t.number = ?`, number)
	var t Ticket
	var rr int
	err := row.Scan(&t.ID, &t.Number, &t.Title, &t.Status, &t.Priority,
		&t.CategoryID, &t.CategoryName, &t.CategoryColor, &t.CreatedBy, &t.CreatorName,
		&t.AssignedTo, &t.AssigneeName, &rr, &t.CreatedAt, &t.UpdatedAt, &t.ClosedAt, &t.ReplyCount, &t.LastActivity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.ReopenRequested = rr == 1
	if err := s.DB.QueryRow(`SELECT description FROM tickets WHERE id = ?`, t.ID).Scan(&t.Description); err != nil {
		return nil, err
	}
	return &t, nil
}

// TicketOwner returns the id of the user who created a ticket.
func (s *Store) TicketOwner(ticketID int64) (int64, error) {
	var owner int64
	err := s.DB.QueryRow(`SELECT created_by FROM tickets WHERE id = ?`, ticketID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return owner, err
}

func touchTicket(tx *sql.Tx, id int64) error {
	_, err := tx.Exec(`UPDATE tickets SET updated_at = ? WHERE id = ?`, nowRFC(), id)
	return err
}

func logEvent(tx *sql.Tx, ticketID, actorID int64, kind, detail string) error {
	_, err := tx.Exec(`INSERT INTO events (ticket_id, actor_id, kind, detail, created_at) VALUES (?, ?, ?, ?, ?)`,
		ticketID, nullInt64(actorID), kind, detail, nowRFC())
	return err
}

// AddPost appends a reply (public or internal note) to the thread.
func (s *Store) AddPost(ticketID, authorID int64, body string, internal bool) (*Post, error) {
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("%w: reply is empty", ErrInvalidInput)
	}
	var internalInt int
	if internal {
		internalInt = 1
	}
	now := nowRFC()
	var p *Post
	err := s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO posts (ticket_id, author_id, body, internal, created_at) VALUES (?, ?, ?, ?, ?)`,
			ticketID, authorID, body, internalInt, now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if err := touchTicket(tx, ticketID); err != nil {
			return err
		}
		p = &Post{ID: id, TicketID: ticketID, AuthorID: authorID, Body: body, Internal: internal, CreatedAt: now}
		return nil
	})
	return p, err
}

type Post struct {
	ID         int64
	TicketID   int64
	AuthorID   int64
	AuthorName string
	AuthorRole string
	Body       string
	Internal   bool
	CreatedAt  string
}

type TicketEvent struct {
	ID        int64
	TicketID  int64
	ActorName string
	Kind      string
	Detail    string
	CreatedAt string
}

// Thread returns the interleaved public posts and events for a ticket.
// when viewerIsAdmin is false, internal notes are omitted.
func (s *Store) Thread(ticketID int64, viewerIsAdmin bool) ([]Post, []TicketEvent, error) {
	q := `SELECT p.id, p.author_id, u.display_name, u.role, p.body, p.internal, p.created_at
		FROM posts p JOIN users u ON u.id = p.author_id WHERE p.ticket_id = ?`
	if !viewerIsAdmin {
		q += ` AND p.internal = 0`
	}
	q += ` ORDER BY p.created_at, p.id`
	rows, err := s.DB.Query(q, ticketID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		var p Post
		var internal int
		if err := rows.Scan(&p.ID, &p.AuthorID, &p.AuthorName, &p.AuthorRole, &p.Body, &internal, &p.CreatedAt); err != nil {
			return nil, nil, err
		}
		p.Internal = internal == 1
		posts = append(posts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	evRows, err := s.DB.Query(`SELECT e.id, COALESCE(u.display_name, 'System'), e.kind, e.detail, e.created_at
		FROM events e LEFT JOIN users u ON u.id = e.actor_id WHERE e.ticket_id = ? ORDER BY e.created_at, e.id`, ticketID)
	if err != nil {
		return nil, nil, err
	}
	defer evRows.Close()
	var events []TicketEvent
	for evRows.Next() {
		var e TicketEvent
		if err := evRows.Scan(&e.ID, &e.ActorName, &e.Kind, &e.Detail, &e.CreatedAt); err != nil {
			return nil, nil, err
		}
		events = append(events, e)
	}
	return posts, events, evRows.Err()
}

// AssignTicket sets or clears the assignee. Passing assigneeID 0 unassigns.
func (s *Store) AssignTicket(ticketID, actorID, assigneeID int64) error {
	return s.tx(func(tx *sql.Tx) error {
		var cur sql.NullInt64
		var status string
		if err := tx.QueryRow(`SELECT assigned_to, status FROM tickets WHERE id = ?`, ticketID).Scan(&cur, &status); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE tickets SET assigned_to = ? WHERE id = ?`, nullInt64(assigneeID), ticketID); err != nil {
			return err
		}
		if assigneeID > 0 {
			name, err := displayName(tx, assigneeID)
			if err != nil {
				return err
			}
			if err := logEvent(tx, ticketID, actorID, "assigned", "Assigned to "+name); err != nil {
				return err
			}
			if status == "open" {
				if err := setStatus(tx, ticketID, actorID, "in_progress"); err != nil {
					return err
				}
			}
		} else {
			if err := logEvent(tx, ticketID, actorID, "unassigned", "Assignment cleared"); err != nil {
				return err
			}
		}
		return touchTicket(tx, ticketID)
	})
}

func displayName(tx *sql.Tx, userID int64) (string, error) {
	var n string
	err := tx.QueryRow(`SELECT display_name FROM users WHERE id = ?`, userID).Scan(&n)
	return n, err
}

func setStatus(tx *sql.Tx, ticketID, actorID int64, status string) error {
	var cur string
	if err := tx.QueryRow(`SELECT status FROM tickets WHERE id = ?`, ticketID).Scan(&cur); err != nil {
		return err
	}
	if cur == status {
		return nil
	}
	now := nowRFC()
	var closedAt any
	if status == "closed" {
		closedAt = now
	}
	if _, err := tx.Exec(`UPDATE tickets SET status = ?, closed_at = ?, reopen_requested = 0, updated_at = ? WHERE id = ?`,
		status, closedAt, now, ticketID); err != nil {
		return err
	}
	return logEvent(tx, ticketID, actorID, "status", "Status: "+statusLabel(cur)+" \u2192 "+statusLabel(status))
}

func statusLabel(s string) string {
	switch s {
	case "open":
		return "Open"
	case "in_progress":
		return "In progress"
	case "closed":
		return "Closed"
	}
	return s
}

func (s *Store) SetStatus(ticketID, actorID int64, status string) error {
	if !validStatus(status) {
		return ErrInvalidInput
	}
	return s.tx(func(tx *sql.Tx) error {
		if err := setStatus(tx, ticketID, actorID, status); err != nil {
			return err
		}
		return touchTicket(tx, ticketID)
	})
}

func (s *Store) SetPriority(ticketID, actorID int64, priority string) error {
	if !validPriority(priority) {
		return ErrInvalidInput
	}
	return s.tx(func(tx *sql.Tx) error {
		var cur string
		if err := tx.QueryRow(`SELECT priority FROM tickets WHERE id = ?`, ticketID).Scan(&cur); err != nil {
			return err
		}
		if cur != priority {
			if _, err := tx.Exec(`UPDATE tickets SET priority = ? WHERE id = ?`, priority, ticketID); err != nil {
				return err
			}
			if err := logEvent(tx, ticketID, actorID, "priority", "Priority: "+cur+" \u2192 "+priority); err != nil {
				return err
			}
		}
		return touchTicket(tx, ticketID)
	})
}

func (s *Store) SetCategory(ticketID, actorID int64, categoryID int64) error {
	return s.tx(func(tx *sql.Tx) error {
		var cur sql.NullInt64
		var curName sql.NullString
		err := tx.QueryRow(`SELECT t.category_id, c.name FROM tickets t LEFT JOIN categories c ON c.id = t.category_id WHERE t.id = ?`,
			ticketID).Scan(&cur, &curName)
		if err != nil {
			return err
		}
		var newName string
		if categoryID > 0 {
			if err := tx.QueryRow(`SELECT name FROM categories WHERE id = ?`, categoryID).Scan(&newName); err != nil {
				return err
			}
		}
		newLabel := "(none)"
		if categoryID > 0 {
			newLabel = newName
		}
		oldLabel := "(none)"
		if curName.Valid {
			oldLabel = curName.String
		}
		if (cur.Valid && cur.Int64 == categoryID) || (!cur.Valid && categoryID == 0) {
			return nil
		}
		if _, err := tx.Exec(`UPDATE tickets SET category_id = ? WHERE id = ?`, nullInt64(categoryID), ticketID); err != nil {
			return err
		}
		if err := logEvent(tx, ticketID, actorID, "category", "Category: "+oldLabel+" \u2192 "+newLabel); err != nil {
			return err
		}
		return touchTicket(tx, ticketID)
	})
}

// MarkReopenRequested records that a user asked for a closed ticket to be reopened.
func (s *Store) MarkReopenRequested(ticketID, actorID int64) error {
	return s.tx(func(tx *sql.Tx) error {
		var current int
		if err := tx.QueryRow(`SELECT reopen_requested FROM tickets WHERE id = ?`, ticketID).Scan(&current); err != nil {
			return err
		}
		if current == 1 {
			return nil
		}
		if _, err := tx.Exec(`UPDATE tickets SET reopen_requested = 1 WHERE id = ?`, ticketID); err != nil {
			return err
		}
		if err := logEvent(tx, ticketID, actorID, "reopen_request", "Reopen requested"); err != nil {
			return err
		}
		return touchTicket(tx, ticketID)
	})
}

// DismissReopenRequest clears the reopen-request flag without reopening.
func (s *Store) DismissReopenRequest(ticketID, actorID int64) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE tickets SET reopen_requested = 0 WHERE id = ?`, ticketID); err != nil {
			return err
		}
		if err := logEvent(tx, ticketID, actorID, "reopen_dismiss", "Reopen request dismissed"); err != nil {
			return err
		}
		return touchTicket(tx, ticketID)
	})
}

// --- categories ---

type Category struct {
	ID     int64
	Name   string
	Color  string
	Active bool
}

func (s *Store) ListCategories(includeInactive bool) ([]Category, error) {
	q := `SELECT id, name, color, active FROM categories`
	if !includeInactive {
		q += ` WHERE active = 1`
	}
	q += ` ORDER BY name`
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		var a int
		if err := rows.Scan(&c.ID, &c.Name, &c.Color, &a); err != nil {
			return nil, err
		}
		c.Active = a == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateCategory(name, color string) error {
	if strings.TrimSpace(name) == "" {
		return ErrInvalidInput
	}
	if color == "" {
		color = "#64748b"
	}
	_, err := s.DB.Exec(`INSERT INTO categories (name, color, active, created_at) VALUES (?, ?, 1, ?)`, name, color, nowRFC())
	if isUniqueViolation(err) {
		return ErrUserExists
	}
	return err
}

func (s *Store) UpdateCategory(id int64, name, color string, active bool) error {
	if strings.TrimSpace(name) == "" {
		return ErrInvalidInput
	}
	a := 0
	if active {
		a = 1
	}
	res, err := s.DB.Exec(`UPDATE categories SET name = ?, color = ?, active = ? WHERE id = ?`, name, color, a, id)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrUserExists
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- dashboard stats ---

type Stats struct {
	Open          int
	InProgress    int
	Unassigned    int
	ReopenReqs    int
	HighPriority  int
	ClosedToday   int
	Recent        []TicketEvent
	RecentTicketN map[int64]int // ticket id -> number, for linking recent events
}

func (s *Store) DashboardStats() (*Stats, error) {
	st := &Stats{RecentTicketN: map[int64]int{}}
	count := func(q string, args ...any) int {
		var n int
		s.DB.QueryRow(q, args...).Scan(&n)
		return n
	}
	st.Open = count(`SELECT COUNT(*) FROM tickets WHERE status = 'open'`)
	st.InProgress = count(`SELECT COUNT(*) FROM tickets WHERE status = 'in_progress'`)
	st.Unassigned = count(`SELECT COUNT(*) FROM tickets WHERE assigned_to IS NULL AND status != 'closed'`)
	st.ReopenReqs = count(`SELECT COUNT(*) FROM tickets WHERE reopen_requested = 1`)
	st.HighPriority = count(`SELECT COUNT(*) FROM tickets WHERE priority IN ('high','urgent') AND status != 'closed'`)
	st.ClosedToday = count(`SELECT COUNT(*) FROM tickets WHERE status = 'closed' AND closed_at >= ?`,
		startOfTodayUTC())

	rows, err := s.DB.Query(`SELECT e.id, e.ticket_id, COALESCE(u.display_name, 'System'), e.kind, e.detail, e.created_at, COALESCE(t.number, 0)
		FROM events e
		LEFT JOIN users u ON u.id = e.actor_id
		LEFT JOIN tickets t ON t.id = e.ticket_id
		ORDER BY e.id DESC LIMIT 12`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Note: no nested queries in this loop — the pool holds one connection and
	// iterating these rows owns it for the duration of the loop.
	for rows.Next() {
		var e TicketEvent
		var num int
		if err := rows.Scan(&e.ID, &e.TicketID, &e.ActorName, &e.Kind, &e.Detail, &e.CreatedAt, &num); err != nil {
			return nil, err
		}
		st.Recent = append(st.Recent, e)
		st.RecentTicketN[e.TicketID] = num
	}
	return st, rows.Err()
}
