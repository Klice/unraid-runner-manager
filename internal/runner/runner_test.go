package runner

import (
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/dockerapi"
	"github.com/Klice/unraid-runner-manager/internal/dockerfake"
	"github.com/Klice/unraid-runner-manager/internal/names"
	"github.com/Klice/unraid-runner-manager/internal/provider"
	"github.com/Klice/unraid-runner-manager/internal/provider/github"
	"github.com/Klice/unraid-runner-manager/internal/provider/gitlab"
	"github.com/Klice/unraid-runner-manager/internal/provider/gitlab/gitlabtest"
)

func newService(t *testing.T, fake *dockerfake.Fake) (*Service, string) {
	svc, root, _ := newServiceWithGitLab(t, fake)
	return svc, root
}

func newServiceWithGitLab(t *testing.T, fake *dockerfake.Fake) (*Service, string, *gitlabtest.Server) {
	t.Helper()
	root := t.TempDir()
	srv := gitlabtest.New(t)
	svc := New(Options{
		Docker: fake,
		Names:  names.New(rand.NewPCG(1, 1)),
		Providers: provider.Registry{
			provider.GitHub: github.New(github.Options{
				Image:    "myoung34/github-runner:latest",
				Icon:     "https://example.invalid/icon.png",
				Prefix:   "Github-Runner",
				Hostname: "Tower",
				Timezone: "America/New_York",
			}),
			provider.GitLab: gitlab.New(gitlab.Options{
				Image:    "gitlab/gitlab-runner:latest",
				JobImage: "alpine:latest",
				Icon:     "https://example.invalid/gitlab.png",
				Prefix:   "Gitlab-Runner",
				Timezone: "America/New_York",
				HTTP:     srv.Client(),
			}),
		},
		HostRoot:  "/mnt/user/appdata/github-runners",
		LocalRoot: root,
	})
	return svc, root, srv
}

