// Package web serves the ticketd HTTP application.
package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ticketd/internal/buildinfo"
	"ticketd/internal/config"
	"ticketd/internal/store"
)

const pageSize = 25

type Server struct {
	cfg      *config.Config
	store    *store.Store
	static   fs.FS
	sets     map[string]*template.Template
	flashes  map[string][]FlashMsg
	flashM   sync.Mutex
	attempts *rateLimiter
}

type FlashMsg struct {
	Kind string // "ok" | "err"
	Text string
}

type ctxKey int

const sessionKey ctxKey = 1

func New(cfg *config.Config, st *store.Store, tmplFS, staticFS fs.FS) (*Server, error) {
	s := &Server{
		cfg:      cfg,
		store:    st,
		static:   staticFS,
		sets:     map[string]*template.Template{},
		flashes:  map[string][]FlashMsg{},
		attempts: newRateLimiter(),
	}
	if err := s.loadTemplates(tmplFS); err != nil {
		return nil, err
	}
	return s, nil
}

// --- templates ---

var pageNames = []string{"login", "dashboard", "list", "detail", "new", "users", "categories", "account", "error"}

var funcMap = template.FuncMap{
	"statusLabel":   statusLabel,
	"statusClass":   func(s string) string { return "pill pill-" + s },
	"priorityLabel": func(p string) string { return strings.ToTitle(p) },
	"priorityClass": func(p string) string { return "prio prio-" + p },
	"initial": func(name string) string {
		r := []rune(name)
		if len(r) == 0 {
			return "?"
		}
		return strings.ToUpper(string(r[0]))
	},
	"shout":     strings.ToUpper,
	"hasPrefix": strings.HasPrefix,
	"hasSuffix": strings.HasSuffix,
	"add":       func(a, b int) int { return a + b },
	"sub":       func(a, b int) int { return a - b },
	"safeHTML":  func(s string) template.HTML { return template.HTML(s) },
	"fmtTime":   fmtTimeHTML,
	"sizeFmt": func(n int64) string {
		switch {
		case n < 1024:
			return fmt.Sprintf("%d B", n)
		case n < 1024*1024:
			return fmt.Sprintf("%.0f KB", float64(n)/1024)
		default:
			return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
		}
	},
	"abbrev": func(s string, n int) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n]) + "…"
	},
}

// fmtTimeHTML renders a machine-readable timestamp; a small inline script
// replaces it with a local relative time in the browser.
func fmtTimeHTML(rfc string) template.HTML {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return template.HTML(template.HTMLEscapeString(rfc))
	}
	label := t.UTC().Format("2006-01-02 15:04") + " UTC"
	return template.HTML(fmt.Sprintf(`<time class="js-time" datetime=%q>%s</time>`, rfc, label))
}

func statusLabel(s string) string {
	switch s {
	case "open":
		return "Open"
	case "in_progress":
		return "In progress"
	case "closed":
		return "Closed"
	}
	return s
}

func (s *Server) loadTemplates(tmplFS fs.FS) error {
	funcs := template.FuncMap{}
	for k, v := range funcMap {
		funcs[k] = v
	}
	prefix := s.cfg.TicketPrefix
	// With no prefix: "#1042". With a prefix: "IT-1042" (the prefix replaces
	// the # sign rather than doubling up).
	funcs["ref"] = func(n int) string {
		if prefix != "" {
			return fmt.Sprintf("%s%d", prefix, n)
		}
		return fmt.Sprintf("#%d", n)
	}

	layout, err := fs.ReadFile(tmplFS, "layout.html")
	if err != nil {
		return err
	}
	partials, err := fs.ReadFile(tmplFS, "partials.html")
	if err != nil {
		return err
	}
	for _, name := range pageNames {
		page, err := fs.ReadFile(tmplFS, name+".html")
		if err != nil {
			return fmt.Errorf("template %s: %w", name, err)
		}
		set := template.New(name).Funcs(funcs)
		if set, err = set.Parse(string(layout)); err != nil {
			return fmt.Errorf("parse layout: %w", err)
		}
		if set, err = set.Parse(string(partials)); err != nil {
			return fmt.Errorf("parse partials: %w", err)
		}
		if set, err = set.Parse(string(page)); err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		s.sets[name] = set
	}
	return nil
}

