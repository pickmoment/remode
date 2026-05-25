package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pickmoment/remode/internal/agent/claudecode"
	"github.com/pickmoment/remode/internal/agent/codex"
	"github.com/pickmoment/remode/internal/config"
	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/platform/discord"
	"github.com/pickmoment/remode/internal/platform/telegram"
	"github.com/pickmoment/remode/internal/session"
	"github.com/pickmoment/remode/internal/store/sqlite"
)

func main() {
	cfgPath := flag.String("config", config.DefaultConfigPath(), "path to config.toml")
	tmuxFlag := flag.Bool("tmux", false, "run inside a tmux session named 'remode'")
	flag.Parse()

	if *tmuxFlag && os.Getenv("REMODE_IN_TMUX") == "" {
		runInTmux("remode")
		os.Exit(0)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Fatalf("load config: %v", err)
		}
		if setupErr := config.Setup(*cfgPath); setupErr != nil {
			log.Fatalf("setup: %v", setupErr)
		}
		cfg, err = config.Load(*cfgPath)
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
	}

	// Ensure required directories exist
	for _, dir := range []string{cfg.Paths.SessionsDir, cfg.Paths.NewProjectDir, filepath.Dir(cfg.Paths.DB)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("create dir %s: %v", dir, err)
		}
	}

	// Open database
	store, err := sqlite.Open(cfg.Paths.DB)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Build agents map
	agents := make(map[string]core.AIAgent)
	for _, aType := range cfg.Agents.Enabled {
		switch aType {
		case "claude_code":
			agents["claude_code"] = claudecode.NewWithPoll(
				time.Duration(cfg.Monitor.PlanBannerPollMS) * time.Millisecond,
			)
		case "codex":
			agents["codex"] = codex.New()
		default:
			log.Printf("unknown agent type %q, skipping", aType)
		}
	}
	if len(agents) == 0 {
		log.Fatal("no agents enabled — add 'claude_code' to [agents] enabled list")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Build session manager (platform injected by telegram.Run before startup)
	mgr := session.New(session.Config{
		TmuxSessionPrefix: cfg.Tmux.SessionPrefix,
		SessionsDir:       cfg.Paths.SessionsDir,
		ClaudeProjectsDir: cfg.Paths.ClaudeProjectsDir,
		NewProjectDir:     cfg.Paths.NewProjectDir,
		MessageLevel:      cfg.Monitor.MessageLevel,
		JSONLSettleMS:     cfg.Monitor.JSONLSettleMS,
	}, store, agents, nil)

	switch cfg.Platform {
	case "telegram":
		if cfg.Telegram.Token == "" {
			log.Fatal("telegram.token is required")
		}
		runCfg := telegram.RunConfig{
			Token:          cfg.Telegram.Token,
			AllowedUserIDs: cfg.Telegram.AllowedUserIDs,
			NewProjectDir:  cfg.Paths.NewProjectDir,
		}
		if err := telegram.Run(ctx, runCfg, mgr, mgr.SetPlatform); err != nil && err != context.Canceled {
			log.Fatalf("telegram bot error: %v", err)
		}
	case "discord":
		if cfg.Discord.Token == "" {
			log.Fatal("discord.token is required")
		}
		runCfg := discord.RunConfig{
			Token:            cfg.Discord.Token,
			AllowedUserIDs:   cfg.Discord.AllowedUserIDs,
			NotifyChannelIDs: cfg.Discord.NotifyChannelIDs,
			NewProjectDir:    cfg.Paths.NewProjectDir,
		}
		if err := discord.Run(ctx, runCfg, mgr, mgr.SetPlatform); err != nil && err != context.Canceled {
			log.Fatalf("discord bot error: %v", err)
		}
	default:
		log.Fatalf("unsupported platform: %q (supported: telegram, discord)", cfg.Platform)
	}
}

// runInTmux starts remode in a detached tmux session named `name`.
// Does nothing if the session already exists.
func runInTmux(name string) {
	if exec.Command("tmux", "has-session", "-t", name).Run() == nil {
		log.Printf("tmux session %q already running", name)
		return
	}

	self, err := os.Executable()
	if err != nil {
		log.Fatalf("runInTmux: %v", err)
	}

	// Rebuild args without the --tmux / -tmux flag.
	var inner []string
	for _, a := range os.Args[1:] {
		if a == "--tmux" || a == "-tmux" ||
			strings.HasPrefix(a, "--tmux=") || strings.HasPrefix(a, "-tmux=") {
			continue
		}
		inner = append(inner, a)
	}

	// Create a detached session and run self inside it.
	// Use `env` so the env var is set properly rather than treated as a command name.
	cmd := append([]string{"env", "REMODE_IN_TMUX=1", self}, inner...)
	args := append([]string{"new-session", "-d", "-s", name}, cmd...)
	if err := exec.Command("tmux", args...).Run(); err != nil {
		log.Fatalf("tmux new-session: %v", err)
	}
	log.Printf("remode started in tmux session %q", name)
}
