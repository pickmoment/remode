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

// Run starts the Telegram bot. It creates the platform, calls sm.Startup, then
// runs the update loop until ctx is cancelled or /shutdown is received.
func Run(ctx context.Context, cfg RunConfig, sm *session.Manager, setPlatform func(core.ChatPlatform)) error {
	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		return err
	}
	log.Printf("Telegram bot authorised as @%s", bot.Self.UserName)

	platform := NewPlatform(bot)
	// Let main inject the platform into the session manager before startup
	if setPlatform != nil {
		setPlatform(platform)
	}

	if err := sm.Startup(ctx); err != nil {
		log.Printf("startup error: %v", err)
	}

	stopCh := make(chan struct{})
	bd := &BotData{
		SM:  sm,
		Bot: bot,
		Config: handlerConfig{
			AllowedUserIDs: cfg.AllowedUserIDs,
			NewProjectDir:  cfg.NewProjectDir,
		},
		StopCh: stopCh,
	}

	// Notify all known sessions about the bot restart
	go notifyStartup(ctx, platform, sm)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

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

func notifyStartup(ctx context.Context, platform core.ChatPlatform, sm *session.Manager) {
	sessions := sm.ListAll()
	seen := make(map[int64]bool)
	for _, sess := range sessions {
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
