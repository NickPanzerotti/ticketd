// Package notify sends ticket-reply emails over SMTP using only the
// standard library (net/smtp), keeping the binary dependency-free.
package notify

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"ticketd/internal/store"
)

// Message is one outbound email.
type Message struct {
	To      []string
	Subject string
	Body    string
}

// Mailer sends a message with the given settings. Production uses SMTPMailer;
// tests substitute a fake that captures messages.
type Mailer interface {
	Send(cfg store.NotifySettings, msg Message) error
}

// SMTPMailer talks to the configured SMTP server.
type SMTPMailer struct{}

// ParseCA builds a certificate pool from a PEM bundle; empty input means
// "use the embedded root store".
func ParseCA(pem string) (*x509.CertPool, error) {
	if strings.TrimSpace(pem) == "" {
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		return nil, errors.New("that does not look like a PEM certificate (expected a block starting with -----BEGIN CERTIFICATE-----)")
	}
	return pool, nil
}

// tlsConfig applies the admin's certificate policy: the runtime trust
// store by default (the image ships a CA bundle), an explicit CA pool
// when provided, verification skipped only when the admin opted in.
func tlsConfig(cfg store.NotifySettings) (*tls.Config, error) {
	tc := &tls.Config{ServerName: cfg.SMTPHost}
	pool, err := ParseCA(cfg.SMTPCACert)
	if err != nil {
		return nil, err
	}
	if pool != nil {
		tc.RootCAs = pool
	}
	if cfg.SMTPInsecureTLS {
		tc.InsecureSkipVerify = true
	}
	return tc, nil
}

func (SMTPMailer) Send(cfg store.NotifySettings, msg Message) error {
	if len(msg.To) == 0 {
		return nil
	}
	from := cfg.SMTPFrom
	if from == "" {
		from = cfg.SMTPUser
	}
	if cfg.SMTPHost == "" || from == "" {
		return fmt.Errorf("SMTP host and a From address are required")
	}
	addr := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort))
	implicitTLS := cfg.SMTPPort == 465

	tlsCfg, err := tlsConfig(cfg)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	defer conn.Close()
	if implicitTLS {
		tconn := tls.Client(conn, tlsCfg)
		if err := tconn.Handshake(); err != nil {
			return fmt.Errorf("smtp tls handshake: %w", err)
		}
		conn = tconn
	}
	c, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Quit()
	if !implicitTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}
	if cfg.SMTPUser != "" {
		auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	for _, to := range msg.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(render(cfg.SMTPFrom, cfg.SMTPUser, msg))); err != nil {
		return fmt.Errorf("smtp body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end: %w", err)
	}
	return c.Quit()
}

func render(from, user string, msg Message) string {
	sender := from
	if sender == "" {
		sender = user
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", sender)
	for _, to := range msg.To {
		fmt.Fprintf(&b, "To: %s\r\n", to)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", msg.Subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(msg.Body)
	return b.String()
}

// emailBody builds the shared shape of notification bodies: an intro line,
// the quoted source text, an optional ticket link, and a reason footer.
func emailBody(intro, snippet string, cfg store.NotifySettings, number int, reason string) string {
	var text strings.Builder
	fmt.Fprintf(&text, "%s:\r\n\r\n", intro)
	if snippet != "" {
		for _, line := range strings.Split(snippet, "\n") {
			text.WriteString("> " + line + "\r\n")
		}
		text.WriteString("\r\n")
	}
	if cfg.PublicURL != "" {
		fmt.Fprintf(&text, "View the ticket: %s/tickets/%d\r\n\r\n", strings.TrimRight(cfg.PublicURL, "/"), number)
	}
	text.WriteString(reason)
	return text.String()
}

// clip truncates long text to a safe email snippet size.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 800 {
		return string(r[:800]) + "…"
	}
	return s
}

// Reply composes and sends notifications for a new public reply on t.
// It is a no-op when notifications are disabled or nobody qualifies.
func Reply(st *store.Store, m Mailer, cfg store.NotifySettings, prefix string,
	t *store.Ticket, authorID int64, authorName, body string) error {
	if !cfg.Enabled || cfg.SMTPHost == "" {
		return nil
	}
	to, err := st.TicketNotifyEmails(t.ID, authorID, cfg.NotifyScope)
	if err != nil || len(to) == 0 {
		return err
	}
	num := fmt.Sprintf("%s%d", prefix, t.Number)
	intro := fmt.Sprintf("%s replied on ticket %s — %s", authorName, num, t.Title)
	return m.Send(cfg, Message{
		To:      to,
		Subject: fmt.Sprintf("Re: %s %s", num, t.Title),
		Body: emailBody(intro, clip(body), cfg, t.Number,
			"You are receiving this email because you are part of this ticket."),
	})
}

// NewTicket emails all active admins (except the creator) about a newly
// created ticket. It is a no-op unless the admin-new-ticket option is on,
// notifications are enabled, and at least one admin has an email address.
func NewTicket(st *store.Store, m Mailer, cfg store.NotifySettings, prefix string,
	t *store.Ticket, creatorID int64, creatorName string) error {
	if !cfg.Enabled || !cfg.AdminNewTicket || cfg.SMTPHost == "" {
		return nil
	}
	to, err := st.AdminNotifyEmails(creatorID)
	if err != nil || len(to) == 0 {
		return err
	}
	num := fmt.Sprintf("%s%d", prefix, t.Number)
	intro := fmt.Sprintf("%s created ticket %s — %s", creatorName, num, t.Title)
	return m.Send(cfg, Message{
		To:      to,
		Subject: fmt.Sprintf("New ticket %s — %s", num, t.Title),
		Body: emailBody(intro, clip(t.Description), cfg, t.Number,
			"You are receiving this email because you are an IT administrator."),
	})
}
