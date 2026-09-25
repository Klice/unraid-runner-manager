package web

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/dockerapi"
	"github.com/Klice/unraid-runner-manager/internal/github"
	"github.com/Klice/unraid-runner-manager/internal/names"
	"github.com/Klice/unraid-runner-manager/internal/runner"
	"github.com/Klice/unraid-runner-manager/internal/store"
)

type runnersData struct {
	Runners  []runner.Runner
	Scope    string
	Query    string
	Total    int
	Running  int
	Hostname string
}

func (s *Server) visibleRunners(r *http.Request, scope, query string) ([]runner.Runner, error) {
	u := currentUser(r)
	all, err := s.runners.List(r.Context())
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []runner.Runner
	for _, rn := range all {
		if !canSee(u, rn) {
			continue
		}
		if u.IsAdmin() && scope == "mine" && rn.Owner != u.Username {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(rn.Name), q) && !strings.Contains(strings.ToLower(rn.Repo), q) && !strings.Contains(strings.ToLower(rn.Owner), q) {
			continue
		}
		out = append(out, rn)
	}
	return out, nil
}

func canSee(u *store.User, rn runner.Runner) bool {
	return u.IsAdmin() || rn.Owner == u.Username
}

func scopeOf(r *http.Request) string {
	scope := r.FormValue("scope")
	if !currentUser(r).IsAdmin() || scope != "mine" {
		return "all"
	}
	return "mine"
}

func (s *Server) runnersTableData(r *http.Request) (runnersData, error) {
	scope := scopeOf(r)
	query := strings.TrimSpace(r.FormValue("q"))
	list, err := s.visibleRunners(r, scope, query)
	if err != nil {
		return runnersData{}, err
	}
	data := runnersData{Runners: list, Scope: scope, Query: query, Total: len(list), Hostname: s.cfg.UnraidHostname}
	for _, rn := range list {
		if rn.Running() {
			data.Running++
		}
	}
	return data, nil
}

func (s *Server) runnersPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.runnersTableData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "runners", "", data)
}

func (s *Server) runnersTable(w http.ResponseWriter, r *http.Request) {
	data, err := s.runnersTableData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "runners", "runners_table", data)
}

type newRunnerData struct {
	Repo            string
	Labels          string
	Error           string
	ContainerPrefix string
	HostRoot        string
	Image           string
	Owner           string
}

func (s *Server) newRunnerForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "runner_new", "", s.newRunnerDefaults(r))
}

func (s *Server) newRunnerDefaults(r *http.Request) newRunnerData {
	return newRunnerData{
		ContainerPrefix: s.cfg.ContainerPrefix,
		HostRoot:        s.cfg.RunnerDataHostRoot,
		Image:           s.cfg.RunnerImage,
		Owner:           currentUser(r).Username,
	}
}

func (s *Server) createRunner(w http.ResponseWriter, r *http.Request) {
	data := s.newRunnerDefaults(r)
	data.Repo = strings.TrimSpace(r.FormValue("repo"))
	data.Labels = strings.TrimSpace(r.FormValue("labels"))
	respond := func(msg string) {
		data.Error = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "runner_new", "", data)
	}
	repo, err := github.ParseRepo(data.Repo)
	if err != nil {
		respond(err.Error())
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		respond("Runner token is required.")
		return
	}
	labels, err := github.ParseLabels(data.Labels)
	if err != nil {
		respond(err.Error())
		return
	}
	created, err := s.runners.Create(r.Context(), runner.CreateRequest{
		Owner:  currentUser(r).Username,
		Repo:   repo,
		Token:  token,
		Labels: labels,
	})
	if err != nil {
		respond(err.Error())
		return
	}
	s.log.Info("runner requested", "runner", created.Name, "owner", created.Owner, "repo", created.Repo)
	http.Redirect(w, r, "/runners/"+created.Name+"?created=1", http.StatusSeeOther)
}

type runnerData struct {
	Runner  runner.Runner
	Created bool
	Tail    int
}

var tailChoices = []int{100, 500, 2000}

