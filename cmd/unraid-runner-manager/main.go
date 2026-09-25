package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Klice/unraid-runner-manager/internal/config"
	"github.com/Klice/unraid-runner-manager/internal/dockerapi"
	"github.com/Klice/unraid-runner-manager/internal/runner"
	"github.com/Klice/unraid-runner-manager/internal/store"
	"github.com/Klice/unraid-runner-manager/internal/web"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Version = version
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	docker, err := dockerapi.NewMoby()
	if err != nil {
		return err
	}
	defer func() {
		if err := docker.Close(); err != nil {
			logger.Warn("closing docker client", "err", err)
		}
	}()
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := docker.Ping(pingCtx); err != nil {
		return fmt.Errorf("docker socket not reachable (is /var/run/docker.sock mounted?): %w", err)
	}
	if cfg.RunnerDataHostRoot == "" {
		cfg.RunnerDataHostRoot, err = detectHostRoot(pingCtx, docker, cfg.RunnerDataDir)
		if err != nil {
			return err
		}
		logger.Info("detected runner data host path", "path", cfg.RunnerDataHostRoot)
	}
	if err := os.MkdirAll(cfg.RunnerDataDir, 0o755); err != nil {
		return fmt.Errorf("runner data dir: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "runner-manager.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Warn("closing database", "err", err)
		}
	}()

	runners := runner.New(runner.Options{
		Docker:          docker,
		Image:           cfg.RunnerImage,
		Icon:            cfg.RunnerIcon,
		ContainerPrefix: cfg.ContainerPrefix,
		HostRoot:        cfg.RunnerDataHostRoot,
		LocalRoot:       cfg.RunnerDataDir,
		Hostname:        cfg.UnraidHostname,
		Timezone:        cfg.Timezone,
		Logger:          logger,
	})
	srv, err := web.New(cfg, st, runners, logger)
	if err != nil {
		return err
	}
	if err := srv.EnsureAdmin(ctx); err != nil {
		return err
	}
	go purgeSessions(ctx, st, logger)
	go runners.RunUpdater(ctx, cfg.UpdateInterval, time.Minute)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.ListenAddr, "version", version, "image", cfg.RunnerImage, "hostRoot", cfg.RunnerDataHostRoot, "updateEvery", cfg.UpdateInterval)
		errCh <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = httpServer.Shutdown(shutdownCtx)
	runners.Wait()
	return nil
}

func detectHostRoot(ctx context.Context, docker dockerapi.Client, target string) (string, error) {
	self, err := os.Hostname()
	if err != nil {
		return "", err
	}
	mounts, err := docker.Mounts(ctx, self)
	if err != nil {
		return "", fmt.Errorf("RUNNER_DATA_HOST_ROOT is not set and the container's own mounts could not be inspected: %w", err)
	}
	for _, m := range mounts {
		if m.Destination == target {
			return m.Source, nil
		}
	}
	return "", fmt.Errorf("RUNNER_DATA_HOST_ROOT is not set and nothing is mounted at %s", target)
}

func purgeSessions(ctx context.Context, st *store.Store, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := st.PurgeExpiredSessions(ctx); err != nil {
				logger.Warn("session purge failed", "err", err)
			}
		}
	}
}
