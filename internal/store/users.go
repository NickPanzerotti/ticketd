package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionTTL = 7 * 24 * time.Hour

var (
	ErrNotFound     = errors.New("not found")
	ErrUserExists   = errors.New("username already exists")
	ErrBadPassword  = errors.New("incorrect username or password")
	ErrInactiveUser = errors.New("account is disabled")
	ErrInvalidInput = errors.New("invalid input")
)

type User struct {
	ID          int64
	Username    string
	DisplayName string
	Email       string // optional; destination for reply notifications
	Role        string // "admin" | "user"
	Active      bool
	CreatedAt   string
}

func (u *User) IsAdmin() bool { return u.Role == "admin" }

const userCols = `id, username, display_name, role, active, created_at, COALESCE(email,'')`

func (s *Store) CreateUser(username, displayName, password, role string) (*User, error) {
	if username == "" || password == "" || displayName == "" {
		return nil, ErrInvalidInput
	}
	if role != "admin" && role != "user" {
		return nil, ErrInvalidInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	now := nowRFC()
	res, err := s.DB.Exec(`INSERT INTO users (username, display_name, password_hash, role, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`, username, displayName, string(hash), role, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, DisplayName: displayName, Role: role, Active: true, CreatedAt: now}, nil
}

func (s *Store) GetUser(id int64) (*User, error) {
	row := s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) GetUserByName(username string) (*User, error) {
	row := s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.DB.Query(`SELECT ` + userCols + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsers(rows)
}

// ListActiveAdmins returns users that tickets can be assigned to.
func (s *Store) ListActiveAdmins() ([]User, error) {
	rows, err := s.DB.Query(`SELECT ` + userCols + ` FROM users WHERE role = 'admin' AND active = 1 ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsers(rows)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(sc scanner) (*User, error) {
	var u User
	var active int
	err := sc.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &active, &u.CreatedAt, &u.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Active = active == 1
	return &u, nil
}

func scanUsers(rows *sql.Rows) ([]User, error) {
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) SetActive(userID int64, active bool) error {
	a := 0
	if active {
		a = 1
	}
	if _, err := s.DB.Exec(`UPDATE users SET active = ?, updated_at = ? WHERE id = ?`, a, nowRFC(), userID); err != nil {
		return err
	}
	if !active {
		s.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	}
	return nil
}

func (s *Store) SetRole(userID int64, role string) error {
	if role != "admin" && role != "user" {
		return ErrInvalidInput
	}
	_, err := s.DB.Exec(`UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, role, nowRFC(), userID)
	return err
}

// emailRe is a deliberately light sanity check, not full RFC validation:
// something@somewhere.tld.
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func ValidEmail(email string) bool {
	return emailRe.MatchString(email)
}

// SetEmail sets (or clears, with "") the notification address for an account.
func (s *Store) SetEmail(userID int64, email string) error {
	if email != "" && !ValidEmail(email) {
		return fmt.Errorf("%w: invalid email", ErrInvalidInput)
	}
	_, err := s.DB.Exec(`UPDATE users SET email = nullif(?, ''), updated_at = ? WHERE id = ?`, email, nowRFC(), userID)
	return err
}

func (s *Store) SetPassword(userID int64, password string) error {
	if password == "" {
		return ErrInvalidInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, string(hash), nowRFC(), userID); err != nil {
		return err
	}
	s.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return nil
}

// HasAnyAdmin reports whether at least one admin account exists.
func (s *Store) HasAnyAdmin() (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND active = 1`).Scan(&n)
	return n > 0, err
}

// Authenticate validates credentials and returns the user.
func (s *Store) Authenticate(username, password string) (*User, error) {
	var hash string
	err := s.DB.QueryRow(`SELECT password_hash FROM users WHERE username = ?`, username).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBadPassword // do not leak whether the user exists
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrBadPassword
	}
	u, err := s.GetUserByName(username)
	if err != nil {
		return nil, err
	}
	if !u.Active {
		return nil, ErrInactiveUser
	}
	return u, nil
}

// --- sessions ---

type Session struct {
	ID      string
	UserID  int64
	CSRF    string
	User    *User
	Expires time.Time
}

func randomToken(nBytes int) string {
	b := make([]byte, nBytes)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) CreateSession(userID int64) (*Session, error) {
	id := randomToken(32)
	csrf := randomToken(16)
	now := time.Now().UTC()
	_, err := s.DB.Exec(`INSERT INTO sessions (id, user_id, csrf, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, userID, csrf, now.Format(time.RFC3339), now.Format(time.RFC3339), now.Add(sessionTTL).Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	u, err := s.GetUser(userID)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, UserID: userID, CSRF: csrf, User: u, Expires: now.Add(sessionTTL)}, nil
}

// GetSession loads a live session and slides its expiry; returns nil when absent or expired.
func (s *Store) GetSession(id string) (*Session, error) {
	var userID int64
	var csrf, expires string
	err := s.DB.QueryRow(`SELECT user_id, csrf, expires_at FROM sessions WHERE id = ?`, id).Scan(&userID, &csrf, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	exp, err := time.Parse(time.RFC3339, expires)
	if err != nil || time.Now().UTC().After(exp) {
		s.DB.Exec(`DELETE FROM sessions WHERE id = ?`, id)
		return nil, nil
	}
	u, err := s.GetUser(userID)
	if errors.Is(err, ErrNotFound) || (err == nil && !u.Active) {
		s.DB.Exec(`DELETE FROM sessions WHERE id = ?`, id)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Slide expiry on use, at most every 5 minutes to avoid write churn.
	threshold := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
	newExp := time.Now().UTC().Add(sessionTTL)
	res, err := s.DB.Exec(`UPDATE sessions SET expires_at = ?, last_seen_at = ? WHERE id = ? AND last_seen_at < ?`,
		newExp.Format(time.RFC3339), nowRFC(), id, threshold)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			exp = newExp
		}
	}
	return &Session{ID: id, UserID: userID, CSRF: csrf, User: u, Expires: exp}, nil
}

func (s *Store) DeleteSession(id string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *Store) DeleteExpiredSessions() {
	s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, nowRFC())
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
