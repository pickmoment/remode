package discord

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/session"
)

var discordSeparatorRE = regexp.MustCompile(`(?m)^─{3,}(?:\s+\S.*?\s+─+)?$`)

var dcAgentLabel = map[string]string{
	"claude_code": "Claude Code",
	"codex":       "Codex",
}
var dcAgentEmoji = map[string]string{
	"claude_code": "🟣",
	"codex":       "🟢",
}

// BotData holds shared references for Discord handlers.
type BotData struct {
	SM           *session.Manager
	Session      *discordgo.Session
	AllowedIDs   map[int64]bool
	NewProjectDir string
	StopCh       chan struct{}
	// ephemeral
	projList     *dcProjListState
	projSelected *dcProjSelectedState
	pendingNew   *dcPendingNewState
}

type dcProjListState struct {
	agentType string
	projects  []core.Project
}
type dcProjSelectedState struct {
	agentType string
	project   core.Project
}
type dcPendingNewState struct {
	name string
	cwd  string
}

func allowed(userID int64, ids map[int64]bool) bool {
	return ids[userID]
}

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content},
	})
}

func respondWithComponents(s *discordgo.Session, i *discordgo.InteractionCreate, content string, components []discordgo.MessageComponent) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Components: components},
	})
}

func editResponse(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content}) //nolint:errcheck
}

func editResponseNoComponents(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	empty := []discordgo.MessageComponent{}
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{ //nolint:errcheck
		Content:    &content,
		Components: &empty,
	})
}

func channelID(i *discordgo.InteractionCreate) int64 {
	id, _ := strconv.ParseInt(i.ChannelID, 10, 64)
	return id
}

func userID(i *discordgo.InteractionCreate) int64 {
	if i.Member != nil && i.Member.User != nil {
		id, _ := strconv.ParseInt(i.Member.User.ID, 10, 64)
		return id
	}
	if i.User != nil {
		id, _ := strconv.ParseInt(i.User.ID, 10, 64)
		return id
	}
	return 0
}

// ── command option helpers ────────────────────────────────────────────────────

func optString(i *discordgo.InteractionCreate, name string) string {
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == name {
			return opt.StringValue()
		}
	}
	return ""
}

// ── slash commands ────────────────────────────────────────────────────────────

func handleStart(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	respond(s, i,
		"👋 **remode** — AI 코딩 에이전트 원격 프론트엔드\n\n"+
			"/projects — 프로젝트 목록\n"+
			"/sessions — 현재 프로젝트의 세션 전환\n"+
			"/new <이름> — 새 세션\n"+
			"/attach <이름> — 세션 재연결\n"+
			"/list — 활성 세션 목록\n"+
			"/kill <이름> — 세션 종료\n"+
			"/status — 현재 세션 정보\n"+
			"/send <텍스트> — 슬래시 커맨드 전달\n"+
			"/clear — 대화 히스토리 초기화\n"+
			"/level — 메시지 수준 조회/변경\n"+
			"/shutdown — remode 봇 종료\n"+
			"/help — 도움말\n\n"+
			"일반 메시지는 바인딩된 세션으로 전달됩니다.",
	)
}

