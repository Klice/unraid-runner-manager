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
	"github.com/Klice/unraid-runner-manager/internal/github"
	"github.com/Klice/unraid-runner-manager/internal/names"
)

func newService(t *testing.T, fake *dockerfake.Fake) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	svc := New(Options{
		Docker:          fake,
		Names:           names.New(rand.NewPCG(1, 1)),
		Image:           "myoung34/github-runner:latest",
		Icon:            "https://example.invalid/icon.png",
		ContainerPrefix: "Github-Runner",
		HostRoot:        "/mnt/user/appdata/github-runners",
		LocalRoot:       root,
		Hostname:        "Tower",
		Timezone:        "America/New_York",
	})
	return svc, root
}

func create(t *testing.T, svc *Service, owner, repo string, labels ...string) Runner {
	t.Helper()
	r, err := svc.Create(t.Context(), CreateRequest{
		Owner:  owner,
		Repo:   github.Repo{Owner: strings.Split(repo, "/")[0], Name: strings.Split(repo, "/")[1]},
		Token:  "TOKEN123",
		Labels: labels,
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
	if _, err := svc.Create(t.Context(), CreateRequest{Owner: "max", Repo: github.Repo{Owner: "a", Name: "b"}, Token: "  "}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateShowsPendingThenFailure(t *testing.T) {
	fake := dockerfake.New()
	fake.PullDelay = 200 * time.Millisecond
	fake.PullErr = errors.New("registry unreachable")
	svc, root := newService(t, fake)

	r, err := svc.Create(t.Context(), CreateRequest{Owner: "max", Repo: github.Repo{Owner: "Klice", Name: "x"}, Token: "T"})
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
