package github

import (
	"strings"
	"testing"

	"github.com/Klice/unraid-runner-manager/internal/provider"
)

func newRunner() provider.Runner {
	return provider.Runner{
		Name:          "fluffy-toaster",
		Owner:         "max",
		ContainerName: "Github-Runner-max-fluffy-toaster",
		URL:           "https://github.com/Klice/homelab",
		Display:       "Klice/homelab",
		Labels:        []string{"toaster"},
		HostDataDir:   "/mnt/user/appdata/github-runners/max/fluffy-toaster",
	}
}

func TestSpecCarriesWatchtowerLabelsWhenEnabled(t *testing.T) {
	p := New(Options{Image: "img", Icon: "icon", Prefix: "Github-Runner", Hostname: "Tower", Timezone: "UTC", Watchtower: true})
	spec := p.Spec(newRunner(), "TOKEN")
	if spec.Labels[provider.WatchtowerEnableLabel] != "true" {
		t.Fatalf("enable label missing: %v", spec.Labels)
	}
	hook := spec.Labels[provider.WatchtowerPreUpdateLabel]
	if !strings.Contains(hook, "Runner[.]Worker") || !strings.HasSuffix(hook, "&& exit 75 || exit 0") {
		t.Fatalf("pre-update hook wrong: %q", hook)
	}
	if _, ok := spec.Labels[provider.WatchtowerStopSignalLabel]; ok {
		t.Fatal("github runners should not override the stop signal")
	}
	if spec.Labels["net.unraid.docker.icon"] != "icon" {
		t.Fatal("icon label lost")
	}
}

func TestSpecWithoutWatchtowerLabels(t *testing.T) {
	p := New(Options{Image: "img", Icon: "icon", Prefix: "Github-Runner", Hostname: "Tower", Timezone: "UTC"})
	spec := p.Spec(newRunner(), "TOKEN")
	for _, label := range []string{provider.WatchtowerEnableLabel, provider.WatchtowerPreUpdateLabel, provider.WatchtowerStopSignalLabel} {
		if _, ok := spec.Labels[label]; ok {
			t.Fatalf("unexpected label %s", label)
		}
	}
	if len(spec.Labels) != 1 {
		t.Fatalf("expected only the icon label, got %v", spec.Labels)
	}
}
