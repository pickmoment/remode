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
	"sync"
	"syscall"
	"time"

	"github.com/pickmoment/remode/internal/agent/claudecode"
	"github.com/pickmoment/remode/internal/agent/codex"
	"github.com/pickmoment/remode/internal/config"
	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/orchestrator"
	"github.com/pickmoment/remode/internal/platform/discord"
	"github.com/pickmoment/remode/internal/platform/telegram"
	"github.com/pickmoment/remode/internal/platform/web"
	"github.com/pickmoment/remode/internal/scheduler"
	"github.com/pickmoment/remode/internal/session"
	"github.com/pickmoment/remode/internal/store/sqlite"
)

// botDriver is the common interface implemented by telegram.BotInstance and discord.BotInstance.
type botDriver interface {
	Run(ctx context.Context, sm *session.Manager) error
}

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
			agents["claude_code"] = claudecode.NewWithPollAndGrace(
				time.Duration(cfg.Monitor.PlanBannerPollMS)*time.Millisecond,
				time.Duration(cfg.Monitor.DialogGraceMS)*time.Millisecond,
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

	// Root context: outlives any individual chat platform driver.
	// Session task goroutines are parented here so shutting down a bot driver
	// does not orphan web or scheduler sessions.
	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Build session manager (no platform yet; platforms are registered below)
	mgr := session.New(session.Config{
		TmuxSessionPrefix: cfg.Tmux.SessionPrefix,
		SessionsDir:       cfg.Paths.SessionsDir,
		ClaudeProjectsDir: cfg.Paths.ClaudeProjectsDir,
		NewProjectDir:     cfg.Paths.NewProjectDir,
		MessageLevel:      cfg.Monitor.MessageLevel,
		JSONLSettleMS:     cfg.Monitor.JSONLSettleMS,
	}, store, agents, rootCtx)

	// Backfill transport for sessions created before transport tracking was added.
	if err := store.BackfillTransport(cfg.Platform); err != nil {
		log.Printf("backfill transport: %v", err)
	}

	// ── Option A: construct all drivers synchronously → register platforms →
	//             Startup → launch blocking Run loops in goroutines ──────────
	//
	// This guarantees all platforms are registered before Startup tries to
	// send notifications for restored sessions.

	var driver botDriver

	switch cfg.Platform {
	case "telegram":
		if cfg.Telegram.Token == "" {
			log.Fatal("telegram.token is required")
		}
		bot, err := telegram.NewBot(telegram.RunConfig{
			Token:          cfg.Telegram.Token,
			AllowedUserIDs: cfg.Telegram.AllowedUserIDs,
			NewProjectDir:  cfg.Paths.NewProjectDir,
		})
		if err != nil {
			log.Fatalf("telegram init: %v", err)
		}
		mgr.RegisterPlatform("telegram", bot.ChatPlatform())
		driver = bot

	case "discord":
		if cfg.Discord.Token == "" {
			log.Fatal("discord.token is required")
		}
		bot, err := discord.NewBot(discord.RunConfig{
			Token:            cfg.Discord.Token,
			GuildID:          cfg.Discord.GuildID,
			AllowedUserIDs:   cfg.Discord.AllowedUserIDs,
			NotifyChannelIDs: cfg.Discord.NotifyChannelIDs,
			NewProjectDir:    cfg.Paths.NewProjectDir,
		})
		if err != nil {
			log.Fatalf("discord init: %v", err)
		}
		mgr.RegisterPlatform("discord", bot.ChatPlatform())
		driver = bot

	default:
		log.Fatalf("unsupported platform: %q (supported: telegram, discord)", cfg.Platform)
	}

	// Restore persisted sessions (all platforms already registered above).
	if err := mgr.Startup(rootCtx); err != nil {
		log.Printf("startup error: %v", err)
	}

	// ── Phase 4: TurnTracker + Orchestrator ──────────────────────────────────
	tracker := orchestrator.NewTurnTracker(cfg.Monitor.TurnIdleMS, nil)
	mgr.RegisterObserver(tracker.OnEvent)

	dagEngine := orchestrator.NewDAGEngine(store, mgr, tracker)
	orc := orchestrator.New(mgr, tracker)

	// TurnTracker tick loop — promotes ACTIVE→IDLE after silence.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tracker.Run(rootCtx)
	}()

	// Resume any persisted "running" workflow runs after Startup.
	if err := dagEngine.ResumeRuns(rootCtx); err != nil {
		log.Printf("dag resume: %v", err)
	}

	// ── Phase 3: Scheduler ────────────────────────────────────────────────────
	sched := scheduler.New(store, mgr, nil)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := sched.Start(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("scheduler error: %v", err)
		}
	}()

	// ── Launch the chat bot driver ────────────────────────────────────────────
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := driver.Run(rootCtx, mgr); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("bot error: %v", err)
			cancel()
		}
	}()

	// ── Phase 2: web management service ──────────────────────────────────────
	if cfg.Web.Enabled {
		webPlatform := web.New()
		mgr.RegisterPlatform("web", webPlatform)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := web.Run(rootCtx, web.RunConfig{
				ListenAddr:    cfg.Web.ListenAddr,
				AuthToken:     cfg.Web.AuthToken,
				NewProjectDir: cfg.Paths.NewProjectDir,
			}, mgr, webPlatform, sched, dagEngine, orc); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("web server error: %v", err)
				cancel()
			}
		}()
	}

	wg.Wait()
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
