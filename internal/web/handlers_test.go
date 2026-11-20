package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"ticketd/internal/config"
	"ticketd/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	srv, st, _ := newTestApp(t)
	return srv, st
}

// newTestApp additionally returns the *Server so tests can swap s.mailer.
func newTestApp(t *testing.T) (*httptest.Server, *store.Store, *Server) {
	t.Helper()
	cfg := &config.Config{
		BindHost: "127.0.0.1", BindPort: 0,
		DataDir:     t.TempDir(),
		MaxUploadMB: 10,
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg, st, os.DirFS("../../templates"), os.DirFS("../../static"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(func() { srv.Close(); st.DB.Close() })
	return srv, st, app
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b)
}

func csrfOf(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	code, body := get(t, c, url)
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d", url, code)
	}
	m := regexp.MustCompile(`name="csrf" value="([a-f0-9]{32})"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf on %s", url)
	}
	return m[1]
}

func post(t *testing.T, c *http.Client, url string, values map[string]string) *http.Response {
	t.Helper()
	form := make(map[string][]string, len(values))
	for k, v := range values {
		form[k] = []string{v}
	}
	resp, err := c.PostForm(url, form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func login(t *testing.T, c *http.Client, base, user, pass string) {
	t.Helper()
	resp := post(t, c, base+"/login", map[string]string{"username": user, "password": pass})
	if resp.StatusCode != http.StatusSeeOther || strings.Contains(resp.Header.Get("Location"), "e=") {
		t.Fatalf("login %s failed: %d -> %s", user, resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestEndToEndFlow(t *testing.T) {
	srv, st := newTestServer(t)
	base := srv.URL

	if _, err := st.CreateUser("admin", "Admin", "pw12345678", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("alice", "Alice", "pw12345678", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("carol", "Carol", "pw12345678", "user"); err != nil {
		t.Fatal(err)
	}

	// --- admin session ---
	ac := newClient(t)
	login(t, ac, base, "admin", "pw12345678")
	if code, body := get(t, ac, base+"/dashboard"); code != 200 || !strings.Contains(body, "Dashboard") {
		t.Fatalf("dashboard: %d", code)
	}

	csrf := csrfOf(t, ac, base+"/tickets/new")
	resp, err := ac.PostForm(base+"/tickets", map[string][]string{
		"csrf": {csrf}, "title": {"Printer down"}, "description": {"jammed"}, "priority": {"high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create ticket: %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/tickets/1" {
		t.Fatalf("expected /tickets/1, got %s", loc)
	}
	resp, _ = ac.PostForm(base+"/tickets", map[string][]string{"csrf": {csrf}, "title": {"VPN access"}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/tickets/2" {
		t.Fatalf("expected monotonic /tickets/2, got %s", loc)
	}

	// Regression guard: the dashboard must still respond after events exist
	// (it once deadlocked the whole app on the first ticket).
	if code, body := get(t, ac, base+"/dashboard"); code != 200 || !strings.Contains(body, "Recent activity") {
		t.Fatalf("dashboard with events: %d", code)
	}

	// admin can see both in the queue
	if code, body := get(t, ac, base+"/tickets"); code != 200 ||
		!strings.Contains(body, "Printer down") || !strings.Contains(body, "VPN access") {
		t.Fatalf("queue listing failed: %d", code)
	}

	// search finds only the match
	if _, body := get(t, ac, base+"/tickets?q=Printer"); !strings.Contains(body, "Printer down") ||
		strings.Contains(body, "VPN access") {
		t.Fatal("search filter failed")
	}

	// admin assign + internal note
	csrf = csrfOf(t, ac, base+"/tickets/1")
	if resp := post(t, ac, base+"/tickets/1/assign", map[string]string{"csrf": csrf, "assignee": "1"}); resp.StatusCode != 303 {
		t.Fatalf("assign: %d", resp.StatusCode)
	}
	if code, body := get(t, ac, base+"/tickets/1"); code != 200 && !strings.Contains(body, "Assigned to Admin") {
		t.Fatalf("assign event missing: %d", code)
	}

	// --- alice session ---
	uc := newClient(t)
	login(t, uc, base, "alice", "pw12345678")

	// alice cannot see admin's queue or dashboard or others' tickets
	if code, _ := get(t, uc, base+"/tickets"); code != http.StatusForbidden {
		t.Fatalf("user on /tickets: want 403, got %d", code)
	}
	if code, _ := get(t, uc, base+"/dashboard"); code != http.StatusForbidden {
		t.Fatalf("user on /dashboard: want 403, got %d", code)
	}
	if code, _ := get(t, uc, base+"/tickets/1"); code != http.StatusForbidden {
		t.Fatalf("user on other's ticket: want 403, got %d", code)
	}

	// alice creates her own ticket → number 3
	csrf = csrfOf(t, uc, base+"/tickets/new")
	resp, _ = uc.PostForm(base+"/tickets", map[string][]string{"csrf": {csrf}, "title": {"Monitor flicker"}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/tickets/3" {
		t.Fatalf("expected /tickets/3, got %s", loc)
	}
	if code, body := get(t, uc, base+"/my-tickets"); code != 200 || !strings.Contains(body, "Monitor flicker") {
		t.Fatalf("my-tickets listing: %d", code)
	}
	if _, body := get(t, uc, base+"/my-tickets"); strings.Contains(body, "Printer down") {
		t.Fatal("my-tickets must not list other users' tickets")
	}

	// carol cannot see alice's ticket
	cc := newClient(t)
	login(t, cc, base, "carol", "pw12345678")
	if code, _ := get(t, cc, base+"/tickets/3"); code != http.StatusForbidden {
		t.Fatalf("carol on alice's ticket: want 403, got %d", code)
	}

	// CSRF: wrong token is rejected
	if resp := post(t, cc, base+"/tickets/1/status", map[string]string{"csrf": "deadbeef", "status": "closed"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad CSRF: want 403, got %d", resp.StatusCode)
	}

	// alice's own reply shows publicly (internal flag ignored for non-admins)
	csrf = csrfOf(t, uc, base+"/tickets/3")
	if resp := post(t, uc, base+"/tickets/3/reply", map[string]string{"csrf": csrf, "body": "check the dock", "internal": "1"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("alice reply: %d", resp.StatusCode)
	}
	if _, html := get(t, uc, base+"/tickets/3"); !strings.Contains(html, "check the dock") {
		t.Fatal("alice reply missing from thread")
	}

	// admin internal notes are hidden from regular users
	csrf = csrfOf(t, ac, base+"/tickets/3")
	if resp := post(t, ac, base+"/tickets/3/reply", map[string]string{"csrf": csrf, "body": "secret internal", "internal": "1"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin internal note: %d", resp.StatusCode)
	}
	if _, html := get(t, uc, base+"/tickets/3"); strings.Contains(html, "secret internal") {
		t.Fatal("internal note leaked to a regular user")
	}
	if _, html := get(t, ac, base+"/tickets/3"); !strings.Contains(html, "secret internal") {
		t.Fatal("internal note missing for admin")
	}

	// Queue filter consistency (regression): tabs, dropdowns and dashboard
	// links must all reflect the same filter state.
	if _, body := get(t, ac, base+"/tickets?status=in_progress"); !strings.Contains(body, "Printer down") ||
		strings.Contains(body, "VPN access") || strings.Contains(body, "Monitor flicker") {
		t.Fatal("in_progress filter should list only the in-progress ticket")
	}
	if code, body := get(t, ac, base+"/tickets"); code != 200 ||
		!strings.Contains(body, `>In Progress</a>`) || strings.Contains(body, `>Reopen requests</a>`) {
		t.Fatalf("tab bar should offer In Progress, not Reopen requests: %d", code)
	}
	if _, body := get(t, ac, base+"/tickets?status=in_progress"); !strings.Contains(body, `value="in_progress" selected`) {
		t.Fatal("status dropdown must be pre-selected as In progress for status=in_progress")
	}

	// Unassigned is an assignee filter, not a status: closing an unassigned
	// ticket must still leave it visible under assignee=none.
	csrf = csrfOf(t, ac, base+"/tickets/2")
	if resp := post(t, ac, base+"/tickets/2/status", map[string]string{"csrf": csrf, "status": "closed"}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("close ticket 2: %d", resp.StatusCode)
	}
	if _, body := get(t, ac, base+"/tickets?assignee=none"); !strings.Contains(body, `value="none" selected`) {
		t.Fatal("assignee dropdown must be pre-selected as Unassigned for assignee=none")
	}
	if _, body := get(t, ac, base+"/tickets?assignee=none"); !strings.Contains(body, "VPN access") ||
		!strings.Contains(body, "Monitor flicker") || strings.Contains(body, "Printer down") {
		t.Fatal("unassigned filter must list all unassigned tickets (any status), not assigned ones")
	}
	if _, body := get(t, ac, base+"/tickets?assignee=none"); strings.Contains(body, `value="unassigned"`) {
		t.Fatal("status dropdown must not contain an Unassigned option")
	}
	if _, body := get(t, ac, base+"/tickets?assignee=none"); strings.Contains(body, `value="open" selected`) ||
		strings.Contains(body, `value="closed" selected`) {
		t.Fatal("status dropdown must stay at Any status for the Unassigned tab")
	}
}
