package gitlab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/dockerapi"
	"github.com/Klice/unraid-runner-manager/internal/provider"
)

const (
	labelIcon     = "net.unraid.docker.icon"
	configDirName = "config"
	cacheDirName  = "cache"
	buildsDirName = "builds"
)

type Options struct {
	Image      string
	JobImage   string
	Icon       string
	Prefix     string
	Timezone   string
	Watchtower bool
	HTTP       *http.Client
	Logger     *slog.Logger
	Now        func() time.Time
}

type Provider struct {
	opts Options
	api  *Client
}

func New(opts Options) *Provider {
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Provider{opts: opts, api: &Client{HTTP: opts.HTTP}}
}

func (p *Provider) Kind() provider.Kind     { return provider.GitLab }
func (p *Provider) Image() string           { return p.opts.Image }
func (p *Provider) ContainerPrefix() string { return p.opts.Prefix }
func (p *Provider) SupportsLabels() bool    { return false }

func (p *Provider) ParseTarget(input string) (provider.Target, error) {
	proj, err := ParseProject(input)
	if err != nil {
		return provider.Target{}, err
	}
	return provider.Target{URL: proj.URL(), Display: proj.Display()}, nil
}

func (p *Provider) Prepare(ctx context.Context, r provider.Runner, token string) error {
	proj, err := ParseProject(r.URL)
	if err != nil {
		return err
	}
	info, err := p.api.Verify(ctx, proj.BaseURL(), token)
	if err != nil {
		return err
	}
	for _, sub := range []string{configDirName, cacheDirName, buildsDirName} {
		if err := os.MkdirAll(filepath.Join(r.LocalDataDir, sub), 0o755); err != nil {
			return fmt.Errorf("create %s folder: %w", sub, err)
		}
	}
	cfg := Config{
		Concurrent: 1,
		Runners: []RunnerConfig{{
			Name:            r.Name,
			URL:             proj.BaseURL(),
			ID:              info.ID,
			Token:           token,
			TokenObtainedAt: p.opts.Now().UTC(),
			Executor:        "docker",
			Docker: DockerConfig{
				Image:        p.opts.JobImage,
				DisableCache: true,
				Volumes: []string{
					"/var/run/docker.sock:/var/run/docker.sock",
					r.HostDataDir + "/" + cacheDirName + ":/cache",
					r.HostDataDir + "/" + buildsDirName + ":/builds",
				},
			},
		}},
	}
	return WriteConfig(configPath(r.LocalDataDir), cfg)
}

func configPath(localDataDir string) string {
	return filepath.Join(localDataDir, configDirName, "config.toml")
}

func (p *Provider) Spec(r provider.Runner, _ string) dockerapi.CreateSpec {
	return dockerapi.CreateSpec{
		Name:   r.ContainerName,
		Image:  p.opts.Image,
		Env:    []string{"TZ=" + p.opts.Timezone},
		Labels: p.labels(),
		Binds: []dockerapi.Bind{
			{Source: r.HostDataDir + "/" + configDirName, Target: "/etc/gitlab-runner"},
			{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		},
		RestartAlways: true,
	}
}

func (p *Provider) labels() map[string]string {
	labels := map[string]string{labelIcon: p.opts.Icon}
	if p.opts.Watchtower {
		maps.Copy(labels, provider.WatchtowerGracefulStop("SIGQUIT"))
	}
	return labels
}

func (p *Provider) Deregister(ctx context.Context, r provider.Runner) error {
	cfg, err := ReadConfig(configPath(r.LocalDataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			p.opts.Logger.Warn("gitlab runner config missing, skipping deregistration", "runner", r.Name)
			return nil
		}
		return err
	}
	if len(cfg.Runners) == 0 || cfg.Runners[0].Token == "" {
		p.opts.Logger.Warn("gitlab runner config has no token, skipping deregistration", "runner", r.Name)
		return nil
	}
	rc := cfg.Runners[0]
	err = p.api.Delete(ctx, rc.URL, rc.Token)
	if errors.Is(err, ErrTokenRejected) {
		p.opts.Logger.Info("gitlab runner already removed on the server", "runner", r.Name)
		return nil
	}
	return err
}
