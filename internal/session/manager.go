// Package session orchestrates agent sessions and routes events to the chat platform.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/formatter"
	"github.com/pickmoment/remode/internal/store"
)

// agentTmuxTag maps agent_type to the tmux session name tag.
var agentTmuxTag = map[string]string{
	"claude_code": "CL",
	"codex":       "CX",
}

// Config holds the configuration needed by the Manager.
type Config struct {
	TmuxSessionPrefix string
	SessionsDir       string // directory for per-session settings files
	ClaudeProjectsDir string // ~/.claude/projects
	NewProjectDir     string // base directory stripped from DisplayPath in project lists
	MessageLevel      string // default for new sessions
	JSONLSettleMS     int
}

// Manager creates, resumes, and monitors agent sessions.
type Manager struct {
	cfg      Config
	store    store.SessionStore
	agents   map[string]core.AIAgent
	rootCtx  context.Context // long-lived; parents all session tasks

	platformsMu sync.RWMutex
	platforms   map[string]core.ChatPlatform // keyed by transport ("telegram"|"discord"|"web")

	mu          sync.RWMutex
	sessions    map[string]*core.Session
	chatToSess  map[int64]string
	cancelFuncs map[string]context.CancelFunc

	obsMu     sync.RWMutex
	observers []func(name string, ev core.AgentEvent)
}

// New creates a Manager. agents must have at least one entry.
// rootCtx is used as the parent for all session task goroutines so that
// session watchers outlive any individual chat-platform driver.
func New(cfg Config, st store.SessionStore, agents map[string]core.AIAgent, rootCtx context.Context) *Manager {
	return &Manager{
		cfg:         cfg,
		store:       st,
		agents:      agents,
		rootCtx:     rootCtx,
		platforms:   make(map[string]core.ChatPlatform),
		sessions:    make(map[string]*core.Session),
		chatToSess:  make(map[int64]string),
		cancelFuncs: make(map[string]context.CancelFunc),
	}
}

// RegisterPlatform registers a ChatPlatform for the given transport.
// Must be called before Startup so that restored sessions can receive output.
func (m *Manager) RegisterPlatform(transport string, p core.ChatPlatform) {
	m.platformsMu.Lock()
	defer m.platformsMu.Unlock()
	m.platforms[transport] = p
}

// platformFor returns the ChatPlatform for the session's transport, or nil if not registered.
// Callers must nil-check before calling Send.
func (m *Manager) platformFor(sess *core.Session) core.ChatPlatform {
	m.platformsMu.RLock()
	defer m.platformsMu.RUnlock()
	return m.platforms[sess.Transport]
}

// RegisterObserver registers a callback that is called for every agent event
// on any session. Used by the scheduler and orchestrator.
func (m *Manager) RegisterObserver(fn func(name string, ev core.AgentEvent)) {
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	m.observers = append(m.observers, fn)
}

func (m *Manager) notifyObservers(name string, ev core.AgentEvent) {
	m.obsMu.RLock()
	defer m.obsMu.RUnlock()
	for _, fn := range m.observers {
		fn(name, ev)
	}
}

// EnabledAgents returns the registered agent type names.
func (m *Manager) EnabledAgents() []string {
	keys := make([]string, 0, len(m.agents))
	for k := range m.agents {
		keys = append(keys, k)
	}
	return keys
}

// ── startup ───────────────────────────────────────────────────────────────────

// Startup restores sessions from the store, reconnecting or resuming each one.
func (m *Manager) Startup(ctx context.Context) error {
	sessions, err := m.store.List()
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	for _, sess := range sessions {
		agent := m.agentFor(sess)
		if agent.Exists(sess) {
			// Check for a newer JSONL
			if sess.SessionID != "" {
				newest := agent.NewestOutput(sess, m.cfg.ClaudeProjectsDir)
				if newest != "" && newest != sess.JSONLPath {
					log.Printf("session %s: newer output %s, switching", sess.Name, filepath.Base(newest))
					sess.SessionID = withoutExt(filepath.Base(newest))
					sess.JSONLPath = newest
					sess.JSONLOffset = 0
					m.store.Save(sess) //nolint:errcheck
				}
			}
			m.register(sess)
			existing := agent.OutputSnapshot(sess, m.cfg.ClaudeProjectsDir)
			m.startTasks(ctx, sess, existing)
			log.Printf("reconnected to session %s", sess.Name)
		} else if dirExists(sess.CWD) {
			m.restoreSession(ctx, sess)
		} else {
			m.store.Delete(sess.Name) //nolint:errcheck
			log.Printf("session %s: directory gone, removed", sess.Name)
			if p := m.platformFor(sess); p != nil {
				p.Send(ctx, sess.ChatID, core.Message{ //nolint:errcheck
					Text: fmt.Sprintf("⚠️ 세션 **%s**의 디렉터리가 없어 삭제했습니다.", sess.Name),
				}, "")
			}
		}
	}
	return nil
}

