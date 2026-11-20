package store

import (
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}

func TestTicketNumbersIncrementAndNeverReuse(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser("it", "IT Admin", "pw12345678", "admin")
	if err != nil {
		t.Fatal(err)
	}

	var last int
	for i := 0; i < 5; i++ {
		tk, err := s.CreateTicket("t", "d", "normal", 0, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if tk.Number <= last {
			t.Fatalf("ticket number %d did not increase past %d", tk.Number, last)
		}
		last = tk.Number
	}

	// Deleting a ticket must not free its number for reuse.
	if _, err := s.DB.Exec(`DELETE FROM tickets WHERE number = 3`); err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket("t", "d", "normal", 0, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Number != 6 {
		t.Fatalf("expected next number 6 after deletion, got %d", tk.Number)
	}
}

func TestAuthenticateAndPassword(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateUser("alice", "Alice", "correct-horse", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate("alice", "correct-horse"); err != nil {
		t.Fatalf("good password rejected: %v", err)
	}
	if _, err := s.Authenticate("alice", "wrong"); err != ErrBadPassword {
		t.Fatalf("bad password: want ErrBadPassword, got %v", err)
	}
	if _, err := s.Authenticate("nobody", "x"); err != ErrBadPassword {
		t.Fatalf("unknown user: want ErrBadPassword, got %v", err)
	}

	u, err := s.GetUserByName("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive(u.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate("alice", "correct-horse"); err != ErrInactiveUser {
		t.Fatalf("inactive user: want ErrInactiveUser, got %v", err)
	}
}

func TestSessionsInvalidateOnPasswordChange(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser("bob", "Bob", "pw12345678", "user")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession(sess.ID); err != nil || got == nil {
		t.Fatalf("session should exist: %v %v", got, err)
	}
	if err := s.SetPassword(u.ID, "new-password-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("session should be deleted after password change")
	}
}

func TestDefaultCategoriesSeeded(t *testing.T) {
	s := newTestStore(t)
	cats, err := s.ListCategories(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) < 6 {
		t.Fatalf("expected seeded categories, got %d", len(cats))
	}
}

func TestDashboardStatsWithEvents(t *testing.T) {
	s := newTestStore(t)
	u, err := s.CreateUser("adm", "Adm", "pw12345678", "admin")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket("hello", "world", "high", 0, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Regression guard: DashboardStats used to run a nested query inside an
	// open rows loop, which deadlocks on the single-connection SQLite pool
	// as soon as any event exists. The whole process then hangs forever.
	done := make(chan *Stats, 1)
	go func() {
		st, err := s.DashboardStats()
		if err != nil {
			t.Errorf("DashboardStats: %v", err)
		}
		done <- st
	}()
	select {
	case st := <-done:
		if st == nil {
			t.Fatal("no stats returned")
		}
		if len(st.Recent) == 0 {
			t.Fatal("expected recent events")
		}
		if st.RecentTicketN[tk.ID] != tk.Number {
			t.Fatalf("event should map to ticket number %d, got %v", tk.Number, st.RecentTicketN)
		}
		if st.Open != 1 || st.HighPriority != 1 {
			t.Fatalf("unexpected counters: %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DashboardStats deadlocked with events present")
	}
}

func TestStatusAssignmentTransitions(t *testing.T) {
	s := newTestStore(t)
	adm, err := s.CreateUser("adm", "Adm", "pw12345678", "admin")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTicket("t", "d", "normal", 0, adm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AssignTicket(tk.ID, adm.ID, adm.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTicketByNumber(tk.Number)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "in_progress" {
		t.Fatalf("assignment should move open -> in_progress, got %s", got.Status)
	}
	if err := s.SetStatus(tk.ID, adm.ID, "closed"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTicketByNumber(tk.Number)
	if got.Status != "closed" || !got.ClosedAt.Valid {
		t.Fatalf("expected closed with timestamp, got %+v", got)
	}
}
