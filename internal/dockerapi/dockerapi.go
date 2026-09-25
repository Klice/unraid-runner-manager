package dockerapi

import (
	"context"
	"io"
	"time"
)

type Container struct {
	ID      string
	Name    string
	Image   string
	ImageID string
	State   string
	Status  string
	Created time.Time
	Labels  map[string]string
}

type Bind struct {
	Source   string
	Target   string
	ReadOnly bool
}

type CreateSpec struct {
	Name          string
	Image         string
	Env           []string
	Labels        map[string]string
	Binds         []Bind
	PidsLimit     int64
	RestartAlways bool
}

type LogOptions struct {
	Tail   int
	Follow bool
}

type MountPoint struct {
	Source      string
	Destination string
}

type Client interface {
	Ping(ctx context.Context) error
	ListByLabel(ctx context.Context, label string) ([]Container, error)
	PullImage(ctx context.Context, image string) error
	ImageID(ctx context.Context, image string) (string, error)
	Create(ctx context.Context, spec CreateSpec) (string, error)
	Inspect(ctx context.Context, id string) (CreateSpec, error)
	Rename(ctx context.Context, id, name string) error
	Exec(ctx context.Context, id string, cmd []string) (int, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Restart(ctx context.Context, id string) error
	Unpause(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Logs(ctx context.Context, id string, opts LogOptions) (io.ReadCloser, error)
	Mounts(ctx context.Context, id string) ([]MountPoint, error)
	Close() error
}