// ── session CRUD ──────────────────────────────────────────────────────────────

// Create starts a new agent session for cwd.
// transport must be "telegram", "discord", or "web".
func (m *Manager) Create(ctx context.Context, name, cwd string, chatID int64, agentType, transport string) (*core.Session, error) {
	actual := m.resolveAgentType(agentType)
	agent := m.agents[actual]
	tag := agentTmuxTag[actual]
	if tag == "" {
		tag = actual
	}

	sess := &core.Session{
		Name:      name,
		TmuxName:  m.cfg.TmuxSessionPrefix + tag + "-" + name,
		CWD:       cwd,
		ChatID:    chatID,
		CreatedAt: time.Now(),
		Level:     core.MessageLevel(m.cfg.MessageLevel),
		AgentType: actual,
		Transport: transport,
	}

	existing := agent.OutputSnapshot(sess, m.cfg.ClaudeProjectsDir)
	settingsPath, err := m.writeSessionSettings(sess)
	if err != nil {
		return nil, fmt.Errorf("write settings: %w", err)
	}
	if err := agent.Create(ctx, sess, settingsPath); err != nil {
		return nil, fmt.Errorf("agent create: %w", err)
	}
	if err := m.store.Save(sess); err != nil {
		return nil, fmt.Errorf("store save: %w", err)
	}
	m.register(sess)
	m.startTasks(ctx, sess, existing)
	return sess, nil
}

// Resume starts a session attached to an existing agent session ID.
// transport must be "telegram", "discord", or "web".
func (m *Manager) Resume(ctx context.Context, name, cwd string, chatID int64, sessionID, agentType, transport string) (*core.Session, error) {
	actual := m.resolveAgentType(agentType)
	agent := m.agents[actual]
	tag := agentTmuxTag[actual]
	if tag == "" {
		tag = actual
	}
	projDir := agent.ProjectDirFor(cwd, m.cfg.ClaudeProjectsDir)
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	var offset int64
	if info, err := os.Stat(jsonlPath); err == nil {
		offset = info.Size()
	}

	sess := &core.Session{
		Name:        name,
		TmuxName:    m.cfg.TmuxSessionPrefix + tag + "-" + name,
		SessionID:   sessionID,
		CWD:         cwd,
		JSONLPath:   jsonlPath,
		ChatID:      chatID,
		CreatedAt:   time.Now(),
		Level:       core.MessageLevel(m.cfg.MessageLevel),
		JSONLOffset: offset,
		AgentType:   actual,
		Transport:   transport,
	}

	settingsPath, err := m.writeSessionSettings(sess)
	if err != nil {
		return nil, fmt.Errorf("write settings: %w", err)
	}
	if err := agent.Resume(ctx, sess, settingsPath, sessionID); err != nil {
		return nil, fmt.Errorf("agent resume: %w", err)
	}
	if err := m.store.Save(sess); err != nil {
		return nil, fmt.Errorf("store save: %w", err)
	}
	m.register(sess)
	m.startTasks(ctx, sess, nil)
	return sess, nil
}

// Attach rebinds a session to a different chat.
// For web-transport sessions, only ChatID is updated; chatToSess is not modified.
func (m *Manager) Attach(ctx context.Context, name string, chatID int64) (*core.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[name]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", name)
	}
	if sess.Transport != "web" {
		old := sess.ChatID
		delete(m.chatToSess, old)
		m.chatToSess[chatID] = name
	}
	sess.ChatID = chatID
	m.store.UpdateChatID(name, chatID) //nolint:errcheck
	return sess, nil
}

// Kill stops a session and removes it from the store.
func (m *Manager) Kill(ctx context.Context, name string) error {
	m.mu.Lock()
	sess, ok := m.sessions[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session not found: %s", name)
	}
	delete(m.sessions, name)
	if sess.Transport != "web" {
		delete(m.chatToSess, sess.ChatID)
	}
	if cancel, ok := m.cancelFuncs[name]; ok {
		cancel()
		delete(m.cancelFuncs, name)
	}
	m.mu.Unlock()

	m.agentFor(sess).Kill(sess) //nolint:errcheck
	m.store.Delete(name)        //nolint:errcheck
	return nil
}

