package discord

import (
	"context"
	"log"

	"github.com/bwmarrin/discordgo"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/session"
)

// RunConfig holds all parameters needed to start the Discord bot.
type RunConfig struct {
	Token            string
	GuildID          string  // empty = global commands (up to 1h propagation); set for dev guild
	AllowedUserIDs   []int64
	NotifyChannelIDs []int64
	NewProjectDir    string
}

var slashCommands = []*discordgo.ApplicationCommand{
	{Name: "start", Description: "remode 소개"},
	{Name: "new", Description: "새 세션 생성", Options: []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "세션 이름", Required: true},
	}},
	{Name: "attach", Description: "세션 재연결", Options: []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "세션 이름", Required: false},
	}},
	{Name: "list", Description: "활성 세션 목록"},
	{Name: "kill", Description: "세션 종료", Options: []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "세션 이름", Required: false},
	}},
	{Name: "status", Description: "현재 세션 정보"},
	{Name: "send", Description: "에이전트 커맨드 전달", Options: []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "전달할 텍스트", Required: true},
	}},
	{Name: "level", Description: "메시지 수준 조회/변경", Options: []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "value", Description: "all | interactive | final", Required: false},
	}},
	{Name: "clear", Description: "대화 히스토리 초기화"},
	{Name: "screen", Description: "tmux 화면 캡처"},
	{Name: "projects", Description: "프로젝트 목록"},
	{Name: "sessions", Description: "현재 프로젝트의 세션 전환"},
	{Name: "shutdown", Description: "remode 봇 종료"},
	{Name: "help", Description: "도움말"},
}

// BotInstance holds a constructed Discord bot instance ready to Run.
// Construct with NewBot, register ChatPlatform() with the session manager,
// then call Run after sm.Startup.
type BotInstance struct {
	platform *Platform
	dg       *discordgo.Session
	cfg      RunConfig
}

// NewBot creates the Discord session and platform.
// Returns an instance whose ChatPlatform() can be registered with the Manager.
func NewBot(cfg RunConfig) (*BotInstance, error) {
	dg, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, err
	}
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages
	return &BotInstance{
		platform: NewPlatform(dg),
		dg:       dg,
		cfg:      cfg,
	}, nil
}

// ChatPlatform returns the core.ChatPlatform for registration with the Manager.
func (b *BotInstance) ChatPlatform() core.ChatPlatform { return b.platform }

// Run starts the Discord interaction/message handlers and blocks until ctx
// is cancelled or /shutdown is received. Must be called after sm.Startup.
func (b *BotInstance) Run(ctx context.Context, sm *session.Manager) error {
	allowedIDs := make(map[int64]bool, len(b.cfg.AllowedUserIDs))
	for _, id := range b.cfg.AllowedUserIDs {
		allowedIDs[id] = true
	}

	stopCh := make(chan struct{})
	bd := &BotData{
		SM:            sm,
		Session:       b.dg,
		AllowedIDs:    allowedIDs,
		NewProjectDir: b.cfg.NewProjectDir,
		StopCh:        stopCh,
	}

	b.dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		uid := userID(i)
		if !allowed(uid, allowedIDs) {
			return
		}
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			dispatchCommand(ctx, s, i, bd)
		case discordgo.InteractionMessageComponent:
			handleButtonInteraction(ctx, s, i, bd)
		}
	})

	b.dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		handleTextMessage(ctx, s, m, bd)
	})

	if err := b.dg.Open(); err != nil {
		return err
	}
	defer b.dg.Close()

	// Register slash commands (single atomic API call)
	registerCommands(b.dg, b.cfg.GuildID)

	// Notify known discord sessions that the bot restarted.
	go notifyDCStartup(ctx, b.platform, sm, b.cfg.NotifyChannelIDs)

	log.Printf("Discord bot ready (guild=%q)", b.cfg.GuildID)

	select {
	case <-ctx.Done():
	case <-stopCh:
		log.Println("shutdown requested via bot")
	}

	return nil
}

func dispatchCommand(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	name := i.ApplicationCommandData().Name
	switch name {
	case "start":
		handleStart(ctx, s, i, bd)
	case "new":
		handleNew(ctx, s, i, bd)
	case "attach":
		handleAttach(ctx, s, i, bd)
	case "list":
		handleList(ctx, s, i, bd)
	case "kill":
		handleKill(ctx, s, i, bd)
	case "status":
		handleStatus(ctx, s, i, bd)
	case "send":
		handleSend(ctx, s, i, bd)
	case "level":
		handleLevel(ctx, s, i, bd)
	case "clear":
		handleClear(ctx, s, i, bd)
	case "screen":
		handleScreen(ctx, s, i, bd)
	case "projects":
		handleProjects(ctx, s, i, bd)
	case "sessions":
		handleSessions(ctx, s, i, bd)
	case "shutdown":
		handleShutdown(ctx, s, i, bd)
	case "help":
		handleHelp(ctx, s, i, bd)
	default:
		respond(s, i, "알 수 없는 명령입니다.")
	}
}

func registerCommands(s *discordgo.Session, guildID string) {
	if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, guildID, slashCommands); err != nil {
		log.Printf("discord: register commands: %v", err)
		return
	}
	log.Printf("discord: registered %d commands", len(slashCommands))
}

// notifyDCStartup sends a restart notice to discord-transport sessions and any
// configured notification channels.
func notifyDCStartup(ctx context.Context, platform core.ChatPlatform, sm *session.Manager, notifyChannelIDs []int64) {
	msg := core.Message{Text: "🚀 remode 봇이 시작됐습니다."}
	seen := make(map[int64]bool)

	// Notify all known discord session channels
	for _, sess := range sm.ListAll() {
		if sess.Transport != "discord" {
			continue
		}
		if !seen[sess.ChatID] {
			seen[sess.ChatID] = true
			platform.Send(ctx, sess.ChatID, msg, "") //nolint:errcheck
		}
	}

	// Also notify any configured notification channels
	for _, chID := range notifyChannelIDs {
		if !seen[chID] {
			seen[chID] = true
			platform.Send(ctx, chID, msg, "") //nolint:errcheck
		}
	}
}
