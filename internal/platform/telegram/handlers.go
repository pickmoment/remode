package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"os"
	"regexp"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/session"
)

var uiSeparatorRE = regexp.MustCompile(`(?m)^─{3,}(?:\s+\S.*?\s+─+)?$`)

// BotData holds shared references passed to handlers.
type BotData struct {
	SM       *session.Manager
	Bot      *tgbotapi.BotAPI
	Config   handlerConfig
	StopCh   chan struct{}
	// ephemeral state (not persisted across restarts)
	projList    *projListState
	projSelected *projSelectedState
	pendingNew  *pendingNewState
}

type handlerConfig struct {
	AllowedUserIDs []int64
	NewProjectDir  string
}

type projListState struct {
	agentType string
	projects  []core.Project
}

type projSelectedState struct {
	agentType string
	project   core.Project
}

type pendingNewState struct {
	name string
	cwd  string
}

var agentLabel = map[string]string{
	"claude_code": "Claude Code",
	"codex":       "Codex",
}
var agentEmoji = map[string]string{
	"claude_code": "🟣",
	"codex":       "🟢",
}

func isAllowed(userID int64, allowed []int64) bool {
	for _, id := range allowed {
		if id == userID {
			return true
		}
	}
	return false
}

func replyHTML(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	bot.Send(msg) //nolint:errcheck
}

// ── command handlers ──────────────────────────────────────────────────────────

func cmdStart(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	replyHTML(bd.Bot, upd.Message.Chat.ID,
		"👋 <b>remode</b> — AI 코딩 에이전트 원격 프론트엔드\n\n"+
			"/projects — 프로젝트 목록\n"+
			"/sessions — 현재 프로젝트의 세션 전환\n"+
			"/new &lt;이름&gt; — 새 세션 (new_project_dir 하위에 생성)\n"+
			"/attach &lt;이름&gt; — 세션 재연결\n"+
			"/list — 활성 세션 목록\n"+
			"/kill &lt;이름&gt; — 세션 종료\n"+
			"/status — 현재 세션 정보\n"+
			"/send &lt;텍스트&gt; — 슬래시 커맨드 전달\n"+
			"/resend — 마지막 응답 재전송\n"+
			"/clear — 대화 히스토리 초기화\n"+
			"/level — 메시지 수준 조회/변경\n"+
			"/shutdown — remode 봇 종료\n"+
			"/help — 도움말\n\n"+
			"일반 메시지는 바인딩된 세션으로 전달됩니다.\n"+
			"<code>\\</code> 로 시작하면 앞의 백슬래시를 제거 후 전달 (<code>\\/plan</code> → <code>/plan</code>)",
	)
}

func cmdNew(ctx context.Context, upd tgbotapi.Update, bd *BotData, args []string) {
	chatID := upd.Message.Chat.ID
	if len(args) == 0 {
		replyHTML(bd.Bot, chatID, "사용법: /new &lt;이름&gt;")
		return
	}
	name := args[0]
	cwd := bd.Config.NewProjectDir + "/" + name

	if bd.SM.Get(name) != nil {
		replyHTML(bd.Bot, chatID, fmt.Sprintf("세션 <b>%s</b>이 이미 존재합니다.", html.EscapeString(name)))
		return
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		replyHTML(bd.Bot, chatID, fmt.Sprintf("❌ 디렉터리 생성 실패: %v", err))
		return
	}

	agents := bd.SM.EnabledAgents()
	if len(agents) > 1 {
		bd.pendingNew = &pendingNewState{name: name, cwd: cwd}
		var rows [][]tgbotapi.InlineKeyboardButton
		for _, a := range agents {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(agentLabel[a], "_:agent_new:"+a),
			))
		}
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("<b>%s</b> 세션에 사용할 에이전트를 선택하세요:", html.EscapeString(name)))
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
		bd.Bot.Send(msg) //nolint:errcheck
		return
	}

	replyHTML(bd.Bot, chatID, fmt.Sprintf("⏳ 세션 <b>%s</b> 시작 중…", html.EscapeString(name)))
	_, err := bd.SM.Create(ctx, name, cwd, chatID, "")
	if err != nil {
		log.Printf("세션 생성 실패: %v", err)
		replyHTML(bd.Bot, chatID, fmt.Sprintf("❌ 실패: %v", err))
		return
	}
	replyHTML(bd.Bot, chatID, fmt.Sprintf("✅ 세션 <b>%s</b> 준비됨", html.EscapeString(name)))
}

