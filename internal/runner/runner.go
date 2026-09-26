package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/dockerapi"
	"github.com/Klice/unraid-runner-manager/internal/names"
	"github.com/Klice/unraid-runner-manager/internal/provider"
)

const (
	LabelManaged  = "runner-manager.managed"
	LabelOwner    = "runner-manager.owner"
	LabelName     = "runner-manager.name"
	LabelRepo     = "runner-manager.repo"
	LabelURL      = "runner-manager.url"
	LabelProvider = "runner-manager.provider"
	LabelLabels   = "runner-manager.labels"
	LabelJobs     = "runner-manager.concurrency"
	LabelDataDir  = "runner-manager.data"

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
	ErrDeregister = errors.New("runner was removed locally but not on the server")
)

type Runner struct {
	Name          string
	Owner         string
	Provider      provider.Kind
	Repo          string
	RepoURL       string
	ContainerName string
	ContainerID   string
	Image         string
	State         string
	Status        string
	Labels        []string
	Concurrency   int
	Created       time.Time
	DataDir       string
	Error         string
}

func (r Runner) Pending() bool { return r.State == StateCreating || r.State == StateFailed }
func (r Runner) Running() bool { return r.State == StateRunning }
func (r Runner) Paused() bool  { return r.State == StatePaused }
func (r Runner) IsGitLab() bool {
	return r.Provider == provider.GitLab
}

