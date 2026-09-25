package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/dockerapi"
	"github.com/Klice/unraid-runner-manager/internal/github"
	"github.com/Klice/unraid-runner-manager/internal/names"
)

const (
	LabelManaged = "runner-manager.managed"
	LabelOwner   = "runner-manager.owner"
	LabelName    = "runner-manager.name"
	LabelRepo    = "runner-manager.repo"
	LabelLabels  = "runner-manager.labels"
	LabelDataDir = "runner-manager.data"
	labelIcon    = "net.unraid.docker.icon"

	StateCreating = "creating"
	StateFailed   = "failed"
	StateRunning  = "running"
	StatePaused   = "paused"
	StateExited   = "exited"

	failedRetention = 30 * time.Minute
)

var (
	ErrNotFound   = errors.New("runner not found")
	ErrInProgress = errors.New("runner is still being created")
)

type Runner struct {
	Name          string
	Owner         string
	Repo          string
	RepoURL       string
	ContainerName string
	ContainerID   string
	Image         string
	State         string
	Status        string
	Labels        []string
	Created       time.Time
	DataDir       string
	Error         string
}

func (r Runner) Pending() bool { return r.State == StateCreating || r.State == StateFailed }
func (r Runner) Running() bool { return r.State == StateRunning }
func (r Runner) Paused() bool  { return r.State == StatePaused }

type Options struct {
	Docker          dockerapi.Client
	Names           *names.Generator
	Image           string
	Icon            string
	ContainerPrefix string
	HostRoot        string
	LocalRoot       string
	Hostname        string
	Timezone        string
	Logger          *slog.Logger
	Now             func() time.Time
}

type Service struct {
	opts    Options
	mu      sync.Mutex
	pending map[string]*Runner
	wg      sync.WaitGroup
}

func New(opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Names == nil {
		opts.Names = names.New(nil)
	}
	return &Service{opts: opts, pending: map[string]*Runner{}}
}

func (s *Service) List(ctx context.Context) ([]Runner, error) {
	containers, err := s.opts.Docker.ListByLabel(ctx, LabelManaged+"=true")
	if err != nil {
		return nil, err
	}
	out := make([]Runner, 0, len(containers))
	for _, c := range containers {
		out = append(out, fromContainer(c))
	}
	out = append(out, s.pendingRunners(out)...)
	slices.SortFunc(out, func(a, b Runner) int { return b.Created.Compare(a.Created) })
	return out, nil
}

func (s *Service) pendingRunners(existing []Runner) []Runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Runner{}
	for name, p := range s.pending {
		expired := p.State == StateFailed && s.opts.Now().Sub(p.Created) > failedRetention
		if expired {
			delete(s.pending, name)
			continue
		}
		if slices.ContainsFunc(existing, func(r Runner) bool { return r.Name == name }) {
			continue
		}
		out = append(out, *p)
	}
	return out
}

func (s *Service) Get(ctx context.Context, name string) (Runner, error) {
	all, err := s.List(ctx)
	if err != nil {
		return Runner{}, err
	}
	for _, r := range all {
		if r.Name == name {
			return r, nil
		}
	}
	return Runner{}, ErrNotFound
}