func cmdProjects(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	chatID := upd.Message.Chat.ID
	agents := bd.SM.EnabledAgents()
	if len(agents) > 1 {
		var rows [][]tgbotapi.InlineKeyboardButton
		for _, a := range agents {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(agentLabel[a], "_:agent_projects:"+a),
			))
		}
		msg := tgbotapi.NewMessage(chatID, "프로젝트를 조회할 에이전트를 선택하세요:")
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
		bd.Bot.Send(msg) //nolint:errcheck
		return
	}
	showProjectList(ctx, chatID, agents[0], bd, nil)
}

func showProjectList(ctx context.Context, chatID int64, agentType string, bd *BotData, editMsgID *int) {
	projects, err := bd.SM.ListProjects(agentType)
	if err != nil || len(projects) == 0 {
		replyHTML(bd.Bot, chatID, "프로젝트 없음")
		return
	}
	bd.projList = &projListState{agentType: agentType, projects: projects}

	var rows [][]tgbotapi.InlineKeyboardButton
	for i, p := range projects {
		if i >= 15 {
			break
		}
		lastMod := ""
		if len(p.Sessions) > 0 {
			ts := p.Sessions[0].LastModified
			if len(ts) >= 16 {
				lastMod = ts[5:16] // "MM-DD HH:MM"
			}
		}
		label := p.DisplayPath + "  " + lastMod
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("_:project:%d", i)),
		))
	}
	markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
	if editMsgID != nil {
		edit := tgbotapi.NewEditMessageText(chatID, *editMsgID, "프로젝트를 선택하세요:")
		edit.ReplyMarkup = &markup
		bd.Bot.Send(edit) //nolint:errcheck
	} else {
		msg := tgbotapi.NewMessage(chatID, "프로젝트를 선택하세요:")
		msg.ReplyMarkup = markup
		bd.Bot.Send(msg) //nolint:errcheck
	}
}

func cmdAttach(ctx context.Context, upd tgbotapi.Update, bd *BotData, args []string) {
	chatID := upd.Message.Chat.ID
	sm := bd.SM
	if len(args) == 0 {
		sessions := sm.ListAll()
		if len(sessions) == 0 {
			replyHTML(bd.Bot, chatID, "활성 세션이 없습니다.")
			return
		}
		current := sm.GetByChat(chatID)
		var rows [][]tgbotapi.InlineKeyboardButton
		for _, s := range sessions {
			suffix := ""
			if current != nil && current.Name == s.Name {
				suffix = "  ←"
			}
			emoji := agentEmoji[s.AgentType]
			if emoji == "" {
				emoji = "🤖"
			}
			label := fmt.Sprintf("%s %s  %s%s", emoji, s.Name, s.CWD, suffix)
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(label, "_:attach:"+s.Name),
			))
		}
		msg := tgbotapi.NewMessage(chatID, "연결할 세션을 선택하세요:")
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
		bd.Bot.Send(msg) //nolint:errcheck
		return
	}
	name := args[0]
	if _, err := sm.Attach(ctx, name, chatID); err != nil {
		replyHTML(bd.Bot, chatID, fmt.Sprintf("세션 <b>%s</b>을 찾을 수 없습니다.", html.EscapeString(name)))
		return
	}
	replyHTML(bd.Bot, chatID, fmt.Sprintf("✅ 세션 <b>%s</b>에 연결됨", html.EscapeString(name)))
}

