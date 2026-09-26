package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr         string
	DataDir            string
	RunnerDataDir      string
	RunnerDataHostRoot string
	RunnerImage        string
	RunnerIcon         string
	ContainerPrefix    string
	GitLabRunnerImage  string
	GitLabJobImage     string
	GitLabRunnerIcon   string
	GitLabPrefix       string
	UnraidHostname     string
	Timezone           string
	AdminUsername      string
	AdminPassword      string
	SecureCookies      bool
	WatchtowerLabels   bool
	SessionTTL         time.Duration
	Version            string
}

const (
	defaultListenAddr    = ":8080"
	defaultDataDir       = "/config"
	defaultRunnerDataDir = "/runners"
	defaultRunnerImage   = "myoung34/github-runner:latest"
	defaultRunnerIcon    = "https://raw.githubusercontent.com/nwithan8/unraid_templates/master/images/github-runner-icon.png"
	defaultPrefix        = "Github-Runner"
	defaultGitLabImage   = "gitlab/gitlab-runner:latest"
	defaultGitLabJob     = "alpine:latest"
	defaultGitLabIcon    = "https://about.gitlab.com/images/press/logo/png/gitlab-icon-rgb.png"
	defaultGitLabPrefix  = "Gitlab-Runner"
	defaultHostname      = "Tower"
	defaultTimezone      = "UTC"
	defaultAdmin         = "admin"
	defaultSessionTTL    = 30 * 24 * time.Hour
)

func FromEnv(lookup func(string) (string, bool)) (Config, error) {
	get := func(key, fallback string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return fallback
	}
	cfg := Config{
		ListenAddr:         get("LISTEN_ADDR", defaultListenAddr),
		DataDir:            get("DATA_DIR", defaultDataDir),
		RunnerDataDir:      strings.TrimRight(get("RUNNER_DATA_DIR", defaultRunnerDataDir), "/"),
		RunnerDataHostRoot: strings.TrimRight(get("RUNNER_DATA_HOST_ROOT", ""), "/"),
		RunnerImage:        get("RUNNER_IMAGE", defaultRunnerImage),
		RunnerIcon:         get("RUNNER_ICON", defaultRunnerIcon),
		ContainerPrefix:    get("CONTAINER_PREFIX", defaultPrefix),
		GitLabRunnerImage:  get("GITLAB_RUNNER_IMAGE", defaultGitLabImage),
		GitLabJobImage:     get("GITLAB_JOB_IMAGE", defaultGitLabJob),
		GitLabRunnerIcon:   get("GITLAB_RUNNER_ICON", defaultGitLabIcon),
		GitLabPrefix:       get("GITLAB_CONTAINER_PREFIX", defaultGitLabPrefix),
		UnraidHostname:     get("UNRAID_HOSTNAME", defaultHostname),
		Timezone:           get("TZ", defaultTimezone),
		AdminUsername:      get("ADMIN_USERNAME", defaultAdmin),
		AdminPassword:      get("ADMIN_PASSWORD", ""),
	}
	var err error
	if cfg.SecureCookies, err = parseBool(get("SECURE_COOKIES", "false")); err != nil {
		return Config{}, fmt.Errorf("SECURE_COOKIES: %w", err)
	}
	if cfg.WatchtowerLabels, err = parseBool(get("WATCHTOWER_LABELS", "true")); err != nil {
		return Config{}, fmt.Errorf("WATCHTOWER_LABELS: %w", err)
	}
	if cfg.SessionTTL, err = time.ParseDuration(get("SESSION_TTL", defaultSessionTTL.String())); err != nil {
		return Config{}, fmt.Errorf("SESSION_TTL: %w", err)
	}
	if cfg.SessionTTL <= 0 {
		return Config{}, errors.New("SESSION_TTL must be positive")
	}
	if cfg.RunnerDataDir == "" {
		return Config{}, errors.New("RUNNER_DATA_DIR must not be empty")
	}
	if !strings.HasPrefix(cfg.RunnerDataDir, "/") {
		return Config{}, errors.New("RUNNER_DATA_DIR must be an absolute path")
	}
	if cfg.RunnerDataHostRoot != "" && !strings.HasPrefix(cfg.RunnerDataHostRoot, "/") {
		return Config{}, errors.New("RUNNER_DATA_HOST_ROOT must be an absolute path")
	}
	return cfg, nil
}

func Load() (Config, error) {
	return FromEnv(os.LookupEnv)
}

func parseBool(v string) (bool, error) {
	return strconv.ParseBool(v)
}
