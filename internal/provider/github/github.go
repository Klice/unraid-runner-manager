package github

import (
	"context"
	"maps"
	"strings"

	"github.com/Klice/unraid-runner-manager/internal/dockerapi"
	"github.com/Klice/unraid-runner-manager/internal/provider"
)

const (
	labelIcon = "net.unraid.docker.icon"
	busyProbe = "grep -qa 'Runner[.]Worker' /proc/[0-9]*/cmdline"
)

type Options struct {
	Image      string
	Icon       string
	Prefix     string
	Hostname   string
	Timezone   string
	Watchtower bool
}

type Provider struct {
	opts Options
}

func New(opts Options) *Provider {
	return &Provider{opts: opts}
}

func (p *Provider) Kind() provider.Kind     { return provider.GitHub }
func (p *Provider) Image() string           { return p.opts.Image }
func (p *Provider) ContainerPrefix() string { return p.opts.Prefix }
func (p *Provider) SupportsLabels() bool    { return true }
func (p *Provider) SupportsConcurrency() bool {
	return false
}

func (p *Provider) ParseTarget(input string) (provider.Target, error) {
	repo, err := ParseRepo(input)
	if err != nil {
		return provider.Target{}, err
	}
	return provider.Target{URL: repo.URL(), Display: repo.FullName()}, nil
}

func (p *Provider) Prepare(context.Context, provider.Runner, string) error {
	return nil
}

func (p *Provider) Spec(r provider.Runner, token string) dockerapi.CreateSpec {
	work := r.HostDataDir + "/work"
	return dockerapi.CreateSpec{
		Name:  r.ContainerName,
		Image: p.opts.Image,
		Env: []string{
			"TZ=" + p.opts.Timezone,
			"HOST_OS=Unraid",
			"HOST_HOSTNAME=" + p.opts.Hostname,
			"HOST_CONTAINERNAME=" + r.ContainerName,
			"REPO_URL=" + r.URL,
			"RUNNER_NAME=" + r.Name,
			"RUNNER_TOKEN=" + token,
			"RUNNER_SCOPE=repo",
			"RUNNER_GROUP=",
			"LABELS=" + strings.Join(r.Labels, ","),
			"RUNNER_WORKDIR=" + work,
			"DISABLE_AUTOMATIC_DEREGISTRATION=true",
			"CONFIGURED_ACTIONS_RUNNER_FILES_DIR=/runner/persistent_files",
		},
		Labels: p.labels(),
		Binds: []dockerapi.Bind{
			{Source: "/tmp/runner", Target: "/tmp/runner"},
			{Source: r.HostDataDir, Target: "/runner/persistent_files"},
			{Source: work, Target: work},
			{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		},
		PidsLimit:     2048,
		RestartAlways: true,
	}
}

func (p *Provider) labels() map[string]string {
	labels := map[string]string{labelIcon: p.opts.Icon}
	if p.opts.Watchtower {
		maps.Copy(labels, provider.WatchtowerSkipWhileBusy(busyProbe))
	}
	return labels
}

func (p *Provider) Deregister(context.Context, provider.Runner) error {
	return nil
}
