package gitlab

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/provider"
	"github.com/Klice/unraid-runner-manager/internal/provider/gitlab/gitlabtest"
)

func TestParseProject(t *testing.T) {
	cases := map[string]Project{
		"https://gitlab.com/max/toy-gallery":                  {Scheme: "https", Host: "gitlab.com", Path: "max/toy-gallery"},
		"https://gitlab.com/group/sub/project/":               {Scheme: "https", Host: "gitlab.com", Path: "group/sub/project"},
		"https://gitlab.com/max/toy-gallery.git":              {Scheme: "https", Host: "gitlab.com", Path: "max/toy-gallery"},
		"https://gitlab.com/max/toy-gallery/-/settings/ci_cd": {Scheme: "https", Host: "gitlab.com", Path: "max/toy-gallery"},
		"gitlab.example.lan/max/toy":                          {Scheme: "https", Host: "gitlab.example.lan", Path: "max/toy"},
		"http://127.0.0.1:8929/max/toy":                       {Scheme: "http", Host: "127.0.0.1:8929", Path: "max/toy"},
	}
	for in, want := range cases {
		got, err := ParseProject(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %+v want %+v", in, got, want)
		}
	}
	for _, bad := range []string{"", "max", "https://gitlab.com/max", "ftp://gitlab.com/a/b", "https://gitlab.com/a/../b", "https://gitlab.com/a b/c"} {
		if _, err := ParseProject(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	p, _ := ParseProject("https://gitlab.com/max/toy-gallery")
	if p.URL() != "https://gitlab.com/max/toy-gallery" || p.BaseURL() != "https://gitlab.com" || p.Display() != "max/toy-gallery" {
		t.Fatalf("unexpected derived values %+v", p)
	}
	self, _ := ParseProject("https://gitlab.example.lan/max/toy")
	if self.Display() != "gitlab.example.lan/max/toy" {
		t.Fatalf("self-managed display should include the host, got %s", self.Display())
	}
}

func TestClientVerifyAndDelete(t *testing.T) {
	srv := gitlabtest.New(t)
	c := &Client{HTTP: srv.Client()}

	info, err := c.Verify(t.Context(), srv.URL, srv.ValidToken)
	if err != nil || info.ID != 4242 {
		t.Fatalf("verify: %+v %v", info, err)
	}
	if _, err := c.Verify(t.Context(), srv.URL, "glrt-wrong"); !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("expected ErrTokenRejected, got %v", err)
	}
	if err := c.Delete(t.Context(), srv.URL, srv.ValidToken); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(t.Context(), srv.URL, "glrt-wrong"); !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("expected ErrTokenRejected, got %v", err)
	}
	srv.SetFailure(500)
	if _, err := c.Verify(t.Context(), srv.URL, srv.ValidToken); err == nil || errors.Is(err, ErrTokenRejected) {
		t.Fatalf("server errors must not look like a rejected token: %v", err)
	}
	if _, err := c.Verify(t.Context(), "http://127.0.0.1:1", srv.ValidToken); err == nil {
		t.Fatal("unreachable host should error")
	}
}

func newProvider(t *testing.T, srv *gitlabtest.Server) (*Provider, provider.Runner) {
	t.Helper()
	p := New(Options{
		Image:    "gitlab/gitlab-runner:latest",
		JobImage: "alpine:latest",
		Icon:     "https://example.invalid/gitlab.png",
		Prefix:   "Gitlab-Runner",
		Timezone: "UTC",
		HTTP:     srv.Client(),
		Now:      func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) },
	})
	r := provider.Runner{
		Name:          "fluffy-toaster",
		Owner:         "max",
		ContainerName: "Gitlab-Runner-max-fluffy-toaster",
		URL:           srv.ProjectURL("max/toy-gallery"),
		Display:       "max/toy-gallery",
		HostDataDir:   "/mnt/user/appdata/github-runners/max/fluffy-toaster",
		LocalDataDir:  filepath.Join(t.TempDir(), "max", "fluffy-toaster"),
	}
	return p, r
}

