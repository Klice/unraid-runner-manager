package dockerfake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/dockerapi"
)

type Record struct {
	dockerapi.Container
	Spec dockerapi.CreateSpec
}

type Fake struct {
	mu         sync.Mutex
	seq        int
	containers map[string]*Record
	Pulled     []string
	LogText    map[string]string
	PullErr    error
	CreateErr  error
	StartErr   error
	PullDelay  time.Duration
	Mount      []dockerapi.MountPoint
}

var ErrNotFound = errors.New("no such container")

func New() *Fake {
	return &Fake{containers: map[string]*Record{}, LogText: map[string]string{}}
}

func (f *Fake) Ping(context.Context) error { return nil }

func (f *Fake) ListByLabel(_ context.Context, label string) ([]dockerapi.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, want, hasValue := strings.Cut(label, "=")
	var out []dockerapi.Container
	for _, r := range f.containers {
		v, ok := r.Labels[key]
		if !ok || (hasValue && v != want) {
			continue
		}
		out = append(out, r.Container)
	}
	slices.SortFunc(out, func(a, b dockerapi.Container) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (f *Fake) PullImage(ctx context.Context, image string) error {
	if f.PullDelay > 0 {
		select {
		case <-time.After(f.PullDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PullErr != nil {
		return f.PullErr
	}
	f.Pulled = append(f.Pulled, image)
	return nil
}

func (f *Fake) Create(_ context.Context, spec dockerapi.CreateSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	for _, r := range f.containers {
		if r.Name == spec.Name {
			return "", fmt.Errorf("conflict: name %q already in use", spec.Name)
		}
	}
	f.seq++
	id := fmt.Sprintf("c%03d", f.seq)
	f.containers[id] = &Record{
		ID:      id,
		Name:    spec.Name,
		Image:   spec.Image,
		State:   "created",
		Status:  "Created",
		Created: time.Now(),
		Labels:  spec.Labels,
		Spec:    spec,
	}
	return id, nil
}

func (f *Fake) setState(id, state, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.containers[id]
	if !ok {
		return ErrNotFound
	}
	r.State = state
	r.Status = status
	return nil
}

func (f *Fake) Start(_ context.Context, id string) error {
	if f.StartErr != nil {
		return f.StartErr
	}
	return f.setState(id, "running", "Up 1 second")
}

func (f *Fake) Stop(_ context.Context, id string) error {
	return f.setState(id, "exited", "Exited (0) 1 second ago")
}

func (f *Fake) Restart(_ context.Context, id string) error {
	return f.setState(id, "running", "Up 1 second")
}

func (f *Fake) Unpause(_ context.Context, id string) error {
	return f.setState(id, "running", "Up 2 days")
}

func (f *Fake) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[id]; !ok {
		return ErrNotFound
	}
	delete(f.containers, id)
	return nil
}

func (f *Fake) Logs(_ context.Context, id string, opts dockerapi.LogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[id]; !ok {
		return nil, ErrNotFound
	}
	text := f.LogText[id]
	if opts.Tail > 0 {
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) > opts.Tail {
			lines = lines[len(lines)-opts.Tail:]
		}
		text = strings.Join(lines, "\n") + "\n"
	}
	return io.NopCloser(strings.NewReader(text)), nil
}

func (f *Fake) Mounts(context.Context, string) ([]dockerapi.MountPoint, error) {
	return f.Mount, nil
}

func (f *Fake) Close() error { return nil }

func (f *Fake) Get(name string) (Record, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.containers {
		if r.Name == name {
			return *r, true
		}
	}
	return Record{}, false
}

func (f *Fake) SetState(name, state, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.containers {
		if r.Name == name {
			r.State = state
			r.Status = status
		}
	}
}

func (f *Fake) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.containers)
}
