package github

import (
	"reflect"
	"testing"
)

func TestParseRepo(t *testing.T) {
	cases := map[string]Repo{
		"Klice/unraid-runner-manager":                     {Owner: "Klice", Name: "unraid-runner-manager"},
		"https://github.com/off-by-one-pixel/toy-gallery": {Owner: "off-by-one-pixel", Name: "toy-gallery"},
		"https://github.com/Klice/homelab.git":            {Owner: "Klice", Name: "homelab"},
		"github.com/Klice/dotfiles/":                      {Owner: "Klice", Name: "dotfiles"},
		"git@github.com:Klice/dotfiles.git":               {Owner: "Klice", Name: "dotfiles"},
		"  Klice/spaces  ":                                {Owner: "Klice", Name: "spaces"},
	}
	for in, want := range cases {
		got, err := ParseRepo(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %+v want %+v", in, got, want)
		}
	}
}

func TestParseRepoRejects(t *testing.T) {
	for _, in := range []string{"", "justname", "a/b/c", "-bad/repo", "owner/", "/repo", "owner/re po", "https://gitlab.com/a/b", "owner/.."} {
		if _, err := ParseRepo(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

func TestRepoURLs(t *testing.T) {
	r := Repo{Owner: "Klice", Name: "homelab"}
	if r.URL() != "https://github.com/Klice/homelab" || r.FullName() != "Klice/homelab" {
		t.Fatalf("unexpected urls: %s %s", r.URL(), r.FullName())
	}
	if r.NewRunnerURL() != "https://github.com/Klice/homelab/settings/actions/runners/new" {
		t.Fatalf("unexpected new runner url: %s", r.NewRunnerURL())
	}
}

func TestParseLabels(t *testing.T) {
	got, err := ParseLabels(" toaster, gpu ,toaster\nfast ")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"toaster", "gpu", "fast"}) {
		t.Fatalf("got %v", got)
	}
	if got, err := ParseLabels(""); err != nil || len(got) != 0 {
		t.Fatalf("empty labels: %v %v", got, err)
	}
	if _, err := ParseLabels("ok,bad label!"); err == nil {
		t.Fatal("expected error for invalid label")
	}
}