func cmdList(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	chatID := upd.Message.Chat.ID
	sessions := bd.SM.ListAll()
	if len(sessions) == 0 {
		replyHTML(bd.Bot, chatID, "활성 세션 없음")
		return
	}
	current := bd.SM.GetByChat(chatID)
	var lines []string
	for _, s := range sessions {
		bound := ""
		if current != nil && current.Name == s.Name {
			bound = "← 현재"
		}
		emoji := agentEmoji[s.AgentType]
		if emoji == "" {
			emoji = "🤖"
		}
		lines = append(lines, fmt.Sprintf("%s <b>%s</b>  <code>%s</code>  %s",
			emoji, html.EscapeString(s.Name), html.EscapeString(s.CWD), bound))
	}
	replyHTML(bd.Bot, chatID, strings.Join(lines, "\n"))
}

func cmdKill(ctx context.Context, upd tgbotapi.Update, bd *BotData, args []string) {
	chatID := upd.Message.Chat.ID
	sm := bd.SM
	if len(args) == 0 {
		sessions := sm.ListAll()
		if len(sessions) == 0 {
			replyHTML(bd.Bot, chatID, "활성 세션이 없습니다.")
			return
		}
		current := sm.GetByChat(chatID)
		var rows [][]tgbotapi.InlineKeyboardButton
		for _, s := range sessions {
			suffix := ""
			if current != nil && current.Name == s.Name {
				suffix = "  ←"
			}
			emoji := agentEmoji[s.AgentType]
			if emoji == "" {
				emoji = "🤖"
			}
			label := fmt.Sprintf("%s %s  %s%s", emoji, s.Name, s.CWD, suffix)
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(label, "_:kill:"+s.Name),
			))
		}
		msg := tgbotapi.NewMessage(chatID, "종료할 세션을 선택하세요:")
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
		bd.Bot.Send(msg) //nolint:errcheck
		return
	}
	name := args[0]
	if err := sm.Kill(ctx, name); err != nil {
		replyHTML(bd.Bot, chatID, fmt.Sprintf("세션 <b>%s</b>을 찾을 수 없습니다.", html.EscapeString(name)))
		return
	}
	replyHTML(bd.Bot, chatID, fmt.Sprintf("🗑️ 세션 <b>%s</b> 종료됨", html.EscapeString(name)))
}

func cmdStatus(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	chatID := upd.Message.Chat.ID
	sess := bd.SM.GetByChat(chatID)
	if sess == nil {
		replyHTML(bd.Bot, chatID, "이 채팅에 바인딩된 세션이 없습니다.")
		return
	}
	stats := readContextStats(sess.JSONLPath)
	ctxLine := ""
	if stats != nil {
		const ctxWindow = 200_000
		pct := float64(stats.ctxTokens) / ctxWindow * 100
		barFilled := int(pct / 10)
		bar := strings.Repeat("█", barFilled) + strings.Repeat("░", 10-barFilled)
		ctxLine = fmt.Sprintf("\n  컨텍스트: <code>%s</code> %.1f%% (%s/%s, %d턴)",
			bar, pct, fmtK(stats.ctxTokens), fmtK(ctxWindow), stats.msgCount)
	}
	emoji := agentEmoji[sess.AgentType]
	label := agentLabel[sess.AgentType]
	replyHTML(bd.Bot, chatID, fmt.Sprintf(
		"📎 <b>%s</b>\n  에이전트: %s %s\n  경로: <code>%s</code>\n  tmux: <code>%s</code>\n  session-id: <code>%s</code>\n  생성: %s%s",
		html.EscapeString(sess.Name),
		emoji, html.EscapeString(label),
		html.EscapeString(sess.CWD),
		html.EscapeString(sess.TmuxName),
		html.EscapeString(sess.SessionID),
		sess.CreatedAt.Format("2006-01-02 15:04:05"),
		ctxLine,
	))
}