type Options struct {
	Docker    dockerapi.Client
	Providers provider.Registry
	Names     *names.Generator
	HostRoot  string
	LocalRoot string
	Logger    *slog.Logger
	Now       func() time.Time
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

func (s *Service) Provider(kind provider.Kind) (provider.Provider, error) {
	return s.opts.Providers.Get(kind)
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
	Owner       string
	Provider    provider.Kind
	Target      string
	Token       string
	Labels      []string
	Concurrency int
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (Runner, error) {
	prov, err := s.Provider(req.Provider)
	if err != nil {
		return Runner{}, err
	}
	target, err := prov.ParseTarget(req.Target)
	if err != nil {
		return Runner{}, err
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		return Runner{}, errors.New("runner token is required")
	}
	if !prov.SupportsLabels() && len(req.Labels) > 0 {
		return Runner{}, fmt.Errorf("%s runners take their tags from the %s UI, not from this form", prov.Kind().Title(), prov.Kind().Title())
	}
	concurrency, err := resolveConcurrency(prov, req.Concurrency)
	if err != nil {
		return Runner{}, err
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
		Provider:      prov.Kind(),
		Repo:          target.Display,
		RepoURL:       target.URL,
		ContainerName: prov.ContainerPrefix() + "-" + req.Owner + "-" + name,
		Image:         prov.Image(),
		State:         StateCreating,
		Status:        "Preparing data folder",
		Labels:        req.Labels,
		Concurrency:   concurrency,
		Created:       s.opts.Now(),
		DataDir:       s.hostDataDir(req.Owner, name),
	}
	s.pending[name] = r
	snapshot := *r
	s.wg.Go(func() { s.provision(prov, snapshot, token) })
	return snapshot, nil
}

func resolveConcurrency(prov provider.Provider, requested int) (int, error) {
	if requested == 0 {
		return 1, nil
	}
	if !prov.SupportsConcurrency() && requested != 1 {
		return 0, fmt.Errorf("%s runners run one job at a time; create more runners instead", prov.Kind().Title())
	}
	if requested < 1 || requested > provider.MaxConcurrency {
		return 0, fmt.Errorf("concurrent jobs must be between 1 and %d", provider.MaxConcurrency)
	}
	return requested, nil
}

func (s *Service) provision(prov provider.Provider, r Runner, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	log := s.opts.Logger.With("runner", r.Name, "owner", r.Owner, "provider", r.Provider)
	stage, err := s.runProvisionSteps(ctx, prov, r, token)
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

func (s *Service) runProvisionSteps(ctx context.Context, prov provider.Provider, r Runner, token string) (string, error) {
	pr := s.providerRunner(r)
	if err := os.MkdirAll(filepath.Join(pr.LocalDataDir, "work"), 0o755); err != nil {
		return "creating data folder", err
	}
	s.setPendingStatus(r.Name, "Registering with "+prov.Kind().Title())
	if err := prov.Prepare(ctx, pr, token); err != nil {
		return "registering with " + prov.Kind().Title(), err
	}
	s.setPendingStatus(r.Name, "Pulling image")
	if err := s.opts.Docker.PullImage(ctx, prov.Image()); err != nil {
		return "pulling image", err
	}
	s.setPendingStatus(r.Name, "Creating container")
	id, err := s.opts.Docker.Create(ctx, s.spec(prov, r, pr, token))
	if err != nil {
		return "creating container", err
	}
	s.setPendingStatus(r.Name, "Starting container")
	if err := s.opts.Docker.Start(ctx, id); err != nil {
		return "starting container", errors.Join(err, s.opts.Docker.Remove(ctx, id))
	}
	return "", nil
}

func (s *Service) providerRunner(r Runner) provider.Runner {
	return provider.Runner{
		Name:          r.Name,
		Owner:         r.Owner,
		ContainerName: r.ContainerName,
		URL:           r.RepoURL,
		Display:       r.Repo,
		Labels:        r.Labels,
		Concurrency:   r.Concurrency,
		HostDataDir:   r.DataDir,
		LocalDataDir:  s.localDataDir(r.Owner, r.Name),
	}
}

func (s *Service) spec(prov provider.Provider, r Runner, pr provider.Runner, token string) dockerapi.CreateSpec {
	spec := prov.Spec(pr, token)
	labels := map[string]string{
		LabelManaged:  "true",
		LabelOwner:    r.Owner,
		LabelName:     r.Name,
		LabelProvider: string(r.Provider),
		LabelRepo:     r.Repo,
		LabelURL:      r.RepoURL,
		LabelLabels:   strings.Join(r.Labels, ","),
		LabelJobs:     strconv.Itoa(r.Concurrency),
		LabelDataDir:  r.DataDir,
	}
	maps.Copy(labels, spec.Labels)
	spec.Labels = labels
	return spec
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

func (s *Service) Delete(ctx context.Context, name string) error {
	r, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	if r.State == StateCreating {
		return ErrInProgress
	}
	var deregisterErr error
	if r.ContainerID != "" {
		prov, err := s.Provider(r.Provider)
		if err != nil {
			return err
		}
		if err := prov.Deregister(ctx, s.providerRunner(r)); err != nil {
			s.opts.Logger.Warn("deregistration failed", "runner", name, "provider", r.Provider, "err", err)
			deregisterErr = fmt.Errorf("%w: %w", ErrDeregister, err)
		}
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
	s.opts.Logger.Info("runner deleted", "runner", name, "owner", r.Owner, "provider", r.Provider)
	return deregisterErr
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
	kind, err := provider.ParseKind(c.Labels[LabelProvider])
	if err != nil {
		kind = provider.GitHub
	}
	concurrency, err := strconv.Atoi(c.Labels[LabelJobs])
	if err != nil || concurrency < 1 {
		concurrency = 1
	}
	repo := c.Labels[LabelRepo]
	repoURL := c.Labels[LabelURL]
	if repoURL == "" {
		repoURL = "https://github.com/" + repo
	}
	return Runner{
		Name:          c.Labels[LabelName],
		Owner:         c.Labels[LabelOwner],
		Provider:      kind,
		Repo:          repo,
		RepoURL:       repoURL,
		ContainerName: c.Name,
		ContainerID:   c.ID,
		Image:         c.Image,
		State:         c.State,
		Status:        c.Status,
		Labels:        labels,
		Concurrency:   concurrency,
		Created:       c.Created,
		DataDir:       c.Labels[LabelDataDir],
	}
}
