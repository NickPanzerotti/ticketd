package web

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"ticketd/internal/notify"
	"ticketd/internal/store"
)

// --- user administration ---

func (s *Server) handleUsersAdmin(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		log.Printf("list users: %v", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	s.renderWith(w, r, "users", "admin-users", map[string]any{"Users": users})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	username := strings.TrimSpace(r.FormValue("username"))
	display := strings.TrimSpace(r.FormValue("display_name"))
	password := r.FormValue("password")
	role := r.FormValue("role")
	if role != "admin" {
		role = "user"
	}
	if display == "" {
		display = username
	}
	if username == "" || password == "" {
		s.pushFlash(r, "err", "Username and password are required.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	if len(password) < 8 {
		s.pushFlash(r, "err", "Password must be at least 8 characters.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	u, err := s.store.CreateUser(username, display, password, role)
	if err != nil {
		switch err {
		case store.ErrUserExists:
			s.pushFlash(r, "err", "That username is already taken.")
		default:
			log.Printf("create user: %v", err)
			s.pushFlash(r, "err", "Could not create the account.")
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	if email := strings.TrimSpace(r.FormValue("email")); email != "" {
		if !store.ValidEmail(email) {
			s.pushFlash(r, "err", "Account created, but that email address looks invalid — add it again if needed.")
		} else if err := s.store.SetEmail(u.ID, email); err != nil {
			log.Printf("set email on create: %v", err)
		}
	}
	s.pushFlash(r, "ok", "Account created for "+username+".")
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) handleUserRole(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	id := idParam(r, "id")
	if id == sess.UserID {
		s.pushFlash(r, "err", "You cannot change your own role.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	if err := s.store.SetRole(id, r.FormValue("role")); err != nil {
		s.pushFlash(r, "err", "Could not change the role.")
	} else {
		s.pushFlash(r, "ok", "Role updated.")
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) handleUserActive(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	id := idParam(r, "id")
	if id == sess.UserID {
		s.pushFlash(r, "err", "You cannot disable your own account.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	active := r.FormValue("active") == "1"
	if err := s.store.SetActive(id, active); err != nil {
		s.pushFlash(r, "err", "Could not update the account.")
	} else if active {
		s.pushFlash(r, "ok", "Account enabled.")
	} else {
		s.pushFlash(r, "ok", "Account disabled.")
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	id := idParam(r, "id")
	pw := r.FormValue("password")
	if len(pw) < 8 {
		s.pushFlash(r, "err", "Password must be at least 8 characters.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	if err := s.store.SetPassword(id, pw); err != nil {
		log.Printf("set password: %v", err)
		s.pushFlash(r, "err", "Could not set the password.")
	} else {
		s.pushFlash(r, "ok", "Password set. The user must sign in again.")
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (s *Server) handleUserEmail(w http.ResponseWriter, r *http.Request) {
	id := idParam(r, "id")
	email := strings.TrimSpace(r.FormValue("email"))
	if email != "" && !store.ValidEmail(email) {
		s.pushFlash(r, "err", "That does not look like an email address.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	if err := s.store.SetEmail(id, email); err != nil {
		log.Printf("set email: %v", err)
		s.pushFlash(r, "err", "Could not set the email address.")
	} else if email == "" {
		s.pushFlash(r, "ok", "Email address removed.")
	} else {
		s.pushFlash(r, "ok", "Email address set.")
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// --- category administration ---

func (s *Server) handleCategoriesAdmin(w http.ResponseWriter, r *http.Request) {
	cats, err := s.store.ListCategories(true)
	if err != nil {
		log.Printf("list categories: %v", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	s.renderWith(w, r, "categories", "admin-categories", map[string]any{"Cats": cats})
}

func (s *Server) handleCategoryCreate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	color := normalizeColor(r.FormValue("color"))
	if name == "" {
		s.pushFlash(r, "err", "A category name is required.")
		http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
		return
	}
	if err := s.store.CreateCategory(name, color); err != nil {
		if errors.Is(err, store.ErrUserExists) {
			s.pushFlash(r, "err", "A category with that name already exists.")
		} else {
			log.Printf("create category: %v", err)
			s.pushFlash(r, "err", "Could not create the category.")
		}
		http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
		return
	}
	s.pushFlash(r, "ok", "Category created.")
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

func (s *Server) handleCategoryUpdate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id := idParam(r, "id")
	name := strings.TrimSpace(r.FormValue("name"))
	color := normalizeColor(r.FormValue("color"))
	active := r.FormValue("active") == "1"
	if err := s.store.UpdateCategory(id, name, color, active); err != nil {
		switch {
		case errors.Is(err, store.ErrUserExists):
			s.pushFlash(r, "err", "A category with that name already exists.")
		case errors.Is(err, store.ErrNotFound):
			s.pushFlash(r, "err", "No such category.")
		default:
			s.pushFlash(r, "err", "Could not update the category.")
		}
	} else {
		s.pushFlash(r, "ok", "Category updated.")
	}
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

func normalizeColor(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if !strings.HasPrefix(c, "#") || (len(c) != 4 && len(c) != 7) {
		return "#64748b"
	}
	for _, r := range c[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "#64748b"
		}
	}
	return c
}

// --- own account ---

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	s.renderWith(w, r, "account", "account", map[string]any{})
}

func (s *Server) handleSelfPassword(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	_ = r.ParseForm()
	current := r.FormValue("current")
	next := r.FormValue("password")
	if _, err := s.store.Authenticate(sess.User.Username, current); err != nil {
		s.pushFlash(r, "err", "Current password is incorrect.")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	if len(next) < 8 {
		s.pushFlash(r, "err", "New password must be at least 8 characters.")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	if err := s.store.SetPassword(sess.UserID, next); err != nil {
		s.pushFlash(r, "err", "Could not change the password.")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	// SetPassword deleted our own session; sign in again.
	http.SetCookie(w, &http.Cookie{Name: "ticketd_session", Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login?e=pw", http.StatusSeeOther)
}

func (s *Server) handleSelfEmail(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	_ = r.ParseForm()
	email := strings.TrimSpace(r.FormValue("email"))
	if email != "" && !store.ValidEmail(email) {
		s.pushFlash(r, "err", "That does not look like an email address.")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	if err := s.store.SetEmail(sess.UserID, email); err != nil {
		s.pushFlash(r, "err", "Could not save the email address.")
	} else {
		sess.User.Email = email // reflect immediately without a re-login
		s.pushFlash(r, "ok", "Email address saved.")
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// --- notification settings ---

func settingsFromForm(r *http.Request) store.NotifySettings {
	_ = r.ParseForm()
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("smtp_port")))
	scope := store.NotifyScopeParticipants
	if r.FormValue("notify_scope") == store.NotifyScopeCreator {
		scope = store.NotifyScopeCreator
	}
	return store.NotifySettings{
		Enabled:      r.FormValue("enabled") == "1",
		SMTPHost:     strings.TrimSpace(r.FormValue("smtp_host")),
		SMTPPort:     port,
		SMTPUser:     strings.TrimSpace(r.FormValue("smtp_user")),
		SMTPPassword: r.FormValue("smtp_pass"), // empty means "keep stored"
		SMTPFrom:     strings.TrimSpace(r.FormValue("smtp_from")),
		PublicURL:    strings.TrimSpace(r.FormValue("public_url")),
		NotifyScope:  scope,
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ns, err := s.store.GetNotifySettings()
	if err != nil {
		log.Printf("settings: %v", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	s.renderWith(w, r, "settings", "admin-settings", map[string]any{"S": ns})
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	ns := settingsFromForm(r)
	if ns.Enabled {
		if ns.SMTPHost == "" {
			s.pushFlash(r, "err", "Notifications need at least an SMTP server address.")
			http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
			return
		}
		if ns.SMTPFrom == "" && ns.SMTPUser == "" {
			s.pushFlash(r, "err", "Notifications need a From address (or an SMTP username to use as one).")
			http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
			return
		}
	}
	if err := s.store.SetNotifySettings(ns); err != nil {
		log.Printf("save settings: %v", err)
		s.pushFlash(r, "err", "Could not save the settings.")
	} else {
		s.pushFlash(r, "ok", "Settings saved.")
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// handleSettingsTest sends one email immediately so the result can be shown
// as a flash message instead of only appearing in the log.
func (s *Server) handleSettingsTest(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	to := strings.TrimSpace(r.FormValue("test_to"))
	if !store.ValidEmail(to) {
		s.pushFlash(r, "err", "Enter a valid address to send the test email to.")
		http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
		return
	}
	ns, err := s.store.GetNotifySettings()
	if err != nil || ns.SMTPHost == "" {
		s.pushFlash(r, "err", "Configure and save the SMTP settings first.")
		http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
		return
	}
	err = s.mailer.Send(ns, notify.Message{
		To:      []string{to},
		Subject: "ticketd test notification",
		Body:    "If you received this, ticketd's email settings are working.",
	})
	if err != nil {
		log.Printf("test email: %v", err)
		s.pushFlash(r, "err", "The test email failed: "+err.Error())
	} else {
		s.pushFlash(r, "ok", "Test email sent to "+to+".")
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}
