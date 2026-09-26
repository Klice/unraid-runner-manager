package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/runner"
	"github.com/Klice/unraid-runner-manager/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type views struct {
	pages map[string]*template.Template
}

var pageFiles = []string{"login", "runners", "runner_new", "runner", "runner_delete", "users", "password"}

func loadViews() (*views, error) {
	funcs := template.FuncMap{
		"date":     func(t time.Time) string { return t.Local().Format("2006-01-02") },
		"datetime": formatOptional,
		"pill":     pillClass,
		"join":     strings.Join,
		"lower":    strings.ToLower,
		"dict":     dict,
	}
	base, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/_runners_table.html", "templates/_runner_actions.html")
	if err != nil {
		return nil, err
	}
	v := &views{pages: map[string]*template.Template{}}
	for _, name := range pageFiles {
		clone, err := base.Clone()
		if err != nil {
			return nil, err
		}
		page, err := clone.ParseFS(templateFS, "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		v.pages[name] = page
	}
	return v, nil
}

func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	out := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
		}
		out[key] = kv[i+1]
	}
	return out, nil
}

func formatOptional(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func pillClass(state string) string {
	switch state {
	case runner.StateRunning:
		return "running"
	case runner.StatePaused, "restarting":
		return "paused"
	case runner.StateCreating:
		return "creating"
	case runner.StateFailed:
		return "failed"
	default:
		return "exited"
	}
}

type pageData struct {
	User    *store.User
	Version string
	Flash   *flash
	Path    string
	Data    any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page, block string, data any) {
	tmpl, ok := s.views.pages[page]
	if !ok {
		http.Error(w, "missing view "+page, http.StatusInternalServerError)
		return
	}
	pd := pageData{User: currentUser(r), Version: s.cfg.Version, Path: r.URL.Path, Data: data}
	if block == "" {
		block = "layout"
		pd.Flash = s.takeFlash(w, r)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, block, pd); err != nil {
		s.log.Error("render failed", "page", page, "block", block, "err", err)
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	})
}
