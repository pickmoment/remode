// Package config loads remode configuration from a TOML file.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration structure.
type Config struct {
	Platform string         `toml:"platform"`
	Telegram TelegramConfig `toml:"telegram"`
	Discord  DiscordConfig  `toml:"discord"`
	Paths    PathsConfig    `toml:"paths"`
	Tmux     TmuxConfig     `toml:"tmux"`
	Monitor  MonitorConfig  `toml:"monitor"`
	Agents   AgentsConfig   `toml:"agents"`
}

type TelegramConfig struct {
	Token          string  `toml:"token"`
	AllowedUserIDs []int64 `toml:"allowed_user_ids"`
}

type DiscordConfig struct {
	Token             string  `toml:"token"`
	AllowedUserIDs    []int64 `toml:"allowed_user_ids"`
	NotifyChannelIDs  []int64 `toml:"notify_channel_ids"`
}

type PathsConfig struct {
	DB                string `toml:"db"`
	NewProjectDir     string `toml:"new_project_dir"`
	ClaudeProjectsDir string `toml:"claude_projects_dir"`
	SessionsDir       string `toml:"sessions_dir"`
}

type TmuxConfig struct {
	SessionPrefix string `toml:"session_prefix"`
}

type MonitorConfig struct {
	PlanBannerPollMS int    `toml:"plan_banner_poll_ms"`
	JSONLSettleMS    int    `toml:"jsonl_settle_ms"`
	MessageLevel     string `toml:"message_level"`
}

type AgentsConfig struct {
	Enabled []string `toml:"enabled"`
}

// DefaultConfigPath returns the default path for the config file.
func DefaultConfigPath() string {
	if v := os.Getenv("REMODE_CONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".remode", "config.toml")
}

// Load reads the TOML config file at path and fills in defaults.
func Load(path string) (*Config, error) {
	cfg := defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	expand(cfg)
	return cfg, nil
}

func defaults() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Platform: "telegram",
		Paths: PathsConfig{
			DB:                filepath.Join(home, ".remode", "sessions.db"),
			NewProjectDir:     filepath.Join(home, "projects"),
			ClaudeProjectsDir: filepath.Join(home, ".claude", "projects"),
			SessionsDir:       filepath.Join(home, ".remode", "sessions"),
		},
		Tmux: TmuxConfig{
			SessionPrefix: "R-",
		},
		Monitor: MonitorConfig{
			PlanBannerPollMS: 500,
			JSONLSettleMS:    100,
			MessageLevel:     "interactive",
		},
		Agents: AgentsConfig{
			Enabled: []string{"claude_code"},
		},
	}
}

// expand resolves ~ in all path fields.
func expand(cfg *Config) {
	cfg.Paths.DB = expandHome(cfg.Paths.DB)
	cfg.Paths.NewProjectDir = expandHome(cfg.Paths.NewProjectDir)
	cfg.Paths.ClaudeProjectsDir = expandHome(cfg.Paths.ClaudeProjectsDir)
	cfg.Paths.SessionsDir = expandHome(cfg.Paths.SessionsDir)
}

func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
