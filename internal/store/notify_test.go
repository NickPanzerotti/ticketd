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
	if ns.Enabled || ns.SMTPPort != 587 || ns.NotifyScope != NotifyScopeParticipants {
		t.Fatalf("unexpected defaults: %+v", ns)
	}

	want := NotifySettings{
		Enabled: true, SMTPHost: "mail.example.com", SMTPPort: 465,
		SMTPUser: "relay", SMTPPassword: "secret", SMTPFrom: "ticketd@example.com",
		PublicURL: "https://it.example.com", NotifyScope: NotifyScopeCreator,
		SMTPCACert: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----", SMTPInsecureTLS: true,
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
