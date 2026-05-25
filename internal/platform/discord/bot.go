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
	Token          string
	GuildID        string  // empty = global commands (up to 1h propagation); set for dev guild
	AllowedUserIDs []int64
	NotifyChannelIDs []int64
	NewProjectDir  string
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

// Run starts the Discord bot. It creates the platform, calls sm.Startup, then
// runs until ctx is cancelled or /shutdown is received.
func Run(ctx context.Context, cfg RunConfig, sm *session.Manager, setPlatform func(core.ChatPlatform)) error {
	dg, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return err
	}
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages

	platform := NewPlatform(dg)
	if setPlatform != nil {
		setPlatform(platform)
	}

	allowedIDs := make(map[int64]bool, len(cfg.AllowedUserIDs))
	for _, id := range cfg.AllowedUserIDs {
		allowedIDs[id] = true
	}

	stopCh := make(chan struct{})
	bd := &BotData{
		SM:            sm,
		Session:       dg,
		AllowedIDs:    allowedIDs,
		NewProjectDir: cfg.NewProjectDir,
		StopCh:        stopCh,
	}

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
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

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		handleTextMessage(ctx, s, m, bd)
	})

	if err := dg.Open(); err != nil {
		return err
	}
	defer dg.Close()

	// Register slash commands
	registered := registerCommands(dg, cfg.GuildID)

	if err := sm.Startup(ctx); err != nil {
		log.Printf("startup error: %v", err)
	}

	// Notify known sessions that the bot restarted
	go notifyDCStartup(ctx, platform, sm, cfg.NotifyChannelIDs)

	log.Printf("Discord bot ready (guild=%q)", cfg.GuildID)

	select {
	case <-ctx.Done():
	case <-stopCh:
		log.Println("shutdown requested via bot")
	}

	deleteCommands(dg, cfg.GuildID, registered)
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

func registerCommands(s *discordgo.Session, guildID string) []*discordgo.ApplicationCommand {
	appID := s.State.User.ID
	var registered []*discordgo.ApplicationCommand
	for _, cmd := range slashCommands {
		c, err := s.ApplicationCommandCreate(appID, guildID, cmd)
		if err != nil {
			log.Printf("discord: register command %s: %v", cmd.Name, err)
			continue
		}
		registered = append(registered, c)
	}
	log.Printf("discord: registered %d commands", len(registered))
	return registered
}

func deleteCommands(s *discordgo.Session, guildID string, cmds []*discordgo.ApplicationCommand) {
	appID := s.State.User.ID
	for _, cmd := range cmds {
		if err := s.ApplicationCommandDelete(appID, guildID, cmd.ID); err != nil {
			log.Printf("discord: delete command %s: %v", cmd.Name, err)
		}
	}
}

func notifyDCStartup(ctx context.Context, platform core.ChatPlatform, sm *session.Manager, notifyChannelIDs []int64) {
	msg := core.Message{Text: "🚀 remode 봇이 시작됐습니다."}
	seen := make(map[int64]bool)

	// Notify all known session channels
	for _, sess := range sm.ListAll() {
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

