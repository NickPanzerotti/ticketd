package web

import (
	"log"
	"net"
	"net/http"

	"ticketd/internal/store"
)

func (s *Server) homePath(sess *store.Session) string {
	if sess.User.IsAdmin() {
		return "/dashboard"
	}
	return "/my-tickets"
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if sessionFrom(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	msg := ""
	switch r.URL.Query().Get("e") {
	case "bad":
		msg = "Incorrect username or password."
	case "inactive":
		msg = "This account has been disabled."
	case "slow":
		msg = "Too many attempts. Please wait a few minutes."
	case "pw":
		msg = "Password changed. Please sign in again."
	}
	s.render(w, r, "login", map[string]any{"Error": msg})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")

	host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		host = r.RemoteAddr
	}
	key := host + "|" + username
	if !s.attempts.allow(key) {
		http.Redirect(w, r, "/login?e=slow", http.StatusSeeOther)
		return
	}

	user, err := s.store.Authenticate(username, password)
	if err == store.ErrBadPassword {
		s.attempts.fail(key)
		http.Redirect(w, r, "/login?e=bad", http.StatusSeeOther)
		return
	}
	if err == store.ErrInactiveUser {
		http.Redirect(w, r, "/login?e=inactive", http.StatusSeeOther)
		return
	}
	if err != nil {
		log.Printf("login error: %v", err)
		http.Redirect(w, r, "/login?e=bad", http.StatusSeeOther)
		return
	}

	sess, err := s.store.CreateSession(user.ID)
	if err != nil {
		log.Printf("create session: %v", err)
		s.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "ticketd_session",
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.homePath(sess), http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sess := sessionFrom(r.Context()); sess != nil {
		if err := s.store.DeleteSession(sess.ID); err != nil {
			log.Printf("logout: %v", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "ticketd_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
