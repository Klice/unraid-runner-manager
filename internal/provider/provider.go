package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/Klice/unraid-runner-manager/internal/dockerapi"
)

type Kind string

const (
	GitHub Kind = "github"
	GitLab Kind = "gitlab"
)

var ErrUnknownKind = errors.New("unknown provider")

func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case GitHub:
		return GitHub, nil
	case GitLab:
		return GitLab, nil
	default:
		return "", fmt.Errorf("%w %q", ErrUnknownKind, s)
	}
}

func (k Kind) Title() string {
	switch k {
	case GitLab:
		return "GitLab"
	default:
		return "GitHub"
	}
}

type Target struct {
	URL     string
	Display string
}

type Runner struct {
	Name          string
	Owner         string
	ContainerName string
	URL           string
	Display       string
	Labels        []string
	Concurrency   int
	HostDataDir   string
	LocalDataDir  string
}

const MaxConcurrency = 8

type Provider interface {
	Kind() Kind
	Image() string
	ContainerPrefix() string
	SupportsLabels() bool
	SupportsConcurrency() bool
	ParseTarget(input string) (Target, error)
	Prepare(ctx context.Context, r Runner, token string) error
	Spec(r Runner, token string) dockerapi.CreateSpec
	Deregister(ctx context.Context, r Runner) error
}

type Registry map[Kind]Provider

func (reg Registry) Get(kind Kind) (Provider, error) {
	p, ok := reg[kind]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
	return p, nil
}