func create(t *testing.T, svc *Service, owner, repo string, labels ...string) Runner {
	t.Helper()
	r, err := svc.Create(t.Context(), CreateRequest{
		Owner:    owner,
		Provider: provider.GitHub,
		Target:   repo,
		Token:    "TOKEN123",
		Labels:   labels,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Wait()
	return r
}

func TestCreateProvisionsContainerAndFolders(t *testing.T) {
	fake := dockerfake.New()
	svc, root := newService(t, fake)
	r := create(t, svc, "max", "Klice/homelab", "toaster")

	if !names.Valid(r.Name) {
		t.Fatalf("bad generated name %q", r.Name)
	}
	if r.ContainerName != "Github-Runner-max-"+r.Name {
		t.Fatalf("container name %q", r.ContainerName)
	}
	if _, err := os.Stat(filepath.Join(root, "max", r.Name, "work")); err != nil {
		t.Fatalf("work dir missing: %v", err)
	}
	rec, ok := fake.Get(r.ContainerName)
	if !ok {
		t.Fatal("container not created")
	}
	if rec.State != "running" {
		t.Fatalf("container state %q", rec.State)
	}
	if fake.Pulled[0] != "myoung34/github-runner:latest" {
		t.Fatalf("image not pulled: %v", fake.Pulled)
	}
	env := strings.Join(rec.Spec.Env, "\n")
	for _, want := range []string{
		"REPO_URL=https://github.com/Klice/homelab",
		"RUNNER_NAME=" + r.Name,
		"RUNNER_TOKEN=TOKEN123",
		"LABELS=toaster",
		"RUNNER_WORKDIR=/mnt/user/appdata/github-runners/max/" + r.Name + "/work",
		"TZ=America/New_York",
		"HOST_CONTAINERNAME=" + r.ContainerName,
		"DISABLE_AUTOMATIC_DEREGISTRATION=true",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q", want)
		}
	}
	if !rec.Spec.RestartAlways || rec.Spec.PidsLimit != 2048 {
		t.Fatalf("host config not applied: %+v", rec.Spec)
	}
	binds := map[string]string{}
	for _, b := range rec.Spec.Binds {
		binds[b.Target] = b.Source
	}
	if binds["/runner/persistent_files"] != "/mnt/user/appdata/github-runners/max/"+r.Name {
		t.Fatalf("persistent mount wrong: %v", binds)
	}
	if binds["/var/run/docker.sock"] != "/var/run/docker.sock" || binds["/tmp/runner"] != "/tmp/runner" {
		t.Fatalf("expected socket and tmp binds: %v", binds)
	}
	if rec.Labels[LabelOwner] != "max" || rec.Labels[LabelRepo] != "Klice/homelab" || rec.Labels[LabelManaged] != "true" {
		t.Fatalf("labels wrong: %v", rec.Labels)
	}
}

func TestListReadsBackFromLabels(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	a := create(t, svc, "max", "Klice/homelab", "gpu", "fast")
	b := create(t, svc, "ola", "off-by-one-pixel/toy-gallery")

	list, err := svc.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 runners, got %d", len(list))
	}
	got, err := svc.Get(t.Context(), a.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "max" || got.Repo != "Klice/homelab" || got.State != "running" || !slices.Equal(got.Labels, []string{"gpu", "fast"}) {
		t.Fatalf("unexpected runner %+v", got)
	}
	if got.DataDir != "/mnt/user/appdata/github-runners/max/"+a.Name || got.RepoURL != "https://github.com/Klice/homelab" {
		t.Fatalf("unexpected paths %+v", got)
	}
	if other, _ := svc.Get(t.Context(), b.Name); other.Owner != "ola" || other.Labels != nil {
		t.Fatalf("unexpected second runner %+v", other)
	}
}

func TestCreateRejectsEmptyToken(t *testing.T) {
	svc, _ := newService(t, dockerfake.New())
	if _, err := svc.Create(t.Context(), CreateRequest{Owner: "max", Provider: provider.GitHub, Target: "a/b", Token: "  "}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateShowsPendingThenFailure(t *testing.T) {
	fake := dockerfake.New()
	fake.PullDelay = 200 * time.Millisecond
	fake.PullErr = errors.New("registry unreachable")
	svc, root := newService(t, fake)

	r, err := svc.Create(t.Context(), CreateRequest{Owner: "max", Provider: provider.GitHub, Target: "Klice/x", Token: "T"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := svc.Get(t.Context(), r.Name)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != StateCreating || !pending.Pending() {
		t.Fatalf("expected creating state, got %+v", pending)
	}
	if err := svc.Delete(t.Context(), r.Name); !errors.Is(err, ErrInProgress) {
		t.Fatalf("expected ErrInProgress, got %v", err)
	}
	svc.Wait()
	failed, err := svc.Get(t.Context(), r.Name)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != StateFailed || !strings.Contains(failed.Error, "registry unreachable") {
		t.Fatalf("expected failed state, got %+v", failed)
	}
	if _, err := os.Stat(filepath.Join(root, "max", r.Name)); !os.IsNotExist(err) {
		t.Fatalf("data folder should be cleaned up after failure: %v", err)
	}
	if fake.Count() != 0 {
		t.Fatal("no container should exist")
	}
	svc.Dismiss(r.Name)
	if _, err := svc.Get(t.Context(), r.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected dismissed runner to vanish, got %v", err)
	}
}

func TestFailedEntriesExpire(t *testing.T) {
	fake := dockerfake.New()
	fake.CreateErr = errors.New("boom")
	svc, _ := newService(t, fake)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	svc.opts.Now = func() time.Time { return now }
	r := create(t, svc, "max", "Klice/x")
	if got, _ := svc.Get(t.Context(), r.Name); got.State != StateFailed {
		t.Fatalf("expected failed, got %+v", got)
	}
	now = now.Add(failedRetention + time.Minute)
	if _, err := svc.Get(t.Context(), r.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected expiry, got %v", err)
	}
}

func TestDeleteRemovesContainerAndData(t *testing.T) {
	fake := dockerfake.New()
	svc, root := newService(t, fake)
	r := create(t, svc, "max", "Klice/homelab")
	if err := os.WriteFile(filepath.Join(root, "max", r.Name, "work", "junk.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(t.Context(), r.Name); err != nil {
		t.Fatal(err)
	}
	if fake.Count() != 0 {
		t.Fatal("container should be removed")
	}
	if _, err := os.Stat(filepath.Join(root, "max", r.Name)); !os.IsNotExist(err) {
		t.Fatalf("data folder should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "max")); err != nil {
		t.Fatalf("owner folder should remain: %v", err)
	}
	if err := svc.Delete(t.Context(), r.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestLifecycleActionsAndLogs(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	r := create(t, svc, "max", "Klice/homelab")
	ctx := t.Context()
	if err := svc.Stop(ctx, r.Name); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, r.Name); got.State != StateExited {
		t.Fatalf("expected exited, got %s", got.State)
	}
	if err := svc.Start(ctx, r.Name); err != nil {
		t.Fatal(err)
	}
	fake.SetState(r.ContainerName, "paused", "Up 1 day (Paused)")
	if got, _ := svc.Get(ctx, r.Name); !got.Paused() {
		t.Fatalf("expected paused, got %s", got.State)
	}
	if err := svc.Unpause(ctx, r.Name); err != nil {
		t.Fatal(err)
	}
	if err := svc.Restart(ctx, r.Name); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, r.Name); !got.Running() {
		t.Fatalf("expected running, got %s", got.State)
	}
	rec, _ := fake.Get(r.ContainerName)
	fake.LogText[rec.ID] = "one\ntwo\nthree\n"
	rc, err := svc.Logs(ctx, r.Name, dockerapi.LogOptions{Tail: 2})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if string(b) != "two\nthree\n" {
		t.Fatalf("unexpected logs %q", b)
	}
	if _, err := svc.Logs(ctx, "nope", dockerapi.LogOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNamesAreUniqueAcrossOwners(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	seen := map[string]bool{}
	for i := range 20 {
		owner := "max"
		if i%2 == 1 {
			owner = "ola"
		}
		r := create(t, svc, owner, "Klice/homelab")
		if seen[r.Name] {
			t.Fatalf("duplicate name %q", r.Name)
		}
		seen[r.Name] = true
	}
}

func createGitLab(t *testing.T, svc *Service, srv *gitlabtest.Server, token string) Runner {
	t.Helper()
	r, err := svc.Create(t.Context(), CreateRequest{
		Owner:    "ola",
		Provider: provider.GitLab,
		Target:   srv.ProjectURL("ola/toy-gallery"),
		Token:    token,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Wait()
	return r
}

func TestGitLabCreateRegistersAndKeepsTokenOutOfEnv(t *testing.T) {
	fake := dockerfake.New()
	svc, root, srv := newServiceWithGitLab(t, fake)
	r := createGitLab(t, svc, srv, srv.ValidToken)

	got, err := svc.Get(t.Context(), r.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != provider.GitLab || !got.IsGitLab() || got.State != StateRunning {
		t.Fatalf("unexpected runner %+v", got)
	}
	if got.ContainerName != "Gitlab-Runner-ola-"+r.Name || got.Repo != "127.0.0.1:"+strings.Split(srv.URL, ":")[2]+"/ola/toy-gallery" || got.RepoURL != srv.ProjectURL("ola/toy-gallery") {
		t.Fatalf("unexpected naming %+v", got)
	}
	if got.Image != "gitlab/gitlab-runner:latest" {
		t.Fatalf("unexpected image %s", got.Image)
	}
	if v := srv.VerifiedTokens(); len(v) != 1 || v[0] != srv.ValidToken {
		t.Fatalf("token should be verified once, got %v", v)
	}
	rec, ok := fake.Get(got.ContainerName)
	if !ok {
		t.Fatal("container missing")
	}
	if strings.Contains(strings.Join(rec.Spec.Env, "\n"), srv.ValidToken) {
		t.Fatal("token leaked into container environment")
	}
	if rec.Labels[LabelProvider] != "gitlab" || rec.Labels[LabelURL] != srv.ProjectURL("ola/toy-gallery") {
		t.Fatalf("labels wrong: %v", rec.Labels)
	}
	cfg, err := gitlab.ReadConfig(filepath.Join(root, "ola", r.Name, "config", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runners[0].Token != srv.ValidToken || cfg.Runners[0].URL != srv.URL {
		t.Fatalf("config.toml wrong: %+v", cfg.Runners[0])
	}
}

func TestGitLabCreateFailsOnRejectedToken(t *testing.T) {
	fake := dockerfake.New()
	svc, root, srv := newServiceWithGitLab(t, fake)
	r := createGitLab(t, svc, srv, "glrt-bad")
	got, _ := svc.Get(t.Context(), r.Name)
	if got.State != StateFailed || !strings.Contains(got.Error, "rejected") {
		t.Fatalf("expected failure with rejection message, got %+v", got)
	}
	if fake.Count() != 0 {
		t.Fatal("no container should be created when registration fails")
	}
	if _, err := os.Stat(filepath.Join(root, "ola", r.Name)); !os.IsNotExist(err) {
		t.Fatal("data folder should be cleaned up")
	}
}

func TestGitLabLabelsAreRejected(t *testing.T) {
	svc, _, srv := newServiceWithGitLab(t, dockerfake.New())
	_, err := svc.Create(t.Context(), CreateRequest{Owner: "ola", Provider: provider.GitLab, Target: srv.ProjectURL("a/b"), Token: "x", Labels: []string{"gpu"}})
	if err == nil || !strings.Contains(err.Error(), "tags") {
		t.Fatalf("expected labels to be rejected for gitlab, got %v", err)
	}
}

func TestGitLabDeleteDeregisters(t *testing.T) {
	fake := dockerfake.New()
	svc, root, srv := newServiceWithGitLab(t, fake)
	r := createGitLab(t, svc, srv, srv.ValidToken)
	if err := svc.Delete(t.Context(), r.Name); err != nil {
		t.Fatal(err)
	}
	if d := srv.DeletedTokens(); len(d) != 1 || d[0] != srv.ValidToken {
		t.Fatalf("expected one delete call with the stored token, got %v (unexpected requests: %v)", d, srv.Unexpected)
	}
	if fake.Count() != 0 {
		t.Fatal("container should be removed")
	}
	if _, err := os.Stat(filepath.Join(root, "ola", r.Name)); !os.IsNotExist(err) {
		t.Fatal("data folder should be removed")
	}
}

func TestGitLabDeleteContinuesWhenServerFails(t *testing.T) {
	fake := dockerfake.New()
	svc, root, srv := newServiceWithGitLab(t, fake)
	r := createGitLab(t, svc, srv, srv.ValidToken)
	srv.SetFailure(500)
	err := svc.Delete(t.Context(), r.Name)
	if !errors.Is(err, ErrDeregister) {
		t.Fatalf("expected ErrDeregister, got %v", err)
	}
	if fake.Count() != 0 {
		t.Fatal("container should still be removed")
	}
	if _, err := os.Stat(filepath.Join(root, "ola", r.Name)); !os.IsNotExist(err) {
		t.Fatal("data folder should still be removed")
	}
}

func TestLegacyContainersDefaultToGitHub(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	_, err := fake.Create(t.Context(), dockerapi.CreateSpec{
		Name:  "Github-Runner-max-old-one",
		Image: "myoung34/github-runner:latest",
		Labels: map[string]string{
			LabelManaged: "true",
			LabelOwner:   "max",
			LabelName:    "old-one",
			LabelRepo:    "Klice/legacy",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(t.Context(), "old-one")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != provider.GitHub || got.RepoURL != "https://github.com/Klice/legacy" || got.Repo != "Klice/legacy" {
		t.Fatalf("legacy container not mapped to github: %+v", got)
	}
}

func TestGitLabConcurrencyRoundTrip(t *testing.T) {
	fake := dockerfake.New()
	svc, root, srv := newServiceWithGitLab(t, fake)
	r, err := svc.Create(t.Context(), CreateRequest{Owner: "ola", Provider: provider.GitLab, Target: srv.ProjectURL("ola/toy-gallery"), Token: srv.ValidToken, Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	svc.Wait()
	got, _ := svc.Get(t.Context(), r.Name)
	if got.Concurrency != 3 {
		t.Fatalf("concurrency not read back from labels: %+v", got)
	}
	cfg, err := gitlab.ReadConfig(filepath.Join(root, "ola", r.Name, "config", "config.toml"))
	if err != nil || cfg.Concurrent != 3 {
		t.Fatalf("config concurrency wrong: %v %+v", err, cfg)
	}
	for _, bad := range []int{-1, 9} {
		if _, err := svc.Create(t.Context(), CreateRequest{Owner: "ola", Provider: provider.GitLab, Target: srv.ProjectURL("a/b"), Token: srv.ValidToken, Concurrency: bad}); err == nil {
			t.Fatalf("concurrency %d should be rejected", bad)
		}
	}
	if _, err := svc.Create(t.Context(), CreateRequest{Owner: "max", Provider: provider.GitHub, Target: "Klice/x", Token: "T", Concurrency: 2}); err == nil {
		t.Fatal("github should reject concurrency above one")
	}
	gh := create(t, svc, "max", "Klice/homelab")
	if got, _ := svc.Get(t.Context(), gh.Name); got.Concurrency != 1 {
		t.Fatalf("github runners default to one job, got %d", got.Concurrency)
	}
}
