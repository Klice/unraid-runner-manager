package runner

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	StateUpdating = "updating"

	busyRetryInterval = 10 * time.Minute
	updateTimeout     = 30 * time.Minute
	oldNameSuffix     = "-old"
)

var busyProbe = []string{"sh", "-c", "grep -qa 'Runner[.]Worker' /proc/[0-9]*/cmdline"}

type UpdateReport struct {
	CheckedAt time.Time
	ImageID   string
	Updated   []string
	Deferred  []string
	Failed    map[string]string
	Error     string
}

func (r UpdateReport) Summary() string {
	switch {
	case r.Error != "":
		return "failed: " + r.Error
	case len(r.Updated) == 0 && len(r.Deferred) == 0 && len(r.Failed) == 0:
		return "all runners up to date"
	default:
		return fmt.Sprintf("%d updated, %d waiting for a job to finish, %d failed", len(r.Updated), len(r.Deferred), len(r.Failed))
	}
}

func (s *Service) LastUpdate() (UpdateReport, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUpdate, s.hasUpdate
}

func (s *Service) CheckUpdates(ctx context.Context) UpdateReport {
	report := UpdateReport{CheckedAt: s.opts.Now(), Failed: map[string]string{}}
	defer s.recordUpdate(&report)
	if err := s.opts.Docker.PullImage(ctx, s.opts.Image); err != nil {
		report.Error = err.Error()
		return report
	}
	latest, err := s.opts.Docker.ImageID(ctx, s.opts.Image)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.ImageID = latest
	containers, err := s.opts.Docker.ListByLabel(ctx, LabelManaged+"=true")
	if err != nil {
		report.Error = err.Error()
		return report
	}
	for _, c := range containers {
		name := c.Labels[LabelName]
		if c.ImageID == latest {
			continue
		}
		busy, err := s.runnerBusy(ctx, c.ID, c.State)
		if err != nil {
			report.Failed[name] = "could not check for a running job: " + err.Error()
			continue
		}
		if busy {
			report.Deferred = append(report.Deferred, name)
			continue
		}
		if err := s.recreate(ctx, name, c.ID, c.State == StateRunning); err != nil {
			s.opts.Logger.Error("runner update failed", "runner", name, "err", err)
			report.Failed[name] = err.Error()
			continue
		}
		s.opts.Logger.Info("runner updated", "runner", name, "image", latest)
		report.Updated = append(report.Updated, name)
	}
	return report
}

func (s *Service) recordUpdate(report *UpdateReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUpdate = *report
	s.hasUpdate = true
}

func (s *Service) runnerBusy(ctx context.Context, id, state string) (bool, error) {
	if state != StateRunning {
		return false, nil
	}
	code, err := s.opts.Docker.Exec(ctx, id, busyProbe)
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

func (s *Service) recreate(ctx context.Context, name, oldID string, wasRunning bool) error {
	spec, err := s.opts.Docker.Inspect(ctx, oldID)
	if err != nil {
		return err
	}
	spec.Image = s.opts.Image
	s.setUpdating(name, true)
	defer s.setUpdating(name, false)
	if wasRunning {
		if err := s.opts.Docker.Stop(ctx, oldID); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}
	if err := s.opts.Docker.Rename(ctx, oldID, spec.Name+oldNameSuffix); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	newID, err := s.opts.Docker.Create(ctx, spec)
	if err != nil {
		return errors.Join(fmt.Errorf("create: %w", err), s.rollback(ctx, oldID, spec.Name, wasRunning))
	}
	if wasRunning {
		if err := s.opts.Docker.Start(ctx, newID); err != nil {
			return errors.Join(fmt.Errorf("start: %w", err), s.opts.Docker.Remove(ctx, newID), s.rollback(ctx, oldID, spec.Name, wasRunning))
		}
	}
	if err := s.opts.Docker.Remove(ctx, oldID); err != nil {
		return fmt.Errorf("remove old container: %w", err)
	}
	return nil
}

func (s *Service) rollback(ctx context.Context, oldID, name string, wasRunning bool) error {
	if err := s.opts.Docker.Rename(ctx, oldID, name); err != nil {
		return fmt.Errorf("rollback rename: %w", err)
	}
	if wasRunning {
		if err := s.opts.Docker.Start(ctx, oldID); err != nil {
			return fmt.Errorf("rollback start: %w", err)
		}
	}
	return nil
}

func (s *Service) setUpdating(name string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.updating[name] = true
		return
	}
	delete(s.updating, name)
}

func (s *Service) RunUpdater(ctx context.Context, interval, initialDelay time.Duration) {
	if interval <= 0 {
		return
	}
	wait := initialDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		checkCtx, cancel := context.WithTimeout(ctx, updateTimeout)
		report := s.CheckUpdates(checkCtx)
		cancel()
		s.opts.Logger.Info("runner image update check", "result", report.Summary())
		wait = interval
		if len(report.Deferred) > 0 {
			wait = min(interval, busyRetryInterval)
		}
	}
}
