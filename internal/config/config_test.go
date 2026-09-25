package config

import (
	"testing"
	"time"
)

func envFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := FromEnv(envFrom(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8080" || cfg.RunnerDataDir != "/runners" || cfg.RunnerImage != defaultRunnerImage {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.SessionTTL != 30*24*time.Hour || cfg.SecureCookies {
		t.Fatalf("unexpected session defaults: %+v", cfg)
	}
	if cfg.UpdateInterval != 24*time.Hour {
		t.Fatalf("unexpected update interval: %v", cfg.UpdateInterval)
	}
}

func TestUpdateIntervalCanBeDisabled(t *testing.T) {
	cfg, err := FromEnv(envFrom(map[string]string{"RUNNER_UPDATE_INTERVAL": "0"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpdateInterval != 0 {
		t.Fatalf("expected disabled updater, got %v", cfg.UpdateInterval)
	}
	if _, err := FromEnv(envFrom(map[string]string{"RUNNER_UPDATE_INTERVAL": "-1h"})); err == nil {
		t.Fatal("negative interval should be rejected")
	}
	if _, err := FromEnv(envFrom(map[string]string{"RUNNER_UPDATE_INTERVAL": "daily"})); err == nil {
		t.Fatal("unparsable interval should be rejected")
	}
}

func TestOverridesAndTrimming(t *testing.T) {
	cfg, err := FromEnv(envFrom(map[string]string{
		"RUNNER_DATA_HOST_ROOT": "/mnt/user/appdata/github-runners/",
		"RUNNER_DATA_DIR":       "/data/",
		"SECURE_COOKIES":        "true",
		"SESSION_TTL":           "1h",
		"TZ":                    " America/New_York ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RunnerDataHostRoot != "/mnt/user/appdata/github-runners" || cfg.RunnerDataDir != "/data" {
		t.Fatalf("paths not normalised: %+v", cfg)
	}
	if !cfg.SecureCookies || cfg.SessionTTL != time.Hour || cfg.Timezone != "America/New_York" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"bool":     {"SECURE_COOKIES": "maybe"},
		"ttl":      {"SESSION_TTL": "soon"},
		"zero ttl": {"SESSION_TTL": "0s"},
		"relative": {"RUNNER_DATA_HOST_ROOT": "appdata/runners"},
		"rel dir":  {"RUNNER_DATA_DIR": "runners"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := FromEnv(envFrom(env)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
