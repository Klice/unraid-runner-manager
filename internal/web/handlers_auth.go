package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Klice/unraid-runner-manager/internal/auth"
	"github.com/Klice/unraid-runner-manager/internal/store"
)

const minPasswordLength = 10

type loginData struct {
	Username string
	Error    string
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if s.sessionUser(r) != nil {
		http.Redirect(w, r, "/runners", http.StatusFound)
		return
	}
	s.render(w, r, "login", "", loginData{})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	username := strings.ToLower(strings.TrimSpace(r.FormValue("username")))
	password := r.FormValue("password")
	key := clientIP(r) + "|" + username
	if blocked, wait := s.limiter.Blocked(key); blocked {
		s.render(w, r, "login", "", loginData{Username: username, Error: "Too many attempts. Try again in " + wait.Round(1e9).String() + "."})
		return
	}
	u, err := s.store.UserByName(r.Context(), username)
	ok := false
	if err == nil && !u.Disabled {
		ok, _ = auth.VerifyPassword(u.PasswordHash, password)
	}
	if !ok {
		s.limiter.Fail(key)
		s.log.Warn("login failed", "username", username, "ip", clientIP(r))
		s.render(w, r, "login", "", loginData{Username: username, Error: "Wrong username or password."})
		return
	}
	s.limiter.Reset(key)
	token, err := auth.RandomToken(32)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.CreateSession(r.Context(), u.ID, token, s.cfg.SessionTTL); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.store.TouchLogin(r.Context(), u.ID)
	s.setSession(w, token)
	if u.MustChangePassword {
		http.Redirect(w, r, "/account/password", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/runners", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	s.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type passwordData struct {
	Forced bool
	Error  string
}

func (s *Server) passwordForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "password", "", passwordData{Forced: currentUser(r).MustChangePassword})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	current := r.FormValue("current")
	next := r.FormValue("password")
	confirm := r.FormValue("confirm")
	respond := func(msg string) {
		s.render(w, r, "password", "", passwordData{Forced: u.MustChangePassword, Error: msg})
	}
	if ok, _ := auth.VerifyPassword(u.PasswordHash, current); !ok {
		respond("Current password is wrong.")
		return
	}
	if msg := passwordProblem(next); msg != "" {
		respond(msg)
		return
	}
	if next != confirm {
		respond("The two new passwords do not match.")
		return
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.SetPassword(r.Context(), u.ID, hash, false); err != nil {
		s.fail(w, r, err)
		return
	}
	s.setFlash(w, "ok", "Password changed.")
	http.Redirect(w, r, "/runners", http.StatusSeeOther)
}

func passwordProblem(p string) string {
	if utf8.RuneCountInString(p) < minPasswordLength {
		return "New password must be at least 10 characters."
	}
	if len(p) > 256 {
		return "New password is too long."
	}
	return ""
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) EnsureAdmin(ctx context.Context) error {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if s.cfg.AdminPassword == "" {
		return errors.New("no users exist yet: set ADMIN_PASSWORD to create the first admin account")
	}
	if msg := passwordProblem(s.cfg.AdminPassword); msg != "" {
		return errors.New("ADMIN_PASSWORD: " + msg)
	}
	hash, err := auth.HashPassword(s.cfg.AdminPassword)
	if err != nil {
		return err
	}
	if _, err := s.store.CreateUser(ctx, s.cfg.AdminUsername, hash, store.RoleAdmin, false); err != nil {
		return fmt.Errorf("create admin %q: %w", s.cfg.AdminUsername, err)
	}
	s.log.Info("created initial admin user", "username", s.cfg.AdminUsername)
	return nil
}
