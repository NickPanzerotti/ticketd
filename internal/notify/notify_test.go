package notify

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"ticketd/internal/store"
)

func selfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"mail.internal"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestParseCA(t *testing.T) {
	pool, err := ParseCA("")
	if err != nil || pool != nil {
		t.Fatal("empty CA input must mean 'use embedded roots'")
	}
	if _, err := ParseCA("not a certificate"); err == nil {
		t.Fatal("garbage accepted")
	}
	pool, err = ParseCA(selfSignedPEM(t))
	if err != nil || pool == nil {
		t.Fatalf("valid PEM rejected: %v", err)
	}
}

func TestTLSConfigPolicy(t *testing.T) {
	// Default: verify against the system/embedded roots for the SMTP host.
	tc, err := tlsConfig(store.NotifySettings{SMTPHost: "mail.internal"})
	if err != nil {
		t.Fatal(err)
	}
	if tc.RootCAs != nil || tc.InsecureSkipVerify || tc.ServerName != "mail.internal" {
		t.Fatalf("unexpected defaults: %+v", tc)
	}

	// Custom CA pool.
	tc, err = tlsConfig(store.NotifySettings{SMTPHost: "mail.internal", SMTPCACert: selfSignedPEM(t)})
	if err != nil || tc.RootCAs == nil || tc.InsecureSkipVerify {
		t.Fatalf("custom CA not applied: %v", err)
	}

	// Explicit opt-out.
	tc, err = tlsConfig(store.NotifySettings{SMTPHost: "mail.internal", SMTPInsecureTLS: true})
	if err != nil || !tc.InsecureSkipVerify {
		t.Fatalf("insecure flag not honored: %v", err)
	}

	// Invalid CA PEM is an error, not a silent fallback.
	if _, err := tlsConfig(store.NotifySettings{SMTPHost: "h", SMTPCACert: "junk"}); err == nil {
		t.Fatal("invalid CA must error")
	}
}

// captureMailer records what would be sent.
type captureMailer struct{ msgs []Message }

func (m *captureMailer) Send(cfg store.NotifySettings, msg Message) error {
	m.msgs = append(m.msgs, msg)
	return nil
}

func TestNewTicketNotify(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()

	admin, _ := st.CreateUser("admin", "Admin Dana", "pw12345678", "admin")
	alice, _ := st.CreateUser("alice", "Alice", "pw12345678", "user")
	if err := st.SetEmail(admin.ID, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	tk, err := st.CreateTicket("Printer on fire", "actual flames", "normal", 0, alice.ID)
	if err != nil {
		t.Fatal(err)
	}

	on := store.NotifySettings{Enabled: true, AdminNewTicket: true, SMTPHost: "h",
		PublicURL: "https://it.example.com"}
	m := &captureMailer{}
	if err := NewTicket(st, m, on, "IT-", tk, alice.ID, "Alice"); err != nil {
		t.Fatal(err)
	}
	if len(m.msgs) != 1 {
		t.Fatalf("want one message, got %d", len(m.msgs))
	}
	msg := m.msgs[0]
	if len(msg.To) != 1 || msg.To[0] != "admin@example.com" {
		t.Fatalf("recipients: %v", msg.To)
	}
	if msg.Subject != "New ticket IT-1 — Printer on fire" {
		t.Fatalf("subject: %q", msg.Subject)
	}
	for _, want := range []string{"Alice created ticket IT-1 — Printer on fire",
		"> actual flames", "View the ticket: https://it.example.com/tickets/1",
		"because you are an IT administrator"} {
		if !strings.Contains(msg.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, msg.Body)
		}
	}

	// option off → nothing
	if err := NewTicket(st, m, store.NotifySettings{Enabled: true, SMTPHost: "h"}, "IT-", tk, alice.ID, "Alice"); err != nil {
		t.Fatal(err)
	}
	// creator is the only admin → nothing
	if err := NewTicket(st, m, on, "IT-", tk, admin.ID, "Admin Dana"); err != nil {
		t.Fatal(err)
	}
	// master switch off → nothing
	off := on
	off.Enabled = false
	if err := NewTicket(st, m, off, "IT-", tk, alice.ID, "Alice"); err != nil {
		t.Fatal(err)
	}
	if len(m.msgs) != 1 {
		t.Fatalf("expected no further sends, got %d total", len(m.msgs))
	}
}