func cmdSend(ctx context.Context, upd tgbotapi.Update, bd *BotData, args []string) {
	text := strings.Join(args, " ")
	if text == "" {
		replyHTML(bd.Bot, upd.Message.Chat.ID, "사용법: /send &lt;텍스트&gt;")
		return
	}
	sendToSession(ctx, upd.Message.Chat.ID, text, bd)
}

func cmdLevel(ctx context.Context, upd tgbotapi.Update, bd *BotData, args []string) {
	chatID := upd.Message.Chat.ID
	sess := bd.SM.GetByChat(chatID)
	if len(args) == 0 {
		current := "interactive"
		if sess != nil {
			current = string(sess.Level)
		}
		sessNote := " (바인딩된 세션 없음)"
		if sess != nil {
			sessNote = fmt.Sprintf(" (세션: <b>%s</b>)", html.EscapeString(sess.Name))
		}
		replyHTML(bd.Bot, chatID, fmt.Sprintf(
			"현재 메시지 수준%s: <b>%s</b>\n\n"+
				"• <b>all</b> — 모든 내역 (툴 카드 포함)\n"+
				"• <b>interactive</b> — 사용자 입력 필요 알림 + 어시스턴트 텍스트\n"+
				"• <b>final</b> — 어시스턴트 최종 답변만\n\n"+
				"변경: /level &lt;all|interactive|final&gt;",
			sessNote, current,
		))
		return
	}
	level := args[0]
	if level != "all" && level != "interactive" && level != "final" {
		replyHTML(bd.Bot, chatID, "유효하지 않은 수준입니다. all / interactive / final 중 선택하세요.")
		return
	}
	if sess == nil {
		replyHTML(bd.Bot, chatID, "이 채팅에 바인딩된 세션이 없습니다. /new 또는 /attach 로 세션을 만드세요.")
		return
	}
	bd.SM.SetMessageLevel(ctx, sess, level) //nolint:errcheck
	replyHTML(bd.Bot, chatID, fmt.Sprintf("✅ 세션 <b>%s</b>의 메시지 수준이 <b>%s</b>로 변경되었습니다.",
		html.EscapeString(sess.Name), level))
}

func cmdResend(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	chatID := upd.Message.Chat.ID
	sess := bd.SM.GetByChat(chatID)
	if sess == nil || len(sess.LastOutbound) == 0 {
		replyHTML(bd.Bot, chatID, "재전송할 마지막 메시지가 없습니다.")
		return
	}
	platform := NewPlatform(bd.Bot)
	for _, msg := range sess.LastOutbound {
		platform.Send(ctx, chatID, msg, "") //nolint:errcheck
	}
}

func cmdScreen(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	chatID := upd.Message.Chat.ID
	sess := bd.SM.GetByChat(chatID)
	if sess == nil {
		replyHTML(bd.Bot, chatID, "이 채팅에 바인딩된 세션이 없습니다.")
		return
	}
	content, err := bd.SM.Capture(sess)
	if err != nil {
		replyHTML(bd.Bot, chatID, fmt.Sprintf("❌ 캡처 실패: %v", err))
		return
	}
	content = stripUISeparators(content)
	escaped := html.EscapeString(content)
	if len(escaped) > 4088 {
		escaped = "…" + escaped[len(escaped)-4087:]
	}
	msg := tgbotapi.NewMessage(chatID, "<pre>"+escaped+"</pre>")
	msg.ParseMode = "HTML"
	bd.Bot.Send(msg) //nolint:errcheck
}

func cmdClear(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	chatID := upd.Message.Chat.ID
	sess := bd.SM.GetByChat(chatID)
	if sess == nil {
		replyHTML(bd.Bot, chatID, "이 채팅에 바인딩된 세션이 없습니다.")
		return
	}
	if err := bd.SM.SendInput(sess, "/clear"); err != nil {
		replyHTML(bd.Bot, chatID, fmt.Sprintf("❌ 전송 실패: %v", err))
		return
	}
	replyHTML(bd.Bot, chatID, "🧹 /clear 명령을 전송했습니다.")
}

