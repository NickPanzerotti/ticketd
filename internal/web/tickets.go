package web

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"ticketd/internal/notify"
	"ticketd/internal/store"
)

func (s *Server) ticketURL(t *store.Ticket) string { return fmt.Sprintf("/tickets/%d", t.Number) }

// --- dashboard ---

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.DashboardStats()
	if err != nil {
		log.Printf("dashboard: %v", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	s.render(w, r, "dashboard", map[string]any{"Stats": stats})
}

// --- lists ---

type listData struct {
	Tickets   []store.Ticket
	Total     int
	Page      int
	Pages     int
	Values    url.Values // form control values for the filter bar
	QueryBase string     // "?k=v&k=v&" for pagination links (keeps status)
	TabBase   string     // like QueryBase but without status, for the tabs
	Admin     bool
	Admins    []store.User
	Cats      []store.Category
}

func (s *Server) handleTicketList(w http.ResponseWriter, r *http.Request) {
	s.listPage(w, r, true, 0)
}

func (s *Server) handleMyTickets(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	s.listPage(w, r, sess.User.IsAdmin(), sess.UserID)
}

func (s *Server) listPage(w http.ResponseWriter, r *http.Request, forAdmin bool, onlyMine int64) {
	tq := s.listFilters(r)
	tq.OnlyMine = onlyMine
	tickets, total, err := s.store.ListTickets(tq)
	if err != nil {
		log.Printf("list: %v", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	page := tq.Offset/pageSize + 1
	pages := (total + pageSize - 1) / pageSize

	qb := url.Values{}
	for _, k := range []string{"q", "status", "priority", "category", "assignee", "sort"} {
		if v := r.URL.Query().Get(k); v != "" {
			qb.Set(k, v)
		}
	}
	base := "?"
	if enc := qb.Encode(); enc != "" {
		base += enc + "&"
	}
	qbTab := url.Values{}
	for _, k := range []string{"q", "priority", "category", "sort"} {
		if v := r.URL.Query().Get(k); v != "" {
			qbTab.Set(k, v)
		}
	}
	tabBase := "?"
	if enc := qbTab.Encode(); enc != "" {
		tabBase += enc + "&"
	}

	data := listData{
		Tickets:   tickets,
		Total:     total,
		Page:      page,
		Pages:     pages,
		Values:    r.URL.Query(),
		QueryBase: base,
		TabBase:   tabBase,
		Admin:     forAdmin && onlyMine == 0,
	}
	if data.Admin {
		data.Admins, _ = s.store.ListActiveAdmins()
	}
	data.Cats, _ = s.store.ListCategories(false)

	active := "my-tickets"
	if data.Admin {
		active = "tickets"
	}
	if r.Header.Get("HX-Request") == "true" {
		// htmx refresh: return only the swappable list fragment.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.sets["list"].ExecuteTemplate(w, "list-part", viewData{P: s.pageFor(r, active), D: data}); err != nil {
			log.Printf("render list-part: %v", err)
		}
		return
	}
	s.renderWith(w, r, "list", active, data)
}

// --- create ---

func (s *Server) handleNewTicket(w http.ResponseWriter, r *http.Request) {
	cats, _ := s.store.ListCategories(false)
	s.render(w, r, "new", map[string]any{"Cats": cats, "Values": map[string]string{}, "MaxMB": s.cfg.MaxUploadMB})
}

func (s *Server) handleCreateTicket(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	vals, files, err := s.parsePostForm(w, r)
	if err != nil {
		s.pushFlash(r, "err", "Upload failed: file too large or request malformed.")
		http.Redirect(w, r, "/tickets/new", http.StatusSeeOther)
		return
	}
	if !s.checkMultipartCSRF(r, vals) {
		s.renderError(w, r, http.StatusForbidden, "The form has expired. Please try again.")
		return
	}
	title := strings.TrimSpace(vals.Get("title"))
	desc := strings.TrimSpace(vals.Get("description"))
	catID, _ := strconv.ParseInt(vals.Get("category"), 10, 64)
	prio := vals.Get("priority")
	if prio == "" {
		prio = "normal"
	}

	if title == "" {
		cats, _ := s.store.ListCategories(false)
		s.render(w, r, "new", map[string]any{
			"Cats":   cats,
			"Error":  "A short title is required.",
			"Values": singleVals(vals, "title", "description", "priority"),
			"MaxMB":  s.cfg.MaxUploadMB,
		})
		return
	}

	t, err := s.store.CreateTicket(title, desc, prio, catID, sess.UserID)
	if err != nil {
		log.Printf("create ticket: %v", err)
		cats, _ := s.store.ListCategories(false)
		s.render(w, r, "new", map[string]any{
			"Cats":   cats,
			"Error":  "Could not create the ticket.",
			"Values": singleVals(vals, "title", "description", "priority"),
			"MaxMB":  s.cfg.MaxUploadMB,
		})
		return
	}
	for _, f := range files {
		fh, oerr := os.Open(f.TempPath)
		if oerr != nil {
			os.Remove(f.TempPath)
			continue
		}
		_, aerr := s.store.SaveAttachment(t.ID, sess.UserID, 0, f.Filename, fh, s.cfg.MaxUploadMB*1024*1024)
		fh.Close()
		os.Remove(f.TempPath)
		if aerr != nil {
			s.pushFlash(r, "err", fmt.Sprintf("Could not attach %s: %v", f.Filename, aerr))
		}
	}
	http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
}

func singleVals(vals url.Values, keys ...string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		out[k] = vals.Get(k)
	}
	return out
}

// --- detail ---

type threadItem struct {
	At    string
	Kind  string // "post" | "event"
	Post  *store.Post
	Event *store.TicketEvent
}

type detailData struct {
	T      *store.Ticket
	Items  []threadItem
	Att    map[int64][]store.Attachment
	Admins []store.User
	Cats   []store.Category
	MaxMB  int64
}

func (s *Server) handleTicketDetail(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	t, ok := s.ticketFromPath(w, r)
	if !ok {
		return
	}
	if !canView(sess, t) {
		s.renderError(w, r, http.StatusForbidden, "You can only view your own tickets.")
		return
	}
	posts, events, err := s.store.Thread(t.ID, sess.User.IsAdmin())
	if err != nil {
		log.Printf("thread: %v", err)
	}
	var items []threadItem
	for i := range posts {
		items = append(items, threadItem{At: posts[i].CreatedAt, Kind: "post", Post: &posts[i]})
	}
	for i := range events {
		items = append(items, threadItem{At: events[i].CreatedAt, Kind: "event", Event: &events[i]})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].At != items[j].At {
			return items[i].At < items[j].At
		}
		return items[i].Kind < items[j].Kind
	})
	att, err := s.store.ListAttachments(t.ID)
	if err != nil {
		log.Printf("attachments: %v", err)
	}
	data := detailData{T: t, Items: items, Att: att, MaxMB: s.cfg.MaxUploadMB}
	if sess.User.IsAdmin() {
		data.Admins, _ = s.store.ListActiveAdmins()
	}
	data.Cats, _ = s.store.ListCategories(false)
	s.render(w, r, "detail", data)
}