type CreateRequest struct {
	Owner  string
	Repo   github.Repo
	Token  string
	Labels []string
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (Runner, error) {
	if strings.TrimSpace(req.Token) == "" {
		return Runner{}, errors.New("runner token is required")
	}
	existing, err := s.List(ctx)
	if err != nil {
		return Runner{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name, err := s.opts.Names.Unique(func(candidate string) bool {
		if _, ok := s.pending[candidate]; ok {
			return true
		}
		return slices.ContainsFunc(existing, func(r Runner) bool { return r.Name == candidate })
	})
	if err != nil {
		return Runner{}, err
	}
	r := &Runner{
		Name:          name,
		Owner:         req.Owner,
		Repo:          req.Repo.FullName(),
		RepoURL:       req.Repo.URL(),
		ContainerName: s.containerName(req.Owner, name),
		Image:         s.opts.Image,
		State:         StateCreating,
		Status:        "Preparing data folder",
		Labels:        req.Labels,
		Created:       s.opts.Now(),
		DataDir:       s.hostDataDir(req.Owner, name),
	}
	s.pending[name] = r
	snapshot := *r
	token := strings.TrimSpace(req.Token)
	s.wg.Go(func() { s.provision(snapshot, token) })
	return snapshot, nil
}

func (s *Service) provision(r Runner, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	log := s.opts.Logger.With("runner", r.Name, "owner", r.Owner)
	stage, err := s.runProvisionSteps(ctx, r, token)
	if err != nil {
		log.Error("runner creation failed", "stage", stage, "err", err)
		s.markFailed(r.Name, stage, err)
		if rmErr := os.RemoveAll(s.localDataDir(r.Owner, r.Name)); rmErr != nil {
			log.Warn("cleanup of data folder failed", "err", rmErr)
		}
		return
	}
	s.mu.Lock()
	delete(s.pending, r.Name)
	s.mu.Unlock()
	log.Info("runner created", "container", r.ContainerName, "repo", r.Repo)
}

func (s *Service) runProvisionSteps(ctx context.Context, r Runner, token string) (string, error) {
	local := s.localDataDir(r.Owner, r.Name)
	if err := os.MkdirAll(filepath.Join(local, "work"), 0o755); err != nil {
		return "creating data folder", err
	}
	s.setPendingStatus(r.Name, "Pulling image")
	if err := s.opts.Docker.PullImage(ctx, s.opts.Image); err != nil {
		return "pulling image", err
	}
	s.setPendingStatus(r.Name, "Creating container")
	id, err := s.opts.Docker.Create(ctx, s.spec(r, token))
	if err != nil {
		return "creating container", err
	}
	s.setPendingStatus(r.Name, "Starting container")
	if err := s.opts.Docker.Start(ctx, id); err != nil {
		return "starting container", errors.Join(err, s.opts.Docker.Remove(ctx, id))
	}
	return "", nil
}

func (s *Service) setPendingStatus(name, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pending[name]; ok {
		p.Status = status
	}
}

func (s *Service) markFailed(name, stage string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[name]
	if !ok {
		return
	}
	p.State = StateFailed
	p.Status = "Failed: " + stage
	p.Error = err.Error()
	p.Created = s.opts.Now()
}

func (s *Service) spec(r Runner, token string) dockerapi.CreateSpec {
	work := r.DataDir + "/work"
	return dockerapi.CreateSpec{
		Name:  r.ContainerName,
		Image: s.opts.Image,
		Env: []string{
			"TZ=" + s.opts.Timezone,
			"HOST_OS=Unraid",
			"HOST_HOSTNAME=" + s.opts.Hostname,
			"HOST_CONTAINERNAME=" + r.ContainerName,
			"REPO_URL=" + r.RepoURL,
			"RUNNER_NAME=" + r.Name,
			"RUNNER_TOKEN=" + token,
			"RUNNER_SCOPE=repo",
			"RUNNER_GROUP=",
			"LABELS=" + strings.Join(r.Labels, ","),
			"RUNNER_WORKDIR=" + work,
			"DISABLE_AUTOMATIC_DEREGISTRATION=true",
			"CONFIGURED_ACTIONS_RUNNER_FILES_DIR=/runner/persistent_files",
		},
		Labels: map[string]string{
			LabelManaged: "true",
			LabelOwner:   r.Owner,
			LabelName:    r.Name,
			LabelRepo:    r.Repo,
			LabelLabels:  strings.Join(r.Labels, ","),
			LabelDataDir: r.DataDir,
			labelIcon:    s.opts.Icon,
		},
		Binds: []dockerapi.Bind{
			{Source: "/tmp/runner", Target: "/tmp/runner"},
			{Source: r.DataDir, Target: "/runner/persistent_files"},
			{Source: work, Target: work},
			{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		},
		PidsLimit:     2048,
		RestartAlways: true,
	}
}

func (s *Service) Delete(ctx context.Context, name string) error {
	r, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	if r.State == StateCreating {
		return ErrInProgress
	}
	if r.ContainerID != "" {
		if err := s.opts.Docker.Remove(ctx, r.ContainerID); err != nil {
			return fmt.Errorf("remove container: %w", err)
		}
	}
	s.mu.Lock()
	delete(s.pending, name)
	s.mu.Unlock()
	if err := os.RemoveAll(s.localDataDir(r.Owner, r.Name)); err != nil {
		return fmt.Errorf("remove data folder: %w", err)
	}
	s.opts.Logger.Info("runner deleted", "runner", name, "owner", r.Owner)
	return nil
}

func (s *Service) Dismiss(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pending[name]; ok && p.State == StateFailed {
		delete(s.pending, name)
	}
}

func (s *Service) Start(ctx context.Context, name string) error {
	return s.containerAction(ctx, name, s.opts.Docker.Start)
}

func (s *Service) Stop(ctx context.Context, name string) error {
	return s.containerAction(ctx, name, s.opts.Docker.Stop)
}

func (s *Service) Restart(ctx context.Context, name string) error {
	return s.containerAction(ctx, name, s.opts.Docker.Restart)
}

func (s *Service) Unpause(ctx context.Context, name string) error {
	return s.containerAction(ctx, name, s.opts.Docker.Unpause)
}

func (s *Service) containerAction(ctx context.Context, name string, action func(context.Context, string) error) error {
	r, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	if r.ContainerID == "" {
		return ErrInProgress
	}
	return action(ctx, r.ContainerID)
}

func (s *Service) Logs(ctx context.Context, name string, opts dockerapi.LogOptions) (io.ReadCloser, error) {
	r, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if r.ContainerID == "" {
		return nil, ErrInProgress
	}
	return s.opts.Docker.Logs(ctx, r.ContainerID, opts)
}

func (s *Service) Wait() {
	s.wg.Wait()
}

func (s *Service) containerName(owner, name string) string {
	return s.opts.ContainerPrefix + "-" + owner + "-" + name
}

func (s *Service) hostDataDir(owner, name string) string {
	return s.opts.HostRoot + "/" + owner + "/" + name
}

func (s *Service) localDataDir(owner, name string) string {
	return filepath.Join(s.opts.LocalRoot, owner, name)
}

func fromContainer(c dockerapi.Container) Runner {
	var labels []string
	if raw := c.Labels[LabelLabels]; raw != "" {
		labels = strings.Split(raw, ",")
	}
	repo := c.Labels[LabelRepo]
	return Runner{
		Name:          c.Labels[LabelName],
		Owner:         c.Labels[LabelOwner],
		Repo:          repo,
		RepoURL:       "https://github.com/" + repo,
		ContainerName: c.Name,
		ContainerID:   c.ID,
		Image:         c.Image,
		State:         c.State,
		Status:        c.Status,
		Labels:        labels,
		Created:       c.Created,
		DataDir:       c.Labels[LabelDataDir],
	}
}