// Page carries the common fields every template receives.
type Page struct {
	Title   string
	Active  string
	User    *store.User
	CSRF    string
	Prefix  string
	Flashes []FlashMsg
	IsAdmin bool
	// Version renders as " v…" in the footer; empty for dev builds.
	Version string
}

type viewData struct {
	P Page
	D any
}

func (s *Server) pageFor(r *http.Request, active string) Page {
	p := Page{Prefix: s.cfg.TicketPrefix, Active: active}
	if buildinfo.Version != "dev" {
		p.Version = buildinfo.Version
	}
	if sess := sessionFrom(r.Context()); sess != nil {
		p.User = sess.User
		p.CSRF = sess.CSRF
		p.IsAdmin = sess.User.IsAdmin()
		p.Flashes = s.popFlashes(sess.ID)
	}
	return p
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	s.renderWith(w, r, page, page, data)
}

func (s *Server) renderWith(w http.ResponseWriter, r *http.Request, page, active string, data any) {
	set, ok := s.sets[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := set.ExecuteTemplate(w, "layout", viewData{P: s.pageFor(r, active), D: data}); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	w.WriteHeader(code)
	s.render(w, r, "error", map[string]any{"Code": code, "Message": msg})
}

// --- flash messages (per session, in memory) ---

func (s *Server) pushFlash(r *http.Request, kind, text string) {
	sess := sessionFrom(r.Context())
	if sess == nil {
		return
	}
	s.flashM.Lock()
	defer s.flashM.Unlock()
	s.flashes[sess.ID] = append(s.flashes[sess.ID], FlashMsg{Kind: kind, Text: text})
}

func (s *Server) popFlashes(sessionID string) []FlashMsg {
	s.flashM.Lock()
	defer s.flashM.Unlock()
	f := s.flashes[sessionID]
	delete(s.flashes, sessionID)
	return f
}

func (s *Server) redirectFlash(w http.ResponseWriter, r *http.Request, url, kind, msg string) {
	if msg != "" {
		s.pushFlash(r, kind, msg)
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// --- middleware ---

func (s *Server) sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie("ticketd_session")
		if err == nil && ck.Value != "" {
			sess, err := s.store.GetSession(ck.Value)
			if err != nil {
				log.Printf("session lookup: %v", err)
			}
			if sess != nil {
				r = r.WithContext(context.WithValue(r.Context(), sessionKey, sess))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sessionFrom(ctx context.Context) *store.Session {
	sess, _ := ctx.Value(sessionKey).(*store.Session)
	return sess
}

func (s *Server) requireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sessionFrom(r.Context()) == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !sess.User.IsAdmin() {
			s.renderError(w, r, http.StatusForbidden, "Administrators only.")
			return
		}
		next(w, r)
	}
}

var csrfSkip = map[string]bool{"/login": true}

func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !csrfSkip[r.URL.Path] &&
			!strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			sess := sessionFrom(r.Context())
			_ = r.ParseForm()
			if sess == nil || r.FormValue("csrf") != sess.CSRF {
				s.renderError(w, r, http.StatusForbidden, "The form has expired. Please try again.")
				return
			}
		}
		// Multipart POSTs are validated with checkMultipartCSRF in the handler,
		// because their body is streamed by parsePostForm.
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// --- routes ---

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()

	m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(s.static)))

	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r.Context())
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if sess.User.IsAdmin() {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/my-tickets", http.StatusSeeOther)
		}
	})
	m.HandleFunc("GET /login", s.handleLoginPage)
	m.HandleFunc("POST /login", s.handleLogin)
	m.HandleFunc("POST /logout", s.handleLogout)

	m.HandleFunc("GET /dashboard", s.requireAdmin(s.handleDashboard))
	m.HandleFunc("GET /tickets", s.requireAdmin(s.handleTicketList))
	m.HandleFunc("GET /my-tickets", s.requireLogin(s.handleMyTickets))
	m.HandleFunc("GET /tickets/new", s.requireLogin(s.handleNewTicket))
	m.HandleFunc("POST /tickets", s.requireLogin(s.handleCreateTicket))
	m.HandleFunc("GET /tickets/{number}", s.requireLogin(s.handleTicketDetail))
	m.HandleFunc("POST /tickets/{number}/reply", s.requireLogin(s.handleReply))
	m.HandleFunc("POST /tickets/{number}/assign", s.requireAdmin(s.handleAssign))
	m.HandleFunc("POST /tickets/{number}/status", s.requireAdmin(s.handleStatus))
	m.HandleFunc("POST /tickets/{number}/priority", s.requireAdmin(s.handlePriority))
	m.HandleFunc("POST /tickets/{number}/category", s.requireAdmin(s.handleCategory))
	m.HandleFunc("POST /tickets/{number}/reopen", s.requireAdmin(s.handleReopen))
	m.HandleFunc("POST /tickets/{number}/dismiss-reopen", s.requireAdmin(s.handleDismissReopen))
	m.HandleFunc("GET /attachments/{id}", s.requireLogin(s.serveAttachment))

	m.HandleFunc("GET /admin/users", s.requireAdmin(s.handleUsersAdmin))
	m.HandleFunc("POST /admin/users/new", s.requireAdmin(s.handleUserCreate))
	m.HandleFunc("POST /admin/users/{id}/role", s.requireAdmin(s.handleUserRole))
	m.HandleFunc("POST /admin/users/{id}/active", s.requireAdmin(s.handleUserActive))
	m.HandleFunc("POST /admin/users/{id}/password", s.requireAdmin(s.handleUserPassword))
	m.HandleFunc("GET /admin/categories", s.requireAdmin(s.handleCategoriesAdmin))
	m.HandleFunc("POST /admin/categories/new", s.requireAdmin(s.handleCategoryCreate))
	m.HandleFunc("POST /admin/categories/{id}", s.requireAdmin(s.handleCategoryUpdate))

	m.HandleFunc("GET /account", s.requireLogin(s.handleAccount))
	m.HandleFunc("POST /account/password", s.requireLogin(s.handleSelfPassword))

	var h http.Handler = m
	h = s.csrfMiddleware(h)
	h = s.sessionMiddleware(h)
	h = securityHeaders(h)
	return h
}