func handleNew(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	name := optString(i, "name")
	if name == "" {
		respond(s, i, "세션 이름을 입력하세요.")
		return
	}
	cwd := bd.NewProjectDir + "/" + name
	sm := bd.SM

	if sm.Get(name) != nil {
		respond(s, i, fmt.Sprintf("세션 **%s**이 이미 존재합니다.", name))
		return
	}

	agents := sm.EnabledAgents()
	if len(agents) > 1 {
		bd.pendingNew = &dcPendingNewState{name: name, cwd: cwd}
		var rows []discordgo.MessageComponent
		var buttons []discordgo.MessageComponent
		for _, a := range agents {
			buttons = append(buttons, discordgo.Button{
				Label:    dcAgentLabel[a],
				Style:    discordgo.PrimaryButton,
				CustomID: "_:agent_new:" + a,
			})
		}
		rows = append(rows, discordgo.ActionsRow{Components: buttons})
		respondWithComponents(s, i, fmt.Sprintf("**%s** 세션에 사용할 에이전트를 선택하세요:", name), rows)
		return
	}

	// Defer + create
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err := mkdirAll(cwd); err != nil {
		editResponse(s, i, fmt.Sprintf("❌ 디렉터리 생성 실패: %v", err))
		return
	}
	if _, err := sm.Create(ctx, name, cwd, channelID(i), ""); err != nil {
		log.Printf("세션 생성 실패: %v", err)
		editResponse(s, i, fmt.Sprintf("❌ 실패: %v", err))
	} else {
		editResponse(s, i, fmt.Sprintf("✅ 세션 **%s** 준비됨", name))
	}
}

func handleAttach(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	name := optString(i, "name")
	sm := bd.SM
	chID := channelID(i)

	if name == "" {
		sessions := sm.ListAll()
		if len(sessions) == 0 {
			respond(s, i, "활성 세션이 없습니다.")
			return
		}
		current := sm.GetByChat(chID)
		var buttons []discordgo.MessageComponent
		for _, sess := range sessions {
			suffix := ""
			if current != nil && current.Name == sess.Name {
				suffix = "  ←"
			}
			emoji := dcAgentEmoji[sess.AgentType]
			buttons = append(buttons, discordgo.Button{
				Label:    fmt.Sprintf("%s %s%s", emoji, sess.Name, suffix),
				Style:    discordgo.PrimaryButton,
				CustomID: "_:attach:" + sess.Name,
			})
		}
		respondWithComponents(s, i, "연결할 세션을 선택하세요:", []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: buttons},
		})
		return
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if _, err := sm.Attach(ctx, name, chID); err != nil {
		editResponse(s, i, fmt.Sprintf("세션 **%s**을 찾을 수 없습니다.", name))
	} else {
		editResponse(s, i, fmt.Sprintf("✅ 세션 **%s**에 연결됨", name))
	}
}

func handleList(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	sm := bd.SM
	sessions := sm.ListAll()
	if len(sessions) == 0 {
		respond(s, i, "활성 세션 없음")
		return
	}
	current := sm.GetByChat(channelID(i))
	var lines []string
	for _, sess := range sessions {
		bound := ""
		if current != nil && current.Name == sess.Name {
			bound = "← 현재"
		}
		emoji := dcAgentEmoji[sess.AgentType]
		lines = append(lines, fmt.Sprintf("%s **%s**  `%s`  %s", emoji, sess.Name, sess.CWD, bound))
	}
	respond(s, i, strings.Join(lines, "\n"))
}

func handleKill(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	name := optString(i, "name")
	sm := bd.SM
	chID := channelID(i)

	if name == "" {
		sessions := sm.ListAll()
		if len(sessions) == 0 {
			respond(s, i, "활성 세션이 없습니다.")
			return
		}
		current := sm.GetByChat(chID)
		var buttons []discordgo.MessageComponent
		for _, sess := range sessions {
			suffix := ""
			if current != nil && current.Name == sess.Name {
				suffix = "  ←"
			}
			emoji := dcAgentEmoji[sess.AgentType]
			buttons = append(buttons, discordgo.Button{
				Label:    fmt.Sprintf("%s %s%s", emoji, sess.Name, suffix),
				Style:    discordgo.DangerButton,
				CustomID: "_:kill:" + sess.Name,
			})
		}
		respondWithComponents(s, i, "종료할 세션을 선택하세요:", []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: buttons},
		})
		return
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err := sm.Kill(ctx, name); err != nil {
		editResponse(s, i, fmt.Sprintf("세션 **%s**을 찾을 수 없습니다.", name))
	} else {
		editResponse(s, i, fmt.Sprintf("🗑️ 세션 **%s** 종료됨", name))
	}
}