func (s *Server) ownedRunner(r *http.Request) (runner.Runner, error) {
	name := r.PathValue("name")
	if !names.Valid(name) {
		return runner.Runner{}, runner.ErrNotFound
	}
	rn, err := s.runners.Get(r.Context(), name)
	if err != nil {
		return runner.Runner{}, err
	}
	if !canSee(currentUser(r), rn) {
		return runner.Runner{}, runner.ErrNotFound
	}
	return rn, nil
}

func (s *Server) runnerPage(w http.ResponseWriter, r *http.Request) {
	rn, err := s.ownedRunner(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "runner", "", runnerData{Runner: rn, Created: r.URL.Query().Get("created") == "1", Tail: tailParam(r)})
}

func tailParam(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("tail"))
	if err != nil || !slices.Contains(tailChoices, n) {
		return 500
	}
	return n
}

func (s *Server) runnerLogs(w http.ResponseWriter, r *http.Request) {
	rn, err := s.ownedRunner(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	follow := r.URL.Query().Get("follow") != "0"
	rc, err := s.runners.Logs(r.Context(), rn.Name, dockerapi.LogOptions{Tail: tailParam(r), Follow: follow})
	if err != nil {
		if errors.Is(err, runner.ErrInProgress) {
			http.Error(w, "runner is still being created", http.StatusConflict)
			return
		}
		s.fail(w, r, err)
		return
	}
	defer func() {
		if err := rc.Close(); err != nil {
			s.log.Debug("closing log stream", "runner", rn.Name, "err", err)
		}
	}()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(rc)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- strings.TrimRight(scanner.Text(), "\r"):
			case <-r.Context().Done():
				return
			}
		}
	}()
	streamEvents(r.Context(), w, flusher, lines)
}

func streamEvents(ctx context.Context, w io.Writer, flusher http.Flusher, lines <-chan string) {
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	send := func(event string) bool {
		if _, err := io.WriteString(w, event); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if !send(": ping\n\n") {
				return
			}
		case line, more := <-lines:
			if !more {
				send("event: end\ndata: \n\n")
				return
			}
			if !send("data: " + line + "\n\n") {
				return
			}
		}
	}
}

func (s *Server) runnerAction(w http.ResponseWriter, r *http.Request) {
	rn, err := s.ownedRunner(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	action := r.PathValue("action")
	switch action {
	case "start":
		err = s.runners.Start(r.Context(), rn.Name)
	case "stop":
		err = s.runners.Stop(r.Context(), rn.Name)
	case "restart":
		err = s.runners.Restart(r.Context(), rn.Name)
	case "resume":
		err = s.runners.Unpause(r.Context(), rn.Name)
	case "dismiss":
		s.runners.Dismiss(rn.Name)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		if errors.Is(err, runner.ErrInProgress) {
			http.Error(w, "runner is still being created", http.StatusConflict)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.log.Info("runner action", "runner", rn.Name, "action", action, "user", currentUser(r).Username)
	if isHTMX(r) && r.FormValue("view") == "table" {
		s.runnersTable(w, r)
		return
	}
	back := "/runners/" + rn.Name
	if action == "dismiss" || r.FormValue("view") == "table" {
		back = "/runners"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) deleteRunnerConfirm(w http.ResponseWriter, r *http.Request) {
	rn, err := s.ownedRunner(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	block := ""
	if isHTMX(r) {
		block = "dialog"
	}
	s.render(w, r, "runner_delete", block, runnerData{Runner: rn})
}

func (s *Server) deleteRunner(w http.ResponseWriter, r *http.Request) {
	rn, err := s.ownedRunner(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.runners.Delete(r.Context(), rn.Name); err != nil {
		if errors.Is(err, runner.ErrInProgress) {
			http.Error(w, "runner is still being created", http.StatusConflict)
			return
		}
		s.fail(w, r, err)
		return
	}
	s.setFlash(w, "ok", "Deleted "+rn.Name+" and its data folder.")
	s.redirect(w, r, "/runners")
}
