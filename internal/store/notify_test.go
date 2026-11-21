package store

import (
	"sort"
	"testing"
)

func TestNotifySettingsRoundTrip(t *testing.T) {
	s := newTestStore(t)

	ns, err := s.GetNotifySettings()
	if err != nil {
		t.Fatal(err)
	}
	if ns.Enabled || ns.SMTPPort != 587 || ns.NotifyScope != NotifyScopeParticipants || ns.AdminNewTicket {
		t.Fatalf("unexpected defaults: %+v", ns)
	}

	want := NotifySettings{
		Enabled: true, SMTPHost: "mail.example.com", SMTPPort: 465,
		SMTPUser: "relay", SMTPPassword: "secret", SMTPFrom: "ticketd@example.com",
		PublicURL: "https://it.example.com", NotifyScope: NotifyScopeCreator,
		SMTPCACert: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----", SMTPInsecureTLS: true,
		AdminNewTicket: true,
	}
	if err := s.SetNotifySettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNotifySettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	// An empty password on save must keep the stored secret.
	want.SMTPPassword = ""
	if err := s.SetNotifySettings(want); err != nil {
		t.Fatal(err)
	}
	want.SMTPPassword = "secret"
	got, _ = s.GetNotifySettings()
	if got != want {
		t.Fatalf("password should be preserved on empty save:\n got %+v", got)
	}
}

func TestSetEmailAndNotifyRecipients(t *testing.T) {
	s := newTestStore(t)

	admin, _ := s.CreateUser("admin", "Admin", "pw12345678", "admin")
	alice, _ := s.CreateUser("alice", "Alice", "pw12345678", "user")
	bob, _ := s.CreateUser("bob", "Bob", "pw12345678", "user")
	if err := s.SetEmail(admin.ID, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEmail(alice.ID, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEmail(bob.ID, "not-an-email"); err == nil {
		t.Fatal("invalid email accepted")
	}

	tk, err := s.CreateTicket("Notifier", "desc", "normal", 0, admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	// alice replies publicly; admin adds an internal note (still a participant).
	if _, err := s.AddPost(tk.ID, alice.ID, "alice reply", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPost(tk.ID, admin.ID, "admin note", true); err != nil {
		t.Fatal(err)
	}

	// Participants scope, bob replies: creator + alice (+ admin who noted), minus bob (none).
	got, err := s.TicketNotifyEmails(tk.ID, bob.ID, NotifyScopeParticipants)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"admin@example.com", "alice@example.com"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("participants scope: got %v, want %v", got, want)
	}

	// Creator scope: admin replies → creator is admin, excluded → nobody.
	got, err = s.TicketNotifyEmails(tk.ID, admin.ID, NotifyScopeCreator)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("creator scope: got %v, want none", got)
	}
	// Creator scope: alice replies → the creator (admin) is notified.
	got, err = s.TicketNotifyEmails(tk.ID, alice.ID, NotifyScopeCreator)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "admin@example.com" {
		t.Fatalf("creator scope: got %v", got)
	}

	// Removing an address opts that user out.
	if err := s.SetEmail(alice.ID, ""); err != nil {
		t.Fatal(err)
	}
	u, _ := s.GetUser(alice.ID)
	if u.Email != "" {
		t.Fatal("email not cleared")
	}
	got, _ = s.TicketNotifyEmails(tk.ID, admin.ID, NotifyScopeParticipants)
	for _, e := range got {
		if e == "alice@example.com" {
			t.Fatal("cleared address still notified")
		}
	}
}

// AdminNotifyEmails: all active admins with addresses, excluding the given
// user; admins without an email or deactivated accounts are skipped.
func TestAdminNotifyEmails(t *testing.T) {
	s := newTestStore(t)

	a1, _ := s.CreateUser("admin1", "Admin One", "pw12345678", "admin")
	a2, _ := s.CreateUser("admin2", "Admin Two", "pw12345678", "admin")
	a4, _ := s.CreateUser("admin4", "Admin Four", "pw12345678", "admin")
	u1, _ := s.CreateUser("user1", "User One", "pw12345678", "user")
	if _, err := s.CreateUser("admin3", "Admin Three", "pw12345678", "admin"); err != nil { // no email address
		t.Fatal(err)
	}
	for id, e := range map[int64]string{a1.ID: "a1@example.com", a2.ID: "a2@example.com", a4.ID: "gone@example.com", u1.ID: "u1@example.com"} {
		if err := s.SetEmail(id, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetActive(a4.ID, false); err != nil {
		t.Fatal(err)
	}

	got, err := s.AdminNotifyEmails(u1.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a1@example.com", "a2@example.com"} // a3 has no email, a4 is inactive
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}

	// the acting admin is excluded from their own notification
	got, _ = s.AdminNotifyEmails(a1.ID)
	if len(got) != 1 || got[0] != "a2@example.com" {
		t.Fatalf("creator exclusion: got %v", got)
	}
}

// Reopening an already-migrated database must be a no-op (guards the
// migration cursor logic: with two migrations an unmaintained cursor used
// to fail with "missing migration 2").
func TestReopenDoesNotReapplyMigrations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.DB.Close()
	var version int
	if err := s2.DB.QueryRow(`SELECT version FROM meta WHERE id = 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
	var cats int
	if err := s2.DB.QueryRow(`SELECT COUNT(*) FROM categories`).Scan(&cats); err != nil {
		t.Fatal(err)
	}
	if cats != 6 {
		t.Fatalf("seed re-applied on reopen: %d categories", cats)
	}
}