func handleStatus(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	sess := bd.SM.GetByChat(channelID(i))
	if sess == nil {
		respond(s, i, "이 채널에 바인딩된 세션이 없습니다.")
		return
	}
	emoji := dcAgentEmoji[sess.AgentType]
	label := dcAgentLabel[sess.AgentType]
	respond(s, i, fmt.Sprintf(
		"📎 **%s**\n  에이전트: %s %s\n  경로: `%s`\n  tmux: `%s`\n  session-id: `%s`\n  생성: %s",
		sess.Name, emoji, label,
		sess.CWD, sess.TmuxName, sess.SessionID,
		sess.CreatedAt.Format("2006-01-02 15:04:05"),
	))
}

func handleSend(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	text := optString(i, "text")
	if text == "" {
		respond(s, i, "전달할 텍스트를 입력하세요.")
		return
	}
	dcSendToSession(ctx, s, i, bd, text)
}

func handleLevel(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	value := optString(i, "value")
	chID := channelID(i)
	sess := bd.SM.GetByChat(chID)

	if value == "" {
		current := "interactive"
		if sess != nil {
			current = string(sess.Level)
		}
		sessNote := " (바인딩된 세션 없음)"
		if sess != nil {
			sessNote = fmt.Sprintf(" (세션: **%s**)", sess.Name)
		}
		respond(s, i, fmt.Sprintf(
			"현재 메시지 수준%s: **%s**\n\n"+
				"• **all** — 모든 내역\n"+
				"• **interactive** — 어시스턴트 텍스트 + 승인 버튼\n"+
				"• **final** — 어시스턴트 최종 답변만",
			sessNote, current,
		))
		return
	}
	if sess == nil {
		respond(s, i, "이 채널에 바인딩된 세션이 없습니다.")
		return
	}
	bd.SM.SetMessageLevel(ctx, sess, value) //nolint:errcheck
	respond(s, i, fmt.Sprintf("✅ 메시지 수준이 **%s**로 변경되었습니다.", value))
}

func handleClear(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	sess := bd.SM.GetByChat(channelID(i))
	if sess == nil {
		respond(s, i, "이 채널에 바인딩된 세션이 없습니다.")
		return
	}
	if err := bd.SM.SendInput(sess, "/clear"); err != nil {
		respond(s, i, fmt.Sprintf("❌ 전송 실패: %v", err))
		return
	}
	respond(s, i, "🧹 /clear 명령을 전송했습니다.")
}

func handleScreen(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	sess := bd.SM.GetByChat(channelID(i))
	if sess == nil {
		respond(s, i, "이 채널에 바인딩된 세션이 없습니다.")
		return
	}
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	content, err := bd.SM.Capture(sess)
	if err != nil {
		editResponse(s, i, fmt.Sprintf("❌ 캡처 실패: %v", err))
		return
	}
	content = stripDCUISeparators(content)
	if len(content) > 1990 {
		content = "…" + content[len(content)-1989:]
	}
	editResponse(s, i, "```\n"+content+"\n```")
}

func handleProjects(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	sm := bd.SM
	agents := sm.EnabledAgents()
	if len(agents) > 1 {
		var buttons []discordgo.MessageComponent
		for _, a := range agents {
			buttons = append(buttons, discordgo.Button{
				Label:    dcAgentLabel[a],
				Style:    discordgo.PrimaryButton,
				CustomID: "_:agent_projects:" + a,
			})
		}
		respondWithComponents(s, i, "프로젝트를 조회할 에이전트를 선택하세요:", []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: buttons},
		})
		return
	}
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	dcShowProjectList(ctx, s, i, agents[0], bd, true)
}