func cmdSessions(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	chatID := upd.Message.Chat.ID
	sess := bd.SM.GetByChat(chatID)
	if sess == nil {
		replyHTML(bd.Bot, chatID, "이 채팅에 바인딩된 세션이 없습니다.")
		return
	}
	sessions := bd.SM.ListProjectSessions(sess)
	if len(sessions) == 0 {
		replyHTML(bd.Bot, chatID, "이 프로젝트의 세션이 없습니다.")
		return
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for i, s := range sessions {
		if i >= 10 {
			break
		}
		ts := s.CreatedAt
		if len(ts) > 16 {
			ts = ts[:16]
		}
		label := ts
		if s.Title != "" {
			label = s.Title + "  " + ts
		}
		if s.SessionID == sess.SessionID {
			label = "● " + label
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, "_:session_switch:"+s.SessionID),
		))
	}
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("<b>%s</b> 프로젝트의 세션 목록:", html.EscapeString(sess.Name)))
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	bd.Bot.Send(msg) //nolint:errcheck
}

func cmdShutdown(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	msg := tgbotapi.NewMessage(upd.Message.Chat.ID,
		"⚠️ <b>remode 봇을 종료하시겠습니까?</b>\n\n종료하면 모든 세션 모니터링이 중단됩니다.")
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ 예, 종료합니다", "_:shutdown:confirm"),
			tgbotapi.NewInlineKeyboardButtonData("❌ 취소", "_:shutdown:cancel"),
		),
	)
	bd.Bot.Send(msg) //nolint:errcheck
}

func cmdHelp(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	replyHTML(bd.Bot, upd.Message.Chat.ID,
		"<b>remode 명령어 목록</b>\n\n"+
			"<b>프로젝트 탐색</b>\n"+
			"/projects — 에이전트별 프로젝트 목록 조회\n"+
			"/sessions — 현재 세션과 같은 프로젝트의 세션 목록\n\n"+
			"<b>세션 관리</b>\n"+
			"/new &lt;이름&gt; — 새 tmux + 에이전트 세션 시작\n"+
			"/attach [이름] — 세션 재연결\n"+
			"/list — 전체 활성 세션 목록\n"+
			"/kill [이름] — 세션 종료\n"+
			"/status — 현재 채팅에 바인딩된 세션 정보\n\n"+
			"<b>메시지 전달</b>\n"+
			"/send &lt;텍스트&gt; — 에이전트 슬래시 커맨드 패스스루\n"+
			"/resend — 마지막으로 전송된 응답 메시지 재전송\n"+
			"/clear — 에이전트 대화 히스토리 초기화\n"+
			"/screen — 현재 tmux 화면 캡처\n"+
			"일반 텍스트 — 바인딩된 세션으로 바로 전달\n\n"+
			"<b>설정</b>\n"+
			"/level — 수신 메시지 수준 조회/변경 (all/interactive/final)\n\n"+
			"<b>기타</b>\n"+
			"/shutdown — remode 봇 프로세스 종료\n"+
			"/help — 이 도움말",
	)
}

// ── text message handler ──────────────────────────────────────────────────────

func handleText(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	text := upd.Message.Text
	if strings.HasPrefix(text, "/") {
		replyHTML(bd.Bot, upd.Message.Chat.ID,
			"알 수 없는 명령입니다. <code>/send /plan</code> 이나 <code>\\/plan</code> 형식으로 커맨드를 전달하세요.")
		return
	}
	if strings.HasPrefix(text, "\\") {
		text = text[1:]
	}
	sendToSession(ctx, upd.Message.Chat.ID, text, bd)
}

// ── callback query handler ────────────────────────────────────────────────────