// ── queries ───────────────────────────────────────────────────────────────────

func (m *Manager) GetByChat(chatID int64) *core.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	name := m.chatToSess[chatID]
	if name == "" {
		return nil
	}
	return m.sessions[name]
}

func (m *Manager) Get(name string) *core.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[name]
}

func (m *Manager) GetBySessionID(sessionID string) *core.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if s.SessionID == sessionID {
			return s
		}
	}
	return nil
}

func (m *Manager) ListAll() []*core.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*core.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ── agent pass-throughs ───────────────────────────────────────────────────────

func (m *Manager) SendInput(sess *core.Session, text string) error {
	return m.agentFor(sess).SendInput(sess, text)
}

// SendInputBy resolves a session by name and sends input. Useful for the
// scheduler and orchestrator which address sessions by name.
func (m *Manager) SendInputBy(name, text string) error {
	sess := m.Get(name)
	if sess == nil {
		return fmt.Errorf("session not found: %s", name)
	}
	return m.agentFor(sess).SendInput(sess, text)
}

func (m *Manager) SendKey(sess *core.Session, key string) error {
	return m.agentFor(sess).SendKey(sess, key)
}

func (m *Manager) Capture(sess *core.Session) (string, error) {
	return m.agentFor(sess).Capture(sess)
}

// DeliverToSession sends a message to a session via its registered transport platform.
// Used by the scheduler for status reports so it never calls platform.Send directly.
func (m *Manager) DeliverToSession(ctx context.Context, name string, msg core.Message) error {
	sess := m.Get(name)
	if sess == nil {
		return fmt.Errorf("session not found: %s", name)
	}
	m.sendSessionMsg(ctx, sess, msg)
	return nil
}

func (m *Manager) ListProjects(agentType string) ([]core.Project, error) {
	projects, err := m.agents[m.resolveAgentType(agentType)].ListProjects(m.cfg.ClaudeProjectsDir)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(m.cfg.NewProjectDir, "/")
	for i := range projects {
		p := &projects[i]
		if base != "" && strings.HasPrefix(p.DisplayPath, base+"/") {
			p.DisplayPath = p.DisplayPath[len(base)+1:]
		}
	}
	return projects, nil
}

func (m *Manager) ListProjectSessions(sess *core.Session) []core.ProjectSession {
	agent := m.agentFor(sess)
	projects, _ := agent.ListProjects(m.cfg.ClaudeProjectsDir)
	for _, p := range projects {
		if p.CWD == sess.CWD {
			return p.Sessions
		}
	}
	return nil
}

func (m *Manager) ProjectDirFor(cwd, agentType string) string {
	return m.agents[m.resolveAgentType(agentType)].ProjectDirFor(cwd, m.cfg.ClaudeProjectsDir)
}

func (m *Manager) SetMessageLevel(ctx context.Context, sess *core.Session, level string) error {
	sess.Level = core.MessageLevel(level)
	return m.store.UpdateMessageLevel(sess.Name, level)
}

// ── private ───────────────────────────────────────────────────────────────────

// register inserts sess into the in-memory maps.
// For web-transport sessions, chatToSess is NOT updated because web sessions
// are addressed by Name, not by ChatID.
func (m *Manager) register(sess *core.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[sess.Name] = sess
	if sess.Transport != "web" {
		m.chatToSess[sess.ChatID] = sess.Name
	}
}

func (m *Manager) startTasks(ctx context.Context, sess *core.Session, existing map[string]struct{}) {
	// Use m.rootCtx (not the caller's ctx) so that session watchers outlive
	// any individual chat-platform driver (e.g. bot shutdown).
	taskCtx, cancel := context.WithCancel(m.rootCtx)

	m.mu.Lock()
	if old, ok := m.cancelFuncs[sess.Name]; ok {
		old()
	}
	m.cancelFuncs[sess.Name] = cancel
	m.mu.Unlock()

	go func() {
		// Discovery phase
		if sess.SessionID == "" {
			if existing == nil {
				existing = map[string]struct{}{}
			}
			sid, path, err := m.agentFor(sess).Discover(taskCtx, sess, existing, m.cfg.ClaudeProjectsDir)
			if err != nil {
				return
			}
			sess.SessionID = sid
			sess.JSONLPath = path
			m.store.Save(sess) //nolint:errcheck
			log.Printf("discovered output for %s: %s", sess.Name, sid)
		}

		onEvent := func(event core.AgentEvent) {
			// Notify scheduler/orchestrator observers before formatting/routing.
			m.notifyObservers(sess.Name, event)

			msgs := formatter.FormatEvent(event)
			var toSend []core.Message
			for _, msg := range msgs {
				if m.shouldSend(msg.Category, sess) {
					toSend = append(toSend, msg)
				}
			}
			if len(toSend) > 0 {
				sess.LastOutbound = toSend
			}
			for _, msg := range toSend {
				m.sendSessionMsg(taskCtx, sess, msg)
			}
		}

		err := m.agentFor(sess).WatchEvents(taskCtx, sess, onEvent, m.store, m.cfg.JSONLSettleMS)
		if err != nil && taskCtx.Err() == nil {
			log.Printf("watch_events ended for %s: %v", sess.Name, err)
		}
	}()
}