func handleSessions(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	sm := bd.SM
	sess := sm.GetByChat(channelID(i))
	if sess == nil {
		respond(s, i, "이 채널에 바인딩된 세션이 없습니다.")
		return
	}
	sessionsList := sm.ListProjectSessions(sess)
	if len(sessionsList) == 0 {
		respond(s, i, "이 프로젝트의 세션이 없습니다.")
		return
	}
	var buttons []discordgo.MessageComponent
	for j, ps := range sessionsList {
		if j >= 25 {
			break
		}
		ts := ps.CreatedAt
		if len(ts) > 16 {
			ts = ts[:16]
		}
		label := ts
		if ps.SessionID == sess.SessionID {
			label = "● " + label
		}
		buttons = append(buttons, discordgo.Button{
			Label:    label,
			Style:    discordgo.PrimaryButton,
			CustomID: "_:session_switch:" + ps.SessionID,
		})
	}
	respondWithComponents(s, i, fmt.Sprintf("**%s** 프로젝트의 세션 목록:", sess.Name), []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: buttons},
	})
}

func handleShutdown(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	respondWithComponents(s, i,
		"⚠️ **remode 봇을 종료하시겠습니까?**\n\n종료하면 모든 세션 모니터링이 중단됩니다.",
		[]discordgo.MessageComponent{
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "✅ 예, 종료합니다", Style: discordgo.DangerButton, CustomID: "_:shutdown:confirm"},
				discordgo.Button{Label: "❌ 취소", Style: discordgo.SecondaryButton, CustomID: "_:shutdown:cancel"},
			}},
		},
	)
}

func handleHelp(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	respond(s, i,
		"**remode 명령어 목록**\n\n"+
			"**/new** <이름> — 새 세션\n"+
			"**/attach** [이름] — 세션 재연결\n"+
			"**/list** — 활성 세션 목록\n"+
			"**/kill** [이름] — 세션 종료\n"+
			"**/status** — 현재 세션 정보\n"+
			"**/projects** — 프로젝트 목록\n"+
			"**/sessions** — 세션 전환\n"+
			"**/send** <텍스트> — 에이전트 커맨드 전달\n"+
			"**/clear** — 대화 히스토리 초기화\n"+
			"**/screen** — tmux 화면 캡처\n"+
			"**/level** — 메시지 수준 조회/변경\n"+
			"**/shutdown** — 봇 종료",
	)
}

// ── button interaction handler ────────────────────────────────────────────────