// --- reply ---

// notifyReply sends reply notifications in the background so a slow or
// unreachable SMTP server can never block or fail the reply request.
func (s *Server) notifyReply(t *store.Ticket, authorID int64, authorName, body string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("notify panic: %v", r)
			}
		}()
		cfg, err := s.store.GetNotifySettings()
		if err != nil {
			log.Printf("notify settings: %v", err)
			return
		}
		if err := notify.Reply(s.store, s.mailer, cfg, s.cfg.TicketPrefix, t, authorID, authorName, body); err != nil {
			log.Printf("email notification: %v", err)
		}
	}()
}

func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	t, ok := s.ticketFromPath(w, r)
	if !ok {
		return
	}
	if !canView(sess, t) {
		s.renderError(w, r, http.StatusForbidden, "You can only reply on your own tickets.")
		return
	}
	vals, files, err := s.parsePostForm(w, r)
	if err != nil {
		s.pushFlash(r, "err", "Reply failed: upload too large or request malformed.")
		http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
		return
	}
	if !s.checkMultipartCSRF(r, vals) {
		s.renderError(w, r, http.StatusForbidden, "The form has expired. Please try again.")
		return
	}
	body := strings.TrimSpace(vals.Get("body"))
	internal := vals.Get("internal") == "1" && sess.User.IsAdmin()
	if body == "" && len(files) == 0 {
		s.pushFlash(r, "err", "Write a reply or attach a file first.")
		http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
		return
	}
	post, err := s.store.AddPost(t.ID, sess.UserID, body, internal)
	if err != nil {
		log.Printf("add post: %v", err)
		s.pushFlash(r, "err", "Could not save the reply.")
		http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
		return
	}
	if t.Status == "closed" && !sess.User.IsAdmin() {
		if err := s.store.MarkReopenRequested(t.ID, sess.UserID); err != nil {
			log.Printf("reopen request: %v", err)
		}
	}
	if msg := s.saveUploads(t.ID, sess.UserID, post.ID, files); msg != "" {
		s.pushFlash(r, "err", msg)
	}
	// Internal notes are admin-only and never send emails.
	if !internal {
		s.notifyReply(t, sess.UserID, sess.User.DisplayName, body)
	}
	http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
}

