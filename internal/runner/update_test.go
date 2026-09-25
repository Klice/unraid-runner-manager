package runner

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/dockerfake"
)

const image = "myoung34/github-runner:latest"

func TestCheckUpdatesUpToDate(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	r := create(t, svc, "max", "Klice/homelab")
	before, _ := fake.Get(r.ContainerName)

	report := svc.CheckUpdates(t.Context())
	if report.Error != "" || len(report.Updated) != 0 || len(report.Deferred) != 0 || len(report.Failed) != 0 {
		t.Fatalf("expected a no-op report, got %+v", report)
	}
	if report.Summary() != "all runners up to date" {
		t.Fatalf("unexpected summary %q", report.Summary())
	}
	after, _ := fake.Get(r.ContainerName)
	if after.ID != before.ID {
		t.Fatal("container should not have been recreated")
	}
	if got, ok := svc.LastUpdate(); !ok || got.CheckedAt.IsZero() {
		t.Fatal("last update report should be recorded")
	}
}

func TestCheckUpdatesRecreatesOutdatedRunner(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	r := create(t, svc, "max", "Klice/homelab", "toaster")
	stopped := create(t, svc, "ola", "Klice/other")
	_ = svc.Stop(t.Context(), stopped.Name)
	old, _ := fake.Get(r.ContainerName)
	fake.SetLatestImageID(image, "sha256:new")

	report := svc.CheckUpdates(t.Context())
	if report.Error != "" || len(report.Failed) != 0 || len(report.Deferred) != 0 {
		t.Fatalf("unexpected report %+v", report)
	}
	if !slices.Contains(report.Updated, r.Name) || !slices.Contains(report.Updated, stopped.Name) {
		t.Fatalf("both runners should be updated, got %v", report.Updated)
	}
	fresh, ok := fake.Get(r.ContainerName)
	if !ok {
		t.Fatal("container should exist under its original name")
	}
	if fresh.ID == old.ID || fresh.ImageID != "sha256:new" || fresh.State != "running" {
		t.Fatalf("container not recreated on the new image: %+v", fresh.Container)
	}
	if !slices.Equal(fresh.Spec.Env, old.Spec.Env) || !slices.Equal(fresh.Spec.Binds, old.Spec.Binds) || fresh.Labels[LabelOwner] != "max" {
		t.Fatal("environment, binds and labels must be carried over")
	}
	if !fresh.Spec.RestartAlways || fresh.Spec.PidsLimit != 2048 {
		t.Fatalf("host config lost: %+v", fresh.Spec)
	}
	if got, _ := fake.Get(stopped.ContainerName); got.State != "created" && got.State != "exited" {
		t.Fatalf("a stopped runner must stay stopped after update, got %s", got.State)
	}
	if fake.Count() != 2 {
		t.Fatalf("old containers should be removed, have %d", fake.Count())
	}
	if _, leftover := fake.Get(r.ContainerName + oldNameSuffix); leftover {
		t.Fatal("renamed old container should be gone")
	}
	if !strings.Contains(report.Summary(), "2 updated") {
		t.Fatalf("unexpected summary %q", report.Summary())
	}
}

func TestCheckUpdatesDefersBusyRunner(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	r := create(t, svc, "max", "Klice/homelab")
	old, _ := fake.Get(r.ContainerName)
	fake.SetLatestImageID(image, "sha256:new")
	fake.SetBusy(r.ContainerName, true)

	report := svc.CheckUpdates(t.Context())
	if len(report.Updated) != 0 || !slices.Contains(report.Deferred, r.Name) {
		t.Fatalf("busy runner should be deferred, got %+v", report)
	}
	if got, _ := fake.Get(r.ContainerName); got.ID != old.ID || got.State != "running" {
		t.Fatal("busy runner must be left untouched")
	}

	fake.SetBusy(r.ContainerName, false)
	report = svc.CheckUpdates(t.Context())
	if !slices.Contains(report.Updated, r.Name) {
		t.Fatalf("idle runner should be updated on the next check, got %+v", report)
	}
}

func TestCheckUpdatesRollsBackWhenCreateFails(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	r := create(t, svc, "max", "Klice/homelab")
	old, _ := fake.Get(r.ContainerName)
	fake.SetLatestImageID(image, "sha256:new")
	fake.CreateErr = errors.New("disk full")

	report := svc.CheckUpdates(t.Context())
	if msg := report.Failed[r.Name]; !strings.Contains(msg, "disk full") {
		t.Fatalf("expected create failure, got %+v", report)
	}
	restored, ok := fake.Get(r.ContainerName)
	if !ok || restored.ID != old.ID || restored.State != "running" {
		t.Fatalf("old container must be renamed back and restarted: %+v ok=%v", restored.Container, ok)
	}
	if fake.Count() != 1 {
		t.Fatalf("no extra containers expected, have %d", fake.Count())
	}
	if got, _ := svc.Get(t.Context(), r.Name); got.State != StateRunning {
		t.Fatalf("updating overlay must be cleared, got %s", got.State)
	}
}

func TestCheckUpdatesReportsPullFailure(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	create(t, svc, "max", "Klice/homelab")
	fake.PullErr = errors.New("registry down")
	report := svc.CheckUpdates(t.Context())
	if !strings.Contains(report.Error, "registry down") || !strings.HasPrefix(report.Summary(), "failed") {
		t.Fatalf("unexpected report %+v", report)
	}
}

func TestRunUpdaterHonoursDisabledInterval(t *testing.T) {
	svc, _ := newService(t, dockerfake.New())
	done := make(chan struct{})
	go func() {
		svc.RunUpdater(t.Context(), 0, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("updater should return immediately when disabled")
	}
}

func TestRunUpdaterRunsFirstCheckAfterDelay(t *testing.T) {
	fake := dockerfake.New()
	svc, _ := newService(t, fake)
	go svc.RunUpdater(t.Context(), time.Hour, 20*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := svc.LastUpdate(); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("first update check did not run")
}
