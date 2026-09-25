package dockerapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type Moby struct {
	cli *client.Client
}

func NewMoby() (*Moby, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Moby{cli: cli}, nil
}

func (m *Moby) Ping(ctx context.Context) error {
	_, err := m.cli.Ping(ctx, client.PingOptions{})
	return err
}

func (m *Moby) ListByLabel(ctx context.Context, label string) ([]Container, error) {
	res, err := m.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", label),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := make([]Container, 0, len(res.Items))
	for _, c := range res.Items {
		out = append(out, Container{
			ID:      c.ID,
			Name:    primaryName(c.Names),
			Image:   c.Image,
			ImageID: c.ImageID,
			State:   string(c.State),
			Status:  c.Status,
			Created: time.Unix(c.Created, 0),
			Labels:  c.Labels,
		})
	}
	return out, nil
}

func primaryName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func (m *Moby) PullImage(ctx context.Context, image string) error {
	resp, err := m.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	waitErr := resp.Wait(ctx)
	closeErr := resp.Close()
	if err := errors.Join(waitErr, closeErr); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

func (m *Moby) ImageID(ctx context.Context, image string) (string, error) {
	res, err := m.cli.ImageInspect(ctx, image)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", image, err)
	}
	return res.ID, nil
}

func (m *Moby) Create(ctx context.Context, spec CreateSpec) (string, error) {
	binds := make([]string, 0, len(spec.Binds))
	for _, b := range spec.Binds {
		mode := "rw"
		if b.ReadOnly {
			mode = "ro"
		}
		binds = append(binds, b.Source+":"+b.Target+":"+mode)
	}
	hostConfig := &container.HostConfig{
		NetworkMode: "bridge",
		Binds:       binds,
	}
	if spec.PidsLimit > 0 {
		hostConfig.PidsLimit = &spec.PidsLimit
	}
	if spec.RestartAlways {
		hostConfig.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyAlways}
	}
	res, err := m.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: spec.Name,
		Config: &container.Config{
			Image:  spec.Image,
			Env:    spec.Env,
			Labels: spec.Labels,
		},
		HostConfig: hostConfig,
	})
	if err != nil {
		return "", fmt.Errorf("create %s: %w", spec.Name, err)
	}
	return res.ID, nil
}

func (m *Moby) Inspect(ctx context.Context, id string) (CreateSpec, error) {
	res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return CreateSpec{}, fmt.Errorf("inspect %s: %w", id, err)
	}
	c := res.Container
	spec := CreateSpec{Name: strings.TrimPrefix(c.Name, "/")}
	if c.Config != nil {
		spec.Image = c.Config.Image
		spec.Env = c.Config.Env
		spec.Labels = c.Config.Labels
	}
	if c.HostConfig != nil {
		spec.RestartAlways = c.HostConfig.RestartPolicy.IsAlways()
		if c.HostConfig.PidsLimit != nil {
			spec.PidsLimit = *c.HostConfig.PidsLimit
		}
		for _, raw := range c.HostConfig.Binds {
			spec.Binds = append(spec.Binds, parseBind(raw))
		}
	}
	return spec, nil
}

func parseBind(raw string) Bind {
	parts := strings.Split(raw, ":")
	b := Bind{Source: parts[0]}
	if len(parts) > 1 {
		b.Target = parts[1]
	}
	if len(parts) > 2 {
		b.ReadOnly = parts[2] == "ro"
	}
	return b
}

func (m *Moby) Rename(ctx context.Context, id, name string) error {
	_, err := m.cli.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: name})
	return err
}

func (m *Moby) Exec(ctx context.Context, id string, cmd []string) (int, error) {
	created, err := m.cli.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: cmd})
	if err != nil {
		return 0, fmt.Errorf("exec create: %w", err)
	}
	if _, err := m.cli.ExecStart(ctx, created.ID, client.ExecStartOptions{Detach: true}); err != nil {
		return 0, fmt.Errorf("exec start: %w", err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := m.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return 0, fmt.Errorf("exec inspect: %w", err)
		}
		if !info.Running {
			return info.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Moby) Start(ctx context.Context, id string) error {
	_, err := m.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

func (m *Moby) Stop(ctx context.Context, id string) error {
	_, err := m.cli.ContainerStop(ctx, id, client.ContainerStopOptions{})
	return err
}

func (m *Moby) Restart(ctx context.Context, id string) error {
	_, err := m.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
	return err
}

func (m *Moby) Unpause(ctx context.Context, id string) error {
	_, err := m.cli.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	return err
}

func (m *Moby) Remove(ctx context.Context, id string) error {
	_, err := m.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	return err
}

func (m *Moby) Logs(ctx context.Context, id string, opts LogOptions) (io.ReadCloser, error) {
	tail := "all"
	if opts.Tail > 0 {
		tail = strconv.Itoa(opts.Tail)
	}
	raw, err := m.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     opts.Follow,
		Tail:       tail,
	})
	if err != nil {
		return nil, fmt.Errorf("logs: %w", err)
	}
	pr, pw := io.Pipe()
	go func() {
		_, copyErr := stdcopy.StdCopy(pw, pw, raw)
		pw.CloseWithError(errors.Join(copyErr, raw.Close()))
	}()
	return &pipeCloser{PipeReader: pr, raw: raw}, nil
}

type pipeCloser struct {
	*io.PipeReader
	raw io.Closer
}

func (p *pipeCloser) Close() error {
	return errors.Join(p.raw.Close(), p.PipeReader.Close())
}

func (m *Moby) Mounts(ctx context.Context, id string) ([]MountPoint, error) {
	res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", id, err)
	}
	out := make([]MountPoint, 0, len(res.Container.Mounts))
	for _, mp := range res.Container.Mounts {
		out = append(out, MountPoint{Source: mp.Source, Destination: mp.Destination})
	}
	return out, nil
}

func (m *Moby) Close() error {
	return m.cli.Close()
}