func handleButtonInteraction(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData) {
	customID := i.MessageComponentData().CustomID
	parts := strings.SplitN(customID, ":", 3)
	if len(parts) != 3 {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		return
	}
	shortID, kind, value := parts[0], parts[1], parts[2]
	sm := bd.SM
	chID := channelID(i)

	editNoComp := func(content string) {
		editResponseNoComponents(s, i, content)
	}

	switch kind {
	case "agent_new":
		pending := bd.pendingNew
		bd.pendingNew = nil
		if pending == nil {
			editNoComp("⚠️ 세션 정보가 만료됐습니다. /new 를 다시 실행하세요.")
			return
		}
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		mkdirAll(pending.cwd) //nolint:errcheck
		if _, err := sm.Create(ctx, pending.name, pending.cwd, chID, value); err != nil {
			log.Printf("세션 생성 실패: %v", err)
			editNoComp(fmt.Sprintf("❌ 실패: %v", err))
		} else {
			editNoComp(fmt.Sprintf("✅ 세션 **%s** 준비됨", pending.name))
		}

	case "agent_projects":
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		dcShowProjectList(ctx, s, i, value, bd, false)

	case "project":
		if bd.projList == nil {
			editNoComp("⚠️ 프로젝트 정보가 만료됐습니다.")
			return
		}
		var idx int
		fmt.Sscanf(value, "%d", &idx)
		if idx < 0 || idx >= len(bd.projList.projects) {
			editNoComp("⚠️ 프로젝트 정보가 만료됐습니다.")
			return
		}
		proj := bd.projList.projects[idx]
		if len(proj.Sessions) == 0 || proj.CWD == "" {
			editNoComp("⚠️ 세션이 없는 프로젝트입니다.")
			return
		}
		bd.projSelected = &dcProjSelectedState{agentType: bd.projList.agentType, project: proj}
		var buttons []discordgo.MessageComponent
		for j, ps := range proj.Sessions {
			if j >= 25 {
				break
			}
			ts := ps.CreatedAt
			if len(ts) > 16 {
				ts = ts[:16]
			}
			label := ts
			if ps.Title != "" {
				label = ps.Title + "  " + ts
			}
			buttons = append(buttons, discordgo.Button{
				Label:    truncate(label, 80),
				Style:    discordgo.PrimaryButton,
				CustomID: fmt.Sprintf("_:resume:%d", j),
			})
		}
		empty := []discordgo.MessageComponent{}
		content := fmt.Sprintf("**%s**\n세션을 선택하세요:", proj.DisplayPath)
		rows := []discordgo.MessageComponent{discordgo.ActionsRow{Components: buttons}}
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{ //nolint:errcheck
			Content:    &content,
			Components: &rows,
		})
		_ = empty

	case "resume":
		if bd.projSelected == nil {
			editNoComp("⚠️ 프로젝트 정보가 만료됐습니다.")
			return
		}
		var jdx int
		fmt.Sscanf(value, "%d", &jdx)
		if jdx < 0 || jdx >= len(bd.projSelected.project.Sessions) {
			editNoComp("⚠️ 세션 정보가 만료됐습니다.")
			return
		}
		ps := bd.projSelected.project.Sessions[jdx]
		base := pathBase(bd.projSelected.project.CWD)
		if base == "" {
			base = "session"
		}
		name := base
		for idx := 2; sm.Get(name) != nil; idx++ {
			name = fmt.Sprintf("%s-%d", base, idx)
		}
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		if _, err := sm.Resume(ctx, name, bd.projSelected.project.CWD, chID, ps.SessionID, bd.projSelected.agentType); err != nil {
			log.Printf("세션 재개 실패: %v", err)
			editNoComp(fmt.Sprintf("❌ 실패: %v", err))
		} else {
			editNoComp(fmt.Sprintf("✅ 세션 **%s** 준비됨", name))
		}

	case "attach":
		if _, err := sm.Attach(ctx, value, chID); err != nil {
			editNoComp(fmt.Sprintf("세션 **%s**을 찾을 수 없습니다.", value))
		} else {
			editNoComp(fmt.Sprintf("✅ 세션 **%s**에 연결됨", value))
		}

	case "kill":
		if err := sm.Kill(ctx, value); err != nil {
			editNoComp(fmt.Sprintf("세션 **%s**을 찾을 수 없습니다.", value))
		} else {
			editNoComp(fmt.Sprintf("🗑️ 세션 **%s** 종료됨", value))
		}

	case "session_switch":
		sess := sm.GetByChat(chID)
		if sess == nil {
			editNoComp("바인딩된 세션이 없습니다.")
			return
		}
		if value == sess.SessionID {
			editNoComp("이미 해당 세션에 연결되어 있습니다.")
			return
		}
		name, cwd, fromChatID, agentType := sess.Name, sess.CWD, sess.ChatID, sess.AgentType
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		sm.Kill(ctx, name)  //nolint:errcheck
		if _, err := sm.Resume(ctx, name, cwd, fromChatID, value, agentType); err != nil {
			log.Printf("세션 전환 실패: %v", err)
			editNoComp(fmt.Sprintf("❌ 실패: %v", err))
		} else {
			editNoComp("✅ 세션 전환 완료")
		}

	case "shutdown":
		if value == "confirm" {
			editNoComp("🔴 remode 봇을 종료합니다…")
			if bd.StopCh != nil {
				close(bd.StopCh)
			}
		} else {
			editNoComp("취소되었습니다.")
		}

	case "key":
		for _, sess := range sm.ListAll() {
			short := sess.Name
			if len(short) > 20 {
				short = short[:20]
			}
			if short == shortID {
				sm.SendKey(sess, value) //nolint:errcheck
				break
			}
		}
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})

	default:
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
	}
}

