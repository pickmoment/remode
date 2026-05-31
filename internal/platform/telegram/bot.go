package telegram

import (
	"context"
	"log"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/session"
)

// RunConfig holds all parameters needed to start the Telegram bot.
type RunConfig struct {
	Token          string
	AllowedUserIDs []int64
	NewProjectDir  string
}

// BotInstance holds a constructed Telegram bot instance ready to Run.
// Construct with NewBot, register ChatPlatform() with the session manager,
// then call Run after sm.Startup.
type BotInstance struct {
	platform *Platform
	bot      *tgbotapi.BotAPI
	cfg      RunConfig
}

// NewBot creates the Telegram BotAPI connection and platform.
// Returns an instance whose ChatPlatform() can be registered with the Manager.
func NewBot(cfg RunConfig) (*BotInstance, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		return nil, err
	}
	log.Printf("Telegram bot authorised as @%s", bot.Self.UserName)
	return &BotInstance{
		platform: NewPlatform(bot),
		bot:      bot,
		cfg:      cfg,
	}, nil
}

// ChatPlatform returns the core.ChatPlatform for registration with the Manager.
func (b *BotInstance) ChatPlatform() core.ChatPlatform { return b.platform }

// Run starts the Telegram update loop. Must be called after sm.Startup.
// Blocks until ctx is cancelled or /shutdown is received.
func (b *BotInstance) Run(ctx context.Context, sm *session.Manager) error {
	stopCh := make(chan struct{})
	bd := &BotData{
		SM:  sm,
		Bot: b.bot,
		Config: handlerConfig{
			AllowedUserIDs: b.cfg.AllowedUserIDs,
			NewProjectDir:  b.cfg.NewProjectDir,
		},
		StopCh: stopCh,
	}

	// Notify telegram sessions that the bot restarted.
	go notifyStartup(ctx, b.platform, sm)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stopCh:
			log.Println("shutdown requested via bot")
			return nil
		case upd, ok := <-updates:
			if !ok {
				return nil
			}
			if upd.Message != nil {
				if !isAllowed(upd.Message.From.ID, bd.Config.AllowedUserIDs) {
					continue
				}
				handleUpdate(ctx, upd, bd)
			} else if upd.CallbackQuery != nil {
				if !isAllowed(upd.CallbackQuery.From.ID, bd.Config.AllowedUserIDs) {
					continue
				}
				handleCallback(ctx, upd, bd)
			}
		}
	}
}

func handleUpdate(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	msg := upd.Message
	if !msg.IsCommand() {
		handleText(ctx, upd, bd)
		return
	}
	cmd := msg.Command()
	args := parseArgs(msg.CommandArguments())
	switch cmd {
	case "start":
		cmdStart(ctx, upd, bd)
	case "new":
		cmdNew(ctx, upd, bd, args)
	case "projects":
		cmdProjects(ctx, upd, bd)
	case "attach":
		cmdAttach(ctx, upd, bd, args)
	case "list":
		cmdList(ctx, upd, bd)
	case "kill":
		cmdKill(ctx, upd, bd, args)
	case "status":
		cmdStatus(ctx, upd, bd)
	case "send":
		cmdSend(ctx, upd, bd, args)
	case "level":
		cmdLevel(ctx, upd, bd, args)
	case "resend":
		cmdResend(ctx, upd, bd)
	case "screen":
		cmdScreen(ctx, upd, bd)
	case "clear":
		cmdClear(ctx, upd, bd)
	case "sessions":
		cmdSessions(ctx, upd, bd)
	case "shutdown":
		cmdShutdown(ctx, upd, bd)
	case "help":
		cmdHelp(ctx, upd, bd)
	default:
		replyHTML(bd.Bot, msg.Chat.ID,
			"알 수 없는 명령입니다. /help 를 참고하세요.")
	}
}

// notifyStartup sends a restart notice to all telegram-transport sessions.
func notifyStartup(ctx context.Context, platform core.ChatPlatform, sm *session.Manager) {
	sessions := sm.ListAll()
	seen := make(map[int64]bool)
	for _, sess := range sessions {
		if sess.Transport != "telegram" {
			continue
		}
		if !seen[sess.ChatID] {
			seen[sess.ChatID] = true
			platform.Send(ctx, sess.ChatID, core.Message{ //nolint:errcheck
				Text: "🚀 remode 봇이 시작됐습니다.",
			}, "")
		}
	}
}

func parseArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}
