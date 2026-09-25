package web

import (
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/auth"
	"github.com/Klice/unraid-runner-manager/internal/config"
	"github.com/Klice/unraid-runner-manager/internal/dockerfake"
	"github.com/Klice/unraid-runner-manager/internal/names"
	"github.com/Klice/unraid-runner-manager/internal/runner"
	"github.com/Klice/unraid-runner-manager/internal/store"
)

type env struct {
	t       *testing.T
	ts      *httptest.Server
	fake    *dockerfake.Fake
	runners *runner.Service
	store   *store.Store
	root    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	fake := dockerfake.New()
	root := filepath.Join(dir, "runners")
	svc := runner.New(runner.Options{
		Docker:          fake,
		Names:           names.New(rand.NewPCG(9, 9)),
		Image:           "myoung34/github-runner:latest",
		ContainerPrefix: "Github-Runner",
		HostRoot:        "/mnt/user/appdata/github-runners",
		LocalRoot:       root,
		Hostname:        "Tower",
		Timezone:        "UTC",
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	cfg, _ := config.FromEnv(func(string) (string, bool) { return "", false })
	cfg.RunnerDataHostRoot = "/mnt/user/appdata/github-runners"
	cfg.AdminUsername = "max"
	cfg.AdminPassword = "max-password-1"
	srv, err := New(cfg, st, svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.EnsureAdmin(t.Context()); err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPassword("ola-password-1")
	if _, err := st.CreateUser(t.Context(), "ola", hash, store.RoleUser, false); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &env{t: t, ts: ts, fake: fake, runners: svc, store: st, root: root}
}

func (e *env) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:     jar,
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (e *env) login(username, password string) *http.Client {
	e.t.Helper()
	c := e.client()
	res := e.post(c, "/login", url.Values{"username": {username}, "password": {password}}, nil)
	if res.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("login %s: status %d", username, res.StatusCode)
	}
	return c
}

func (e *env) get(c *http.Client, path string, headers map[string]string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) post(c *http.Client, path string, form url.Values, headers map[string]string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *env) createRunner(c *http.Client, repo string) string {
	e.t.Helper()
	res := e.post(c, "/runners", url.Values{"repo": {repo}, "token": {"TOKEN"}, "labels": {""}}, nil)
	if res.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("create runner: status %d body %s", res.StatusCode, body(e.t, res))
	}
	loc := res.Header.Get("Location")
	m := regexp.MustCompile(`^/runners/([a-z0-9-]+)\?created=1$`).FindStringSubmatch(loc)
	if m == nil {
		e.t.Fatalf("unexpected redirect %q", loc)
	}
	e.runners.Wait()
	return m[1]
}

func TestRequiresLogin(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	res := e.get(c, "/runners", nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Fatalf("expected redirect to login, got %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res = e.get(c, "/runners/table", map[string]string{"HX-Request": "true"})
	if res.StatusCode != http.StatusNoContent || res.Header.Get("HX-Redirect") != "/login" {
		t.Fatalf("expected HX-Redirect, got %d %s", res.StatusCode, res.Header.Get("HX-Redirect"))
	}
	if res := e.get(c, "/healthz", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("healthz should be public, got %d", res.StatusCode)
	}
}

func TestLoginFailureAndLockout(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	for i := range 5 {
		res := e.post(c, "/login", url.Values{"username": {"max"}, "password": {"nope"}}, nil)
		if res.StatusCode != http.StatusOK || !strings.Contains(body(t, res), "Wrong username or password") {
			t.Fatalf("attempt %d: expected error page", i)
		}
	}
	res := e.post(c, "/login", url.Values{"username": {"max"}, "password": {"max-password-1"}}, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body(t, res), "Too many attempts") {
		t.Fatal("expected lockout after repeated failures")
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	e := newEnv(t)
	c := e.login("max", "max-password-1")
	res := e.post(c, "/logout", nil, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", res.StatusCode)
	}
	res = e.post(c, "/logout", nil, map[string]string{"Origin": "https://evil.example"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for foreign origin, got %d", res.StatusCode)
	}
}

func TestCreateListAndScoping(t *testing.T) {
	e := newEnv(t)
	admin := e.login("max", "max-password-1")
	user := e.login("ola", "ola-password-1")

	mine := e.createRunner(admin, "Klice/homelab")
	theirs := e.createRunner(user, "https://github.com/off-by-one-pixel/toy-gallery")

	page := body(t, e.get(admin, "/runners", nil))
	if !strings.Contains(page, mine) || !strings.Contains(page, theirs) || !strings.Contains(page, "<th>Owner</th>") {
		t.Fatal("admin should see every runner with an owner column")
	}
	page = body(t, e.get(admin, "/runners/table?scope=mine", nil))
	if !strings.Contains(page, mine) || strings.Contains(page, theirs) {
		t.Fatal("scope=mine should hide other owners")
	}
	page = body(t, e.get(admin, "/runners/table?q=toy-gallery", nil))
	if strings.Contains(page, mine) || !strings.Contains(page, theirs) {
		t.Fatal("query filter should match repository")
	}

	page = body(t, e.get(user, "/runners", nil))
	if strings.Contains(page, mine) || !strings.Contains(page, theirs) || strings.Contains(page, "<th>Owner</th>") {
		t.Fatal("user should only see their own runners and no owner column")
	}
	if res := e.get(user, "/runners/"+mine, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("user must not open another user's runner, got %d", res.StatusCode)
	}
	if res := e.post(user, "/runners/"+mine+"/stop", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("user must not act on another user's runner, got %d", res.StatusCode)
	}
	if res := e.post(user, "/runners/"+mine+"/delete", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("user must not delete another user's runner, got %d", res.StatusCode)
	}
	rec, ok := e.fake.Get("Github-Runner-ola-" + theirs)
	if !ok || rec.Labels[runner.LabelOwner] != "ola" {
		t.Fatalf("container should be namespaced by owner: %+v", rec)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	c := e.login("ola", "ola-password-1")
	res := e.post(c, "/runners", url.Values{"repo": {"not a repo"}, "token": {"T"}}, nil)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body(t, res), "owner/repo") {
		t.Fatal("expected repo validation error")
	}
	res = e.post(c, "/runners", url.Values{"repo": {"a/b"}, "token": {" "}}, nil)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body(t, res), "token is required") {
		t.Fatal("expected token validation error")
	}
	res = e.post(c, "/runners", url.Values{"repo": {"a/b"}, "token": {"T"}, "labels": {"bad label!"}}, nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatal("expected label validation error")
	}
	if e.fake.Count() != 0 {
		t.Fatal("no container should have been created")
	}
}

func TestActionsDetailAndLogs(t *testing.T) {
	e := newEnv(t)
	c := e.login("ola", "ola-password-1")
	name := e.createRunner(c, "Klice/homelab")

	res := e.get(c, "/runners/"+name+"?created=1", nil)
	page := body(t, res)
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "Runner requested") || !strings.Contains(page, "Github-Runner-ola-"+name) {
		t.Fatalf("detail page wrong: %d", res.StatusCode)
	}

	res = e.post(c, "/runners/"+name+"/stop", url.Values{"view": {"table"}}, map[string]string{"HX-Request": "true"})
	if res.StatusCode != http.StatusOK || !strings.Contains(body(t, res), `id="runners-table"`) {
		t.Fatal("htmx action should return the table partial")
	}
	if rec, _ := e.fake.Get("Github-Runner-ola-" + name); rec.State != "exited" {
		t.Fatalf("container should be stopped, got %s", rec.State)
	}
	res = e.post(c, "/runners/"+name+"/start", nil, nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/runners/"+name {
		t.Fatalf("plain action should redirect to detail, got %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res := e.post(c, "/runners/"+name+"/explode", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Fatal("unknown action should 404")
	}

	rec, _ := e.fake.Get("Github-Runner-ola-" + name)
	e.fake.LogText[rec.ID] = "Listening for Jobs\nRunning job: build\n"
	res = e.get(c, "/runners/"+name+"/logs?follow=0&tail=100", nil)
	if res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected event stream, got %s", res.Header.Get("Content-Type"))
	}
	stream := body(t, res)
	if !strings.Contains(stream, "data: Listening for Jobs\n\n") || !strings.Contains(stream, "data: Running job: build\n\n") || !strings.Contains(stream, "event: end") {
		t.Fatalf("unexpected stream: %q", stream)
	}
}

func TestDeleteFlow(t *testing.T) {
	e := newEnv(t)
	c := e.login("ola", "ola-password-1")
	name := e.createRunner(c, "Klice/homelab")

	res := e.get(c, "/runners/"+name+"/delete", map[string]string{"HX-Request": "true"})
	dialog := body(t, res)
	if strings.Contains(dialog, "<html") || !strings.Contains(dialog, "Delete "+name+"?") || !strings.Contains(dialog, "/mnt/user/appdata/github-runners/ola/"+name) {
		t.Fatal("htmx confirm should return the dialog fragment with the data path")
	}
	full := body(t, e.get(c, "/runners/"+name+"/delete", nil))
	if !strings.Contains(full, "<html") || !strings.Contains(full, "Delete "+name+"?") {
		t.Fatal("plain confirm should return a full page")
	}
	res = e.post(c, "/runners/"+name+"/delete", nil, map[string]string{"HX-Request": "true"})
	if res.StatusCode != http.StatusNoContent || res.Header.Get("HX-Redirect") != "/runners" {
		t.Fatalf("expected HX-Redirect, got %d", res.StatusCode)
	}
	if e.fake.Count() != 0 {
		t.Fatal("container should be removed")
	}
	if _, err := os.Stat(filepath.Join(e.root, "ola", name)); !os.IsNotExist(err) {
		t.Fatal("data folder should be removed")
	}
	if !strings.Contains(body(t, e.get(c, "/runners", nil)), "Deleted "+name) {
		t.Fatal("flash message should be shown after delete")
	}
	if res := e.get(c, "/runners/"+name, nil); res.StatusCode != http.StatusNotFound {
		t.Fatal("deleted runner should 404")
	}
}

func TestUserManagement(t *testing.T) {
	e := newEnv(t)
	admin := e.login("max", "max-password-1")
	user := e.login("ola", "ola-password-1")

	if res := e.get(user, "/users", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("non-admin must not see users page, got %d", res.StatusCode)
	}
	if res := e.post(user, "/users", url.Values{"username": {"sam"}, "role": {"user"}, "password": {"temporary-pass-1"}}, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("non-admin must not create users, got %d", res.StatusCode)
	}

	res := e.post(admin, "/users", url.Values{"username": {"Sam"}, "role": {"user"}, "password": {"temporary-pass-1"}}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create user: %d %s", res.StatusCode, body(t, res))
	}
	page := body(t, e.get(admin, "/users", nil))
	if !strings.Contains(page, "Created sam") || !strings.Contains(page, "temporary-pass-1") {
		t.Fatal("flash with temporary password expected")
	}
	res = e.post(admin, "/users", url.Values{"username": {"sam"}, "role": {"user"}, "password": {"temporary-pass-1"}}, nil)
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body(t, res), "already taken") {
		t.Fatal("duplicate username should be rejected")
	}
	res = e.post(admin, "/users", url.Values{"username": {"tim"}, "role": {"user"}, "password": {"short"}}, nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatal("short temporary password should be rejected")
	}

	sam := e.login("sam", "temporary-pass-1")
	res = e.get(sam, "/runners", nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/account/password" {
		t.Fatalf("temporary password should force a change, got %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res = e.post(sam, "/account/password", url.Values{"current": {"temporary-pass-1"}, "password": {"brand-new-pass-2"}, "confirm": {"different"}}, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body(t, res), "do not match") {
		t.Fatal("mismatched confirmation should be rejected")
	}
	res = e.post(sam, "/account/password", url.Values{"current": {"temporary-pass-1"}, "password": {"brand-new-pass-2"}, "confirm": {"brand-new-pass-2"}}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("password change failed: %d", res.StatusCode)
	}
	if res := e.get(sam, "/runners", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("after password change runners should load, got %d", res.StatusCode)
	}

	samUser, _ := e.store.UserByName(t.Context(), "sam")
	idPath := "/users/" + itoa(samUser.ID)
	e.createRunner(sam, "Klice/homelab")
	e.post(admin, idPath+"/disable", nil, nil)
	if res := e.get(sam, "/runners", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatal("disabled user should be signed out")
	}
	e.post(admin, idPath+"/delete", nil, nil)
	if !strings.Contains(body(t, e.get(admin, "/users", nil)), "Delete the runners owned by sam first") {
		t.Fatal("deleting a user with runners must be refused")
	}
	if _, err := e.store.UserByName(t.Context(), "sam"); err != nil {
		t.Fatal("user should still exist")
	}
	e.post(admin, idPath+"/reset", nil, nil)
	if !strings.Contains(body(t, e.get(admin, "/users", nil)), "Password for sam reset") {
		t.Fatal("reset should confirm with a temporary password")
	}
	maxUser, _ := e.store.UserByName(t.Context(), "max")
	e.post(admin, "/users/"+itoa(maxUser.ID)+"/disable", nil, nil)
	if !strings.Contains(body(t, e.get(admin, "/users", nil)), "cannot disable your own account") {
		t.Fatal("self-disable must be refused")
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	c := e.login("max", "max-password-1")
	if res := e.post(c, "/logout", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", res.StatusCode)
	}
	if res := e.get(c, "/runners", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatal("session should be gone after logout")
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