func (m *Manager) restoreSession(ctx context.Context, sess *core.Session) {
	if info, err := os.Stat(sess.JSONLPath); err == nil {
		sess.JSONLOffset = info.Size()
	}
	resumeID := sess.SessionID
	agent := m.agentFor(sess)

	settingsPath, err := m.writeSessionSettings(sess)
	if err != nil {
		log.Printf("restore %s: write settings: %v", sess.Name, err)
		return
	}
	if resumeID != "" {
		err = agent.Resume(ctx, sess, settingsPath, resumeID)
	} else {
		err = agent.Create(ctx, sess, settingsPath)
	}
	if err != nil {
		log.Printf("restore %s: %v", sess.Name, err)
		return
	}
	m.register(sess)
	m.startTasks(ctx, sess, nil)
	log.Printf("restored session %s", sess.Name)
	if p := m.platformFor(sess); p != nil {
		p.Send(ctx, sess.ChatID, core.Message{ //nolint:errcheck
			Text: fmt.Sprintf("🔄 세션 **%s**을 복구했습니다.", sess.Name),
		}, "")
	}
}

func (m *Manager) sendSessionMsg(ctx context.Context, sess *core.Session, msg core.Message) {
	// Prefix action IDs with session name for callback routing
	if len(msg.Actions) > 0 {
		short := sess.Name
		if len(short) > 20 {
			short = short[:20]
		}
		prefixed := make([][]core.Action, len(msg.Actions))
		for i, row := range msg.Actions {
			prefixed[i] = make([]core.Action, len(row))
			for j, a := range row {
				prefixed[i][j] = core.Action{Label: a.Label, ActionID: short + ":" + a.ActionID}
			}
		}
		msg = core.Message{Text: msg.Text, Actions: prefixed, Category: msg.Category, Preformatted: msg.Preformatted}
	}

	// Show session name as prefix only when multiple sessions share the chat
	var prefix string
	m.mu.RLock()
	count := 0
	for _, s := range m.sessions {
		if s.ChatID == sess.ChatID {
			count++
		}
	}
	m.mu.RUnlock()
	if count > 1 {
		prefix = sess.Name
	}

	p := m.platformFor(sess)
	if p == nil {
		// Platform not registered (e.g. web disabled, or session from previous run).
		return
	}
	if err := p.Send(ctx, sess.ChatID, msg, prefix); err != nil {
		log.Printf("platform send error (session %s): %v", sess.Name, err)
	}

	// Mirror to web platform for non-web sessions so the web UI can display output.
	if sess.Transport != "web" {
		m.platformsMu.RLock()
		webP := m.platforms["web"]
		m.platformsMu.RUnlock()
		if webP != nil {
			webP.Send(ctx, sess.ChatID, msg, prefix) //nolint:errcheck
		}
	}
}

func (m *Manager) shouldSend(category core.MessageCategory, sess *core.Session) bool {
	switch sess.Level {
	case core.LevelAll:
		return true
	case core.LevelInteractive:
		return category != core.CategoryTool
	default: // LevelFinal
		return category == core.CategoryText
	}
}

func (m *Manager) writeSessionSettings(sess *core.Session) (string, error) {
	dir := filepath.Join(m.cfg.SessionsDir, sess.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "settings.json")
	data, _ := json.Marshal(map[string]any{})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (m *Manager) agentFor(sess *core.Session) core.AIAgent {
	if a, ok := m.agents[sess.AgentType]; ok {
		return a
	}
	for _, a := range m.agents {
		return a
	}
	panic("no agents registered")
}

func (m *Manager) resolveAgentType(agentType string) string {
	if _, ok := m.agents[agentType]; ok {
		return agentType
	}
	for k := range m.agents {
		return k
	}
	panic("no agents registered")
}

// ── helpers ───────────────────────────────────────────────────────────────────

func withoutExt(name string) string {
	ext := filepath.Ext(name)
	return name[:len(name)-len(ext)]
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