func TestPrepareWritesConfigAndSpecHasNoToken(t *testing.T) {
	srv := gitlabtest.New(t)
	p, r := newProvider(t, srv)

	if err := p.Prepare(t.Context(), r, srv.ValidToken); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.LocalDataDir, "config", "config.toml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != fs.FileMode(0o600) {
		t.Fatalf("config must be private, mode %v", info.Mode().Perm())
	}
	for _, sub := range []string{"cache", "builds"} {
		if _, err := os.Stat(filepath.Join(r.LocalDataDir, sub)); err != nil {
			t.Fatalf("%s folder missing: %v", sub, err)
		}
	}
	cfg, err := ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrent != 1 || len(cfg.Runners) != 1 {
		t.Fatalf("unexpected config %+v", cfg)
	}
	rc := cfg.Runners[0]
	if rc.Token != srv.ValidToken || rc.ID != 4242 || rc.URL != srv.URL || rc.Executor != "docker" || rc.Name != "fluffy-toaster" {
		t.Fatalf("unexpected runner config %+v", rc)
	}
	if rc.Docker.Image != "alpine:latest" || !rc.Docker.DisableCache {
		t.Fatalf("unexpected docker config %+v", rc.Docker)
	}
	volumes := strings.Join(rc.Docker.Volumes, "\n")
	for _, want := range []string{"/var/run/docker.sock:/var/run/docker.sock", r.HostDataDir + "/cache:/cache", r.HostDataDir + "/builds:/builds"} {
		if !strings.Contains(volumes, want) {
			t.Errorf("volumes missing %q", want)
		}
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "[[runners]]") || !strings.Contains(string(raw), "[runners.docker]") {
		t.Fatalf("config not in gitlab-runner layout:\n%s", raw)
	}

	spec := p.Spec(r, srv.ValidToken)
	if strings.Contains(strings.Join(spec.Env, "\n"), "glrt-") {
		t.Fatal("token must not be placed in the container environment")
	}
	if spec.Image != "gitlab/gitlab-runner:latest" || spec.Name != r.ContainerName || !spec.RestartAlways {
		t.Fatalf("unexpected spec %+v", spec)
	}
	binds := map[string]string{}
	for _, b := range spec.Binds {
		binds[b.Target] = b.Source
	}
	if binds["/etc/gitlab-runner"] != r.HostDataDir+"/config" || binds["/var/run/docker.sock"] != "/var/run/docker.sock" {
		t.Fatalf("unexpected binds %v", binds)
	}
}

func TestPrepareRejectsBadToken(t *testing.T) {
	srv := gitlabtest.New(t)
	p, r := newProvider(t, srv)
	err := p.Prepare(t.Context(), r, "glrt-nope")
	if !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("expected ErrTokenRejected, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.LocalDataDir, "config", "config.toml")); !os.IsNotExist(err) {
		t.Fatal("no config should be written for a rejected token")
	}
}

func TestDeregister(t *testing.T) {
	srv := gitlabtest.New(t)
	p, r := newProvider(t, srv)

	if err := p.Deregister(t.Context(), r); err != nil {
		t.Fatalf("missing config should be tolerated, got %v", err)
	}
	if err := p.Prepare(t.Context(), r, srv.ValidToken); err != nil {
		t.Fatal(err)
	}
	if err := p.Deregister(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if got := srv.DeletedTokens(); len(got) != 1 || got[0] != srv.ValidToken {
		t.Fatalf("delete should use the stored token, got %v", got)
	}
	srv.ValidToken = "glrt-rotated"
	if err := p.Deregister(t.Context(), r); err != nil {
		t.Fatalf("an already-removed runner should not fail deletion, got %v", err)
	}
	srv.SetFailure(500)
	if err := p.Deregister(t.Context(), r); err == nil {
		t.Fatal("server errors must surface")
	}
}

func TestSpecWatchtowerLabels(t *testing.T) {
	srv := gitlabtest.New(t)
	_, r := newProvider(t, srv)
	with := New(Options{Image: "img", JobImage: "job", Icon: "icon", Prefix: "Gitlab-Runner", Timezone: "UTC", Watchtower: true, HTTP: srv.Client()})
	spec := with.Spec(r, "tok")
	if spec.Labels[provider.WatchtowerEnableLabel] != "true" || spec.Labels[provider.WatchtowerStopSignalLabel] != "SIGQUIT" {
		t.Fatalf("watchtower labels wrong: %v", spec.Labels)
	}
	if _, ok := spec.Labels[provider.WatchtowerPreUpdateLabel]; ok {
		t.Fatal("gitlab runners have no pre-update probe")
	}
	without := New(Options{Image: "img", JobImage: "job", Icon: "icon", Prefix: "Gitlab-Runner", Timezone: "UTC", HTTP: srv.Client()})
	if labels := without.Spec(r, "tok").Labels; len(labels) != 1 || labels["net.unraid.docker.icon"] != "icon" {
		t.Fatalf("expected only the icon label, got %v", labels)
	}
}
