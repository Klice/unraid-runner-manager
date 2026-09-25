package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/auth"
	"github.com/Klice/unraid-runner-manager/internal/config"
	"github.com/Klice/unraid-runner-manager/internal/runner"
	"github.com/Klice/unraid-runner-manager/internal/store"
)

const (
	sessionCookie = "rm_session"
	flashCookie   = "rm_flash"
)

type Server struct {
	cfg     config.Config
	store   *store.Store
	runners *runner.Service
	limiter *auth.Limiter
	views   *views
	log     *slog.Logger
	mux     *http.ServeMux
}

func New(cfg config.Config, st *store.Store, runners *runner.Service, logger *slog.Logger) (*Server, error) {
	v, err := loadViews()
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		cfg:     cfg,
		store:   st,
		runners: runners,
		limiter: auth.NewLimiter(5, 10*time.Minute, time.Minute),
		views:   v,
		log:     logger,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /login", s.loginForm)
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("POST /logout", s.logout)

	s.mux.HandleFunc("GET /{$}", s.user(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/runners", http.StatusFound)
	}))
	s.mux.HandleFunc("GET /runners", s.user(s.runnersPage))
	s.mux.HandleFunc("GET /runners/table", s.user(s.runnersTable))
	s.mux.HandleFunc("GET /runners/new", s.user(s.newRunnerForm))
	s.mux.HandleFunc("POST /runners", s.user(s.createRunner))
	s.mux.HandleFunc("GET /runners/{name}", s.user(s.runnerPage))
	s.mux.HandleFunc("GET /runners/{name}/logs", s.user(s.runnerLogs))
	s.mux.HandleFunc("GET /runners/{name}/delete", s.user(s.deleteRunnerConfirm))
	s.mux.HandleFunc("POST /runners/{name}/delete", s.user(s.deleteRunner))
	s.mux.HandleFunc("POST /runners/{name}/{action}", s.user(s.runnerAction))

	s.mux.HandleFunc("GET /users", s.admin(s.usersPage))
	s.mux.HandleFunc("POST /users", s.admin(s.createUser))
	s.mux.HandleFunc("POST /users/{id}/{action}", s.admin(s.userAction))

	s.mux.HandleFunc("GET /account/password", s.user(s.passwordForm))
	s.mux.HandleFunc("POST /account/password", s.user(s.changePassword))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
		http.Error(w, "cross-site request rejected", http.StatusForbidden)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	s.mux.ServeHTTP(w, r)
}

func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

type ctxKey struct{}

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxKey{}).(*store.User)
	return u
}

func (s *Server) user(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.sessionUser(r)
		if u == nil {
			s.redirect(w, r, "/login")
			return
		}
		if u.MustChangePassword && !strings.HasPrefix(r.URL.Path, "/account/password") {
			s.redirect(w, r, "/account/password")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return s.user(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin() {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	})
}

func (s *Server) sessionUser(r *http.Request) *store.User {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	u, err := s.store.UserBySession(r.Context(), c.Value)
	if err != nil || u.Disabled {
		return nil
	}
	return &u
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.cfg.SessionTTL / time.Second),
	})
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

type flash struct {
	Kind    string
	Message string
}

func (s *Server) setFlash(w http.ResponseWriter, kind, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    url.QueryEscape(kind + "|" + message),
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60,
	})
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1})
	raw, err := url.QueryUnescape(c.Value)
	if err != nil {
		return nil
	}
	kind, msg, ok := strings.Cut(raw, "|")
	if !ok {
		return nil
	}
	return &flash{Kind: kind, Message: msg}
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, runner.ErrNotFound), errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	default:
		s.log.Error("request failed", "path", r.URL.Path, "err", err)
		http.Error(w, "something went wrong: "+err.Error(), http.StatusInternalServerError)
	}
}
