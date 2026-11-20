package store

import (
	"database/sql"
	"errors"
	"strconv"
)

// NotifySettings is the email-notification configuration, editable by admins
// on the settings page and stored in the settings table.
type NotifySettings struct {
	Enabled      bool // master switch
	SMTPHost     string
	SMTPPort     int // 587 = STARTTLS (upgrade if offered), 465 = implicit TLS
	SMTPUser     string
	SMTPPassword string // stored in plain text in the local database file
	SMTPFrom     string // From: address; defaults to SMTPUser when empty
	PublicURL    string // base URL used in email links, e.g. https://it.example.com
	NotifyScope  string // "creator" or "participants"
}

const (
	NotifyScopeCreator      = "creator"
	NotifyScopeParticipants = "participants"
)

func (s *Store) GetNotifySettings() (NotifySettings, error) {
	ns := NotifySettings{SMTPPort: 587, NotifyScope: NotifyScopeParticipants}
	rows, err := s.DB.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return ns, err
	}
	defer rows.Close()
	vals := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return ns, err
		}
		vals[k] = v
	}
	if err := rows.Err(); err != nil {
		return ns, err
	}
	ns.Enabled = vals["notify_enabled"] == "1"
	ns.SMTPHost = vals["smtp_host"]
	if p, err := strconv.Atoi(vals["smtp_port"]); err == nil && p > 0 && p < 65536 {
		ns.SMTPPort = p
	}
	ns.SMTPUser = vals["smtp_user"]
	ns.SMTPPassword = vals["smtp_pass"]
	ns.SMTPFrom = vals["smtp_from"]
	ns.PublicURL = vals["public_url"]
	if vals["notify_scope"] == NotifyScopeCreator {
		ns.NotifyScope = NotifyScopeCreator
	}
	return ns, nil
}

// SetNotifySettings upserts all settings at once. An empty password keeps the
// existing one, so the form never needs to echo the stored secret back.
func (s *Store) SetNotifySettings(ns NotifySettings) error {
	scope := NotifyScopeParticipants
	if ns.NotifyScope == NotifyScopeCreator {
		scope = ns.NotifyScope
	}
	port := ns.SMTPPort
	if port < 1 || port > 65535 {
		port = 587
	}
	up := func(k, v string) error {
		_, err := s.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?`, k, v, v)
		return err
	}
	enabled := "0"
	if ns.Enabled {
		enabled = "1"
	}
	if err := s.tx(func(tx *sql.Tx) error {
		for _, kv := range []struct{ k, v string }{
			{"notify_enabled", enabled},
			{"smtp_host", ns.SMTPHost},
			{"smtp_port", strconv.Itoa(port)},
			{"smtp_user", ns.SMTPUser},
			{"notify_scope", scope},
			{"smtp_from", ns.SMTPFrom},
			{"public_url", ns.PublicURL},
		} {
			if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?`, kv.k, kv.v, kv.v); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if ns.SMTPPassword != "" {
		return up("smtp_pass", ns.SMTPPassword)
	}
	return nil
}

// TicketNotifyEmails returns the addresses that should be notified about a
// new reply on ticketID, excluding the user who wrote it. Scope "creator"
// covers only the ticket creator; "participants" adds everyone who has
// posted on the ticket. Accounts without an email address are skipped.
func (s *Store) TicketNotifyEmails(ticketID, excludeUserID int64, scope string) ([]string, error) {
	const base = `SELECT DISTINCT u.email FROM users u WHERE u.email IS NOT NULL AND u.email <> '' AND u.active = 1 AND u.id <> ? AND (`
	var rows *sql.Rows
	var err error
	if scope == NotifyScopeCreator {
		rows, err = s.DB.Query(base+`u.id = (SELECT created_by FROM tickets WHERE id = ?))`, excludeUserID, ticketID)
	} else {
		rows, err = s.DB.Query(base+`u.id = (SELECT created_by FROM tickets WHERE id = ?)
			OR u.id IN (SELECT author_id FROM posts WHERE ticket_id = ?))`, excludeUserID, ticketID, ticketID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, errors.Join(rows.Err(), err)
}
