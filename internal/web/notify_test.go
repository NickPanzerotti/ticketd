package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"ticketd/internal/notify"
	"ticketd/internal/store"
)

// recordingMailer captures what the app tries to send.
type recordingMailer struct {
	sent chan notify.Message
}

func (m *recordingMailer) Send(cfg store.NotifySettings, msg notify.Message) error {
	if !cfg.Enabled || cfg.SMTPHost == "" {
		panic("mailer invoked while notifications disabled/unconfigured")
	}
	m.sent <- msg
	return nil
}

// testCertPEM creates a throwaway self-signed certificate for round-trip tests.
func testCertPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"mail.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func waitForMail(t *testing.T, m *recordingMailer, d time.Duration) (notify.Message, bool) {
	t.Helper()
	select {
	case msg := <-m.sent:
		return msg, true
	case <-time.After(d):
		return notify.Message{}, false
	}
}

func TestEmailNotificationsEndToEnd(t *testing.T) {
	srv, st, app := newTestApp(t)
	base := srv.URL
	mailer := &recordingMailer{sent: make(chan notify.Message, 8)}
	app.mailer = mailer

	admin, err := st.CreateUser("admin", "Admin Dana", "pw12345678", "admin")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := st.CreateUser("alice", "Alice", "pw12345678", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmail(admin.ID, "admin@example.com"); err != nil {
		t.Fatal(err)
	}

	// regular users cannot reach the settings page
	uc := newClient(t)
	login(t, uc, base, "alice", "pw12345678")
	if code, _ := get(t, uc, base+"/admin/settings"); code != http.StatusForbidden {
		t.Fatalf("settings as user: want 403, got %d", code)
	}

	// admin configures notifications through the web UI
	ac := newClient(t)
	login(t, ac, base, "admin", "pw12345678")
	csrf := csrfOf(t, ac, base+"/admin/settings")
	if resp := post(t, ac, base+"/admin/settings", map[string]string{
		"csrf": csrf, "enabled": "1", "smtp_host": "127.0.0.1", "smtp_port": "2525",
		"smtp_from": "ticketd@example.com", "notify_scope": "participants", "public_url": "https://it.example.com",
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save settings: %d", resp.StatusCode)
	}
	if _, body := get(t, ac, base+"/admin/settings"); !strings.Contains(body, `name="enabled" value="1" checked`) {
		t.Fatal("settings page should show notifications enabled after save")
	}

	// alice (no email yet) opens a ticket
	csrf = csrfOf(t, uc, base+"/tickets/new")
	if resp, err := uc.PostForm(base+"/tickets", map[string][]string{
		"csrf": {csrf}, "title": {"Printer on fire"}, "description": {"seriously"},
	}); err != nil {
		t.Fatal(err)
	} else if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}

	// admin replies: alice has no address yet, admin is the author → nothing.
	csrf = csrfOf(t, ac, base+"/tickets/1")
	if resp := post(t, ac, base+"/tickets/1/reply", map[string]string{"csrf": csrf, "body": "looking into it"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("reply: %d", resp.StatusCode)
	}
	if _, ok := waitForMail(t, mailer, 400*time.Millisecond); ok {
		t.Fatal("no email expected: author excluded and creator has no address")
	}

	// alice adds her own address through the account page, then follows up.
	csrf = csrfOf(t, uc, base+"/account")
	if resp := post(t, uc, base+"/account/email", map[string]string{"csrf": csrf, "email": "alice@example.com"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("self email: %d", resp.StatusCode)
	}
	if u, _ := st.GetUser(alice.ID); u.Email != "alice@example.com" {
		t.Fatal("account page did not save the email")
	}

	// admin replies again → alice (creator) receives exactly one message.
	csrf = csrfOf(t, ac, base+"/tickets/1")
	if resp := post(t, ac, base+"/tickets/1/reply", map[string]string{"csrf": csrf, "body": "it is fixed now"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("reply 2: %d", resp.StatusCode)
	}
	msg, ok := waitForMail(t, mailer, 3*time.Second)
	if !ok {
		t.Fatal("expected one notification for alice")
	}
	if len(msg.To) != 1 || msg.To[0] != "alice@example.com" {
		t.Fatalf("recipients: %v", msg.To)
	}
	if !strings.Contains(msg.Subject, "Printer on fire") {
		t.Fatalf("subject: %q", msg.Subject)
	}
	if !strings.Contains(msg.Body, "Admin Dana") || !strings.Contains(msg.Body, "it is fixed now") ||
		!strings.Contains(msg.Body, "https://it.example.com/tickets/1") {
		t.Fatalf("email body missing details: %q", msg.Body)
	}

	// internal notes never send email
	csrf = csrfOf(t, ac, base+"/tickets/1")
	if resp := post(t, ac, base+"/tickets/1/reply", map[string]string{"csrf": csrf, "body": "hidden note", "internal": "1"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("internal reply: %d", resp.StatusCode)
	}
	if _, ok := waitForMail(t, mailer, 400*time.Millisecond); ok {
		t.Fatal("internal note must not send email")
	}

	// disabling notifications stops delivery
	csrf = csrfOf(t, ac, base+"/admin/settings")
	if resp := post(t, ac, base+"/admin/settings", map[string]string{
		"csrf": csrf, "smtp_host": "127.0.0.1", "smtp_port": "2525",
		"smtp_from": "ticketd@example.com", "notify_scope": "participants",
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save settings off: %d", resp.StatusCode)
	}
	csrf = csrfOf(t, ac, base+"/tickets/1")
	if resp := post(t, ac, base+"/tickets/1/reply", map[string]string{"csrf": csrf, "body": "one more"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("reply 3: %d", resp.StatusCode)
	}
	if _, ok := waitForMail(t, mailer, 400*time.Millisecond); ok {
		t.Fatal("notifications were disabled; no email expected")
	}

	// enabled without any From/username must be rejected on save
	csrf = csrfOf(t, ac, base+"/admin/settings")
	if resp := post(t, ac, base+"/admin/settings", map[string]string{
		"csrf": csrf, "enabled": "1", "smtp_host": "127.0.0.1", "smtp_port": "2525",
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save settings no-from: %d", resp.StatusCode)
	}
	if _, body := get(t, ac, base+"/admin/settings"); strings.Contains(body, `name="enabled" value="1" checked`) {
		t.Fatal("enabled without a From address must not be accepted")
	}

	// certificate options: invalid PEM is rejected, valid PEM round-trips into the form
	csrf = csrfOf(t, ac, base+"/admin/settings")
	if resp := post(t, ac, base+"/admin/settings", map[string]string{
		"csrf": csrf, "enabled": "0", "smtp_ca_pem": "not a certificate",
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save bad-ca: %d", resp.StatusCode)
	}
	if _, body := get(t, ac, base+"/admin/settings"); strings.Contains(body, "not a certificate") {
		t.Fatal("invalid CA PEM must not be saved")
	}
	goodCA := testCertPEM(t)
	csrf = csrfOf(t, ac, base+"/admin/settings")
	if resp := post(t, ac, base+"/admin/settings", map[string]string{
		"csrf": csrf, "smtp_ca_pem": goodCA, "smtp_tls_insecure": "1",
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save ca: %d", resp.StatusCode)
	}
	_, body := get(t, ac, base+"/admin/settings")
	if !strings.Contains(body, "BEGIN CERTIFICATE") || !strings.Contains(body, `name="smtp_tls_insecure" value="1" checked`) {
		t.Fatal("certificate options must round-trip through the settings page")
	}

	// admin-new-ticket emails: enabled → creating a ticket notifies active admins
	csrf = csrfOf(t, ac, base+"/admin/settings")
	if resp := post(t, ac, base+"/admin/settings", map[string]string{
		"csrf": csrf, "enabled": "1", "smtp_host": "127.0.0.1", "smtp_port": "2525",
		"smtp_from": "ticketd@example.com", "notify_admin_new": "1",
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save admin-new on: %d", resp.StatusCode)
	}
	_, body = get(t, ac, base+"/admin/settings")
	if !strings.Contains(body, `name="notify_admin_new" value="1" checked`) {
		t.Fatal("admin-new checkbox should render as checked after save")
	}

	csrf = csrfOf(t, uc, base+"/tickets/new")
	if resp, err := uc.PostForm(base+"/tickets", map[string][]string{
		"csrf": {csrf}, "title": {"Monitor flicker"}, "description": {"worse on Mondays"},
	}); err != nil {
		t.Fatal(err)
	} else if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create ticket 2: %d", resp.StatusCode)
	}
	msg, ok = waitForMail(t, mailer, 3*time.Second)
	if !ok {
		t.Fatal("expected admin notification for the new ticket")
	}
	if len(msg.To) != 1 || msg.To[0] != "admin@example.com" {
		t.Fatalf("admin-new recipients: %v (creator and non-admins must not be included)", msg.To)
	}
	if !strings.Contains(msg.Subject, "Monitor flicker") {
		t.Fatalf("admin-new subject: %q", msg.Subject)
	}
	if !strings.Contains(msg.Body, "Alice created ticket") || !strings.Contains(msg.Body, "worse on Mondays") {
		t.Fatalf("admin-new body: %q", msg.Body)
	}

	// turning the option off stops admin notifications (replies still work)
	csrf = csrfOf(t, ac, base+"/admin/settings")
	if resp := post(t, ac, base+"/admin/settings", map[string]string{
		"csrf": csrf, "enabled": "1", "smtp_host": "127.0.0.1", "smtp_port": "2525",
		"smtp_from": "ticketd@example.com",
	}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save admin-new off: %d", resp.StatusCode)
	}
	csrf = csrfOf(t, uc, base+"/tickets/new")
	if resp, err := uc.PostForm(base+"/tickets", map[string][]string{
		"csrf": {csrf}, "title": {"Quiet queue"}, "description": {"no email please"},
	}); err != nil {
		t.Fatal(err)
	} else if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create ticket 3: %d", resp.StatusCode)
	}
	if _, ok := waitForMail(t, mailer, 400*time.Millisecond); ok {
		t.Fatal("admin-new was disabled; no email expected")
	}
}