// ── text message handler ──────────────────────────────────────────────────────

func handleTextMessage(ctx context.Context, s *discordgo.Session, m *discordgo.MessageCreate, bd *BotData) {
	if m.Author == nil || m.Author.Bot {
		return
	}
	uid, _ := strconv.ParseInt(m.Author.ID, 10, 64)
	if !allowed(uid, bd.AllowedIDs) {
		return
	}
	text := m.Content
	if strings.HasPrefix(text, "/") {
		s.ChannelMessageSend(m.ChannelID, "알 수 없는 명령입니다. /send /plan 형식으로 커맨드를 전달하세요.") //nolint:errcheck
		return
	}
	if strings.HasPrefix(text, "\\") {
		text = text[1:]
	}
	chID, _ := strconv.ParseInt(m.ChannelID, 10, 64)
	sess := bd.SM.GetByChat(chID)
	if sess == nil {
		s.ChannelMessageSend(m.ChannelID, "이 채널에 바인딩된 세션이 없습니다. /new 또는 /attach 로 세션을 만드세요.") //nolint:errcheck
		return
	}
	bd.SM.SendInput(sess, text) //nolint:errcheck
}

// ── project list display ──────────────────────────────────────────────────────

func dcShowProjectList(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, agentType string, bd *BotData, editMode bool) {
	projects, err := bd.SM.ListProjects(agentType)
	if err != nil || len(projects) == 0 {
		if editMode {
			editResponse(s, i, "프로젝트 없음")
		} else {
			editResponseNoComponents(s, i, "프로젝트 없음")
		}
		return
	}
	bd.projList = &dcProjListState{agentType: agentType, projects: projects}

	var buttons []discordgo.MessageComponent
	for idx, p := range projects {
		if idx >= 25 {
			break
		}
		lastMod := ""
		if len(p.Sessions) > 0 {
			ts := p.Sessions[0].LastModified
			if len(ts) >= 16 {
				lastMod = ts[5:16]
			}
		}
		label := truncate(p.DisplayPath+"  "+lastMod, 80)
		buttons = append(buttons, discordgo.Button{
			Label:    label,
			Style:    discordgo.PrimaryButton,
			CustomID: fmt.Sprintf("_:project:%d", idx),
		})
	}
	content := "프로젝트를 선택하세요:"
	rows := []discordgo.MessageComponent{discordgo.ActionsRow{Components: buttons}}
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{ //nolint:errcheck
		Content:    &content,
		Components: &rows,
	})
}

// ── helpers ───────────────────────────────────────────────────────────────────

func dcSendToSession(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, bd *BotData, text string) {
	chID := channelID(i)
	sess := bd.SM.GetByChat(chID)
	if sess == nil {
		respond(s, i, "이 채널에 바인딩된 세션이 없습니다.")
		return
	}
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ //nolint:errcheck
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
	if err := bd.SM.SendInput(sess, text); err != nil {
		editResponse(s, i, fmt.Sprintf("❌ 전송 실패: %v", err))
	} else {
		editResponse(s, i, "✅ 전송됨")
	}
}

func stripDCUISeparators(text string) string {
	var result []string
	prevBlank := false
	for _, line := range strings.Split(text, "\n") {
		if discordSeparatorRE.MatchString(strings.TrimSpace(line)) {
			continue
		}
		isBlank := strings.TrimSpace(line) == ""
		if isBlank && prevBlank {
			continue
		}
		prevBlank = isBlank
		result = append(result, line)
	}
	for len(result) > 0 && strings.TrimSpace(result[0]) == "" {
		result = result[1:]
	}
	for len(result) > 0 && strings.TrimSpace(result[len(result)-1]) == "" {
		result = result[:len(result)-1]
	}
	return strings.Join(result, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func pathBase(p string) string {
	p = strings.TrimRight(p, "/")
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

func mkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}