// --- helpers used by handlers ---

var numberRe = regexp.MustCompile(`^\d+$`)

func (s *Server) ticketFromPath(w http.ResponseWriter, r *http.Request) (*store.Ticket, bool) {
	raw := r.PathValue("number")
	if !numberRe.MatchString(raw) {
		s.renderError(w, r, http.StatusNotFound, "No such ticket.")
		return nil, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "No such ticket.")
		return nil, false
	}
	t, err := s.store.GetTicketByNumber(n)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, r, http.StatusNotFound, "No such ticket.")
		return nil, false
	}
	if err != nil {
		log.Printf("ticket lookup: %v", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return nil, false
	}
	return t, true
}

// canView enforces: admins see everything; users only their own tickets.
func canView(sess *store.Session, t *store.Ticket) bool {
	return sess.User.IsAdmin() || t.CreatedBy == sess.UserID
}

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n
}

// listFilters parses the query parameters shared by admin and user list views.
// Callers set OnlyMine themselves when restricting to a single creator.
func (s *Server) listFilters(r *http.Request) store.TicketQuery {
	q := r.URL.Query()
	tq := store.TicketQuery{Q: strings.TrimSpace(q.Get("q"))}
	switch v := q.Get("status"); v {
	case "open", "in_progress", "closed", "reopen":
		tq.Status = v
	}
	switch v := q.Get("priority"); v {
	case "low", "normal", "high", "urgent":
		tq.Priority = v
	}
	switch v := q.Get("sort"); v {
	case "created", "priority":
		tq.Sort = v
	default:
		tq.Sort = "updated"
	}
	tq.CategoryID, _ = strconv.ParseInt(q.Get("category"), 10, 64)
	switch v := q.Get("assignee"); v {
	case "none":
		tq.AssigneeID = -1
	case "", "any":
		// no assignee filter
	default:
		tq.AssigneeID, _ = strconv.ParseInt(v, 10, 64)
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 1000 {
		page = 1000
	}
	tq.Offset = (page - 1) * pageSize
	return tq
}

// rateLimiter limits failed logins per key (ip + username).
type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{hits: map[string][]time.Time{}} }

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-15 * time.Minute)
	kept := []time.Time{}
	for _, t := range rl.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 10 {
		rl.hits[key] = kept
		return false
	}
	rl.hits[key] = kept
	return true
}

func (rl *rateLimiter) fail(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.hits[key] = append(rl.hits[key], time.Now())
}
