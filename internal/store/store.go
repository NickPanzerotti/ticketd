// Package store provides SQLite-backed persistence for ticketd.
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store wraps the SQLite database and the uploads directory.
type Store struct {
	DB        *sql.DB
	DataDir   string
	UploadDir string
}

// Open opens (creating if needed) the database inside dir and runs migrations.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "uploads"), 0o750); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	dsn := "file:" + filepath.Join(dir, "ticketd.db") +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1) // serialise access; plenty for this app
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	s := &Store{DB: db, DataDir: dir, UploadDir: filepath.Join(dir, "uploads")}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	var version int
	var found string
	err := s.DB.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.DB.Exec(`CREATE TABLE meta (id INTEGER PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
			return fmt.Errorf("create meta: %w", err)
		}
		version = 0
	} else if err != nil {
		return fmt.Errorf("probe meta: %w", err)
	} else if err := s.DB.QueryRow(`SELECT version FROM meta WHERE id = 1`).Scan(&version); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read schema version: %w", err)
	}

	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for i, f := range files {
		want := i + 1
		if version >= want {
			continue
		}
		if version < want-1 {
			return fmt.Errorf("missing migration %d", want)
		}
		sqlBytes, err := migrations.ReadFile("migrations/" + f.Name())
		if err != nil {
			return err
		}
		err = s.tx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(string(sqlBytes)); err != nil {
				return fmt.Errorf("migration %s: %w", f.Name(), err)
			}
			if _, err := tx.Exec(`INSERT INTO meta (id, version) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET version = ?`, want, want); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		version = want // keep the in-memory cursor in step with the DB
	}
	return s.seed()
}

// seed inserts the default IT categories on a fresh database.
func (s *Store) seed() error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM categories`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	defaults := []struct{ name, color string }{
		{"Hardware", "#2563eb"},
		{"Software", "#7c3aed"},
		{"Network", "#0891b2"},
		{"Access & Accounts", "#16a34a"},
		{"Peripherals", "#d97706"},
		{"Other", "#64748b"},
	}
	now := nowRFC()
	return s.tx(func(tx *sql.Tx) error {
		for _, c := range defaults {
			if _, err := tx.Exec(`INSERT INTO categories (name, color, active, created_at) VALUES (?, ?, 1, ?)`, c.name, c.color, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// tx runs fn inside a transaction, committing on success.
func (s *Store) tx(fn func(tx *sql.Tx) error) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }

// startOfTodayUTC returns 00:00 UTC of the current day as RFC3339.
func startOfTodayUTC() string {
	t := time.Now().UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