func handleCallback(ctx context.Context, upd tgbotapi.Update, bd *BotData) {
	query := upd.CallbackQuery
	bd.Bot.Request(tgbotapi.NewCallback(query.ID, "")) //nolint:errcheck

	data := query.Data
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 {
		return
	}
	shortID, kind, value := parts[0], parts[1], parts[2]
	chatID := query.Message.Chat.ID
	msgID := query.Message.MessageID
	sm := bd.SM

	editText := func(text string) {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = "HTML"
		bd.Bot.Send(edit) //nolint:errcheck
	}

	switch kind {
	case "agent_projects":
		showProjectList(ctx, chatID, value, bd, &msgID)

	case "agent_new":
		pending := bd.pendingNew
		bd.pendingNew = nil
		if pending == nil {
			editText("⚠️ 세션 정보가 만료됐습니다. /new 를 다시 실행하세요.")
			return
		}
		editText(fmt.Sprintf("⏳ 세션 <b>%s</b> 시작 중…", html.EscapeString(pending.name)))
		if _, err := sm.Create(ctx, pending.name, pending.cwd, query.From.ID, value); err != nil {
			log.Printf("세션 생성 실패: %v", err)
			editText(fmt.Sprintf("❌ 실패: %v", err))
		} else {
			editText(fmt.Sprintf("✅ 세션 <b>%s</b> 준비됨", html.EscapeString(pending.name)))
		}

	case "project":
		if bd.projList == nil {
			editText("⚠️ 프로젝트 정보가 만료됐습니다. /projects 를 다시 실행하세요.")
			return
		}
		var idx int
		fmt.Sscanf(value, "%d", &idx)
		if idx < 0 || idx >= len(bd.projList.projects) {
			editText("⚠️ 프로젝트 정보가 만료됐습니다. /projects 를 다시 실행하세요.")
			return
		}
		proj := bd.projList.projects[idx]
		if len(proj.Sessions) == 0 || proj.CWD == "" {
			editText("⚠️ 세션이 없는 프로젝트입니다.")
			return
		}
		bd.projSelected = &projSelectedState{agentType: bd.projList.agentType, project: proj}
		var rows [][]tgbotapi.InlineKeyboardButton
		for j, s := range proj.Sessions {
			if j >= 5 {
				break
			}
			ts := s.CreatedAt
			if len(ts) > 16 {
				ts = ts[:16]
			}
			label := ts
			if s.Title != "" {
				label = s.Title + "  " + ts
			}
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("_:resume:%d", j)),
			))
		}
		edit := tgbotapi.NewEditMessageText(chatID, msgID,
			fmt.Sprintf("<b>%s</b>\n세션을 선택하세요:", html.EscapeString(proj.DisplayPath)))
		edit.ParseMode = "HTML"
		markup := tgbotapi.NewInlineKeyboardMarkup(rows...)
		edit.ReplyMarkup = &markup
		bd.Bot.Send(edit) //nolint:errcheck

	case "resume":
		selected := bd.projSelected
		if selected == nil || selected.project.CWD == "" {
			editText("⚠️ 프로젝트 정보가 만료됐습니다. /projects 를 다시 실행하세요.")
			return
		}
		var jdx int
		fmt.Sscanf(value, "%d", &jdx)
		if jdx < 0 || jdx >= len(selected.project.Sessions) {
			editText("⚠️ 세션 정보가 만료됐습니다.")
			return
		}
		sessInfo := selected.project.Sessions[jdx]
		base := pathBase(selected.project.CWD)
		if base == "" {
			base = "session"
		}
		name := base
		for i := 2; sm.Get(name) != nil; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		editText(fmt.Sprintf("⏳ <b>%s</b> 재개 중…", html.EscapeString(name)))
		if _, err := sm.Resume(ctx, name, selected.project.CWD, query.From.ID, sessInfo.SessionID, selected.agentType); err != nil {
			log.Printf("세션 재개 실패: %v", err)
			editText(fmt.Sprintf("❌ 실패: %v", err))
		} else {
			editText(fmt.Sprintf("✅ 세션 <b>%s</b> 준비됨", html.EscapeString(name)))
		}

	case "attach":
		if _, err := sm.Attach(ctx, value, query.From.ID); err != nil {
			editText(fmt.Sprintf("세션 <b>%s</b>을 찾을 수 없습니다.", html.EscapeString(value)))
		} else {
			editText(fmt.Sprintf("✅ 세션 <b>%s</b>에 연결됨", html.EscapeString(value)))
		}

	case "kill":
		if err := sm.Kill(ctx, value); err != nil {
			editText(fmt.Sprintf("세션 <b>%s</b>을 찾을 수 없습니다.", html.EscapeString(value)))
		} else {
			editText(fmt.Sprintf("🗑️ 세션 <b>%s</b> 종료됨", html.EscapeString(value)))
		}

	case "session_switch":
		sess := sm.GetByChat(query.From.ID)
		if sess == nil {
			editText("바인딩된 세션이 없습니다.")
			return
		}
		if value == sess.SessionID {
			editText("이미 해당 세션에 연결되어 있습니다.")
			return
		}
		name, cwd, fromChatID, agentType := sess.Name, sess.CWD, sess.ChatID, sess.AgentType
		editText("⏳ 세션 전환 중…")
		sm.Kill(ctx, name)  //nolint:errcheck
		if _, err := sm.Resume(ctx, name, cwd, fromChatID, value, agentType); err != nil {
			log.Printf("세션 전환 실패: %v", err)
			editText(fmt.Sprintf("❌ 실패: %v", err))
		} else {
			editText("✅ 세션 전환 완료")
		}

	case "shutdown":
		if value == "confirm" {
			editText("🔴 remode 봇을 종료합니다…")
			if bd.StopCh != nil {
				close(bd.StopCh)
			}
		} else {
			editText("취소되었습니다.")
		}

	case "key":
		// callback_data: "{short_session_name}:key:{key_value}"
		for _, s := range sm.ListAll() {
			short := s.Name
			if len(short) > 20 {
				short = short[:20]
			}
			if short == shortID {
				sm.SendKey(s, value) //nolint:errcheck
				break
			}
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func sendToSession(ctx context.Context, chatID int64, text string, bd *BotData) {
	sess := bd.SM.GetByChat(chatID)
	if sess == nil {
		replyHTML(bd.Bot, chatID, "이 채팅에 바인딩된 세션이 없습니다. /new 또는 /attach 로 세션을 만드세요.")
		return
	}
	if err := bd.SM.SendInput(sess, text); err != nil {
		replyHTML(bd.Bot, chatID, fmt.Sprintf("❌ 전송 실패: %v", err))
	}
}

func stripUISeparators(text string) string {
	var result []string
	prevBlank := false
	for _, line := range strings.Split(text, "\n") {
		if uiSeparatorRE.MatchString(strings.TrimSpace(line)) {
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

type ctxStats struct {
	msgCount  int
	ctxTokens int
}

func readContextStats(jsonlPath string) *ctxStats {
	if jsonlPath == "" {
		return nil
	}
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	data, _ := io.ReadAll(f)
	type entry struct {
		idx, out int
	}
	var entries []entry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e["type"] != "assistant" {
			continue
		}
		msg, _ := e["message"].(map[string]any)
		usage, _ := msg["usage"].(map[string]any)
		if usage == nil {
			continue
		}
		in := int(jsonFloat(usage, "input_tokens"))
		cr := int(jsonFloat(usage, "cache_read_input_tokens"))
		cc := int(jsonFloat(usage, "cache_creation_input_tokens"))
		out := int(jsonFloat(usage, "output_tokens"))
		if in+cr+cc > 0 || out > 0 {
			entries = append(entries, entry{in + cr + cc, out})
		}
	}
	if len(entries) == 0 {
		return &ctxStats{}
	}
	sessionStart := 0
	for i := 1; i < len(entries); i++ {
		if entries[i].idx < entries[i-1].idx {
			sessionStart = i
		}
	}
	current := entries[sessionStart:]
	return &ctxStats{
		msgCount:  len(current),
		ctxTokens: current[len(current)-1].idx,
	}
}

func jsonFloat(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

func fmtK(n int) string {
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func pathBase(p string) string {
	p = strings.TrimRight(p, "/")
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}