// --- admin actions ---

func (s *Server) handleAssign(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	t, ok := s.ticketFromPath(w, r)
	if !ok {
		return
	}
	assignee, _ := strconv.ParseInt(r.FormValue("assignee"), 10, 64)
	if err := s.store.AssignTicket(t.ID, sess.UserID, assignee); err != nil {
		log.Printf("assign: %v", err)
		s.pushFlash(r, "err", "Could not change the assignment.")
	}
	http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	t, ok := s.ticketFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.SetStatus(t.ID, sess.UserID, r.FormValue("status")); err != nil {
		log.Printf("status: %v", err)
		s.pushFlash(r, "err", "Could not change the status.")
	} else {
		s.pushFlash(r, "ok", "Status updated.")
	}
	http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
}

func (s *Server) handleReopen(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	t, ok := s.ticketFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.SetStatus(t.ID, sess.UserID, "open"); err != nil {
		s.pushFlash(r, "err", "Could not reopen the ticket.")
	}
	http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
}

func (s *Server) handleDismissReopen(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	t, ok := s.ticketFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.DismissReopenRequest(t.ID, sess.UserID); err != nil {
		s.pushFlash(r, "err", "Could not dismiss the reopen request.")
	}
	http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
}

func (s *Server) handlePriority(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	t, ok := s.ticketFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.SetPriority(t.ID, sess.UserID, r.FormValue("priority")); err != nil {
		s.pushFlash(r, "err", "Could not change the priority.")
	}
	http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
}

func (s *Server) handleCategory(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	t, ok := s.ticketFromPath(w, r)
	if !ok {
		return
	}
	catID, _ := strconv.ParseInt(r.FormValue("category"), 10, 64)
	if err := s.store.SetCategory(t.ID, sess.UserID, catID); err != nil {
		log.Printf("category: %v", err)
		s.pushFlash(r, "err", "Could not change the category.")
	}
	http.Redirect(w, r, s.ticketURL(t), http.StatusSeeOther)
}

// --- attachments serving ---

func contentDisposition(att *store.Attachment) string {
	disposition := "attachment"
	if att.Inline() {
		disposition = "inline"
	}
	return fmt.Sprintf(`%s; filename=%q`, disposition, att.Filename)
}

func (s *Server) serveAttachment(w http.ResponseWriter, r *http.Request) {
	att, err := s.store.GetAttachment(idParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, r, http.StatusNotFound, "No such attachment.")
		return
	}
	if err != nil {
		log.Printf("attachment lookup: %v", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	sess := sessionFrom(r.Context())
	if !sess.User.IsAdmin() {
		owner, err := s.store.TicketOwner(att.TicketID)
		if err != nil || owner != sess.UserID {
			s.renderError(w, r, http.StatusForbidden, "You cannot view this attachment.")
			return
		}
	}
	f, err := os.Open(att.Path(s.store.UploadDir))
	if err != nil {
		log.Printf("open attachment: %v", err)
		s.renderError(w, r, http.StatusNotFound, "File missing from storage.")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", att.ContentType)
	w.Header().Set("Content-Disposition", contentDisposition(att))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, att.Filename, time.Time{}, f)
}
