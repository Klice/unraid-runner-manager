package gitlab

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Concurrent    int            `toml:"concurrent"`
	CheckInterval int            `toml:"check_interval"`
	Runners       []RunnerConfig `toml:"runners"`
}

type RunnerConfig struct {
	Name            string       `toml:"name"`
	URL             string       `toml:"url"`
	ID              int64        `toml:"id"`
	Token           string       `toml:"token"`
	TokenObtainedAt time.Time    `toml:"token_obtained_at"`
	Executor        string       `toml:"executor"`
	Limit           int          `toml:"limit"`
	Docker          DockerConfig `toml:"docker"`
}

type DockerConfig struct {
	Image        string   `toml:"image"`
	Privileged   bool     `toml:"privileged"`
	DisableCache bool     `toml:"disable_cache"`
	Volumes      []string `toml:"volumes"`
	ShmSize      int64    `toml:"shm_size"`
}

func WriteConfig(path string, cfg Config) error {
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ReadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg, nil
}
