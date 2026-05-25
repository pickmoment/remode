package session_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/session"
	"github.com/pickmoment/remode/internal/store/memory"
)

// ── mock agent ────────────────────────────────────────────────────────────────

type mockAgent struct {
	mu       sync.Mutex
	created  []string
	inputs   []string
	keys     []string
	existing bool
}

func (a *mockAgent) Create(_ context.Context, sess *core.Session, _ string) error {
	a.mu.Lock()
	a.created = append(a.created, sess.Name)
	a.mu.Unlock()
	return nil
}
func (a *mockAgent) Resume(_ context.Context, sess *core.Session, _, _ string) error {
	a.mu.Lock()
	a.created = append(a.created, sess.Name)
	a.mu.Unlock()
	return nil
}
func (a *mockAgent) SendInput(sess *core.Session, text string) error {
	a.mu.Lock()
	a.inputs = append(a.inputs, text)
	a.mu.Unlock()
	return nil
}
func (a *mockAgent) SendKey(sess *core.Session, key string) error {
	a.mu.Lock()
	a.keys = append(a.keys, key)
	a.mu.Unlock()
	return nil
}
func (a *mockAgent) Capture(_ *core.Session) (string, error) { return "", nil }
func (a *mockAgent) Kill(_ *core.Session) error               { return nil }
func (a *mockAgent) Exists(_ *core.Session) bool              { return a.existing }
func (a *mockAgent) Discover(_ context.Context, sess *core.Session, _ map[string]struct{}, _ string) (string, string, error) {
	return "sess-001", "/tmp/sess-001.jsonl", nil
}
func (a *mockAgent) WatchEvents(ctx context.Context, _ *core.Session, _ func(core.AgentEvent), _ core.SessionUpdater, _ int) error {
	<-ctx.Done()
	return ctx.Err()
}
func (a *mockAgent) ListProjects(_ string) ([]core.Project, error) { return nil, nil }
func (a *mockAgent) ProjectDirFor(_, _ string) string              { return "/tmp/proj" }
func (a *mockAgent) NewestOutput(_ *core.Session, _ string) string { return "" }
func (a *mockAgent) OutputSnapshot(_ *core.Session, _ string) map[string]struct{} {
	return map[string]struct{}{}
}

// ── mock platform ─────────────────────────────────────────────────────────────

type mockPlatform struct {
	mu   sync.Mutex
	msgs []core.Message
}

func (p *mockPlatform) Send(_ context.Context, _ int64, msg core.Message, _ string) error {
	p.mu.Lock()
	p.msgs = append(p.msgs, msg)
	p.mu.Unlock()
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newManager(t *testing.T) (*session.Manager, *mockAgent, *mockPlatform) {
	t.Helper()
	agent := &mockAgent{}
	platform := &mockPlatform{}
	cfg := session.Config{
		TmuxSessionPrefix: "tc-",
		SessionsDir:       t.TempDir(),
		ClaudeProjectsDir: t.TempDir(),
		MessageLevel:      "all",
		JSONLSettleMS:     0,
	}
	mgr := session.New(cfg, memory.New(), map[string]core.AIAgent{"claude_code": agent}, platform)
	return mgr, agent, platform
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestCreate_RegistersSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, _, _ := newManager(t)
	sess, err := mgr.Create(ctx, "myproj", "/tmp/myproj", 42, "")
	require.NoError(t, err)
	assert.Equal(t, "myproj", sess.Name)
	assert.Equal(t, int64(42), sess.ChatID)

	got := mgr.Get("myproj")
	require.NotNil(t, got)
	assert.Equal(t, "myproj", got.Name)
}

func TestCreate_FindableByChat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, _, _ := newManager(t)
	mgr.Create(ctx, "proj", "/tmp/proj", 99, "") //nolint:errcheck

	got := mgr.GetByChat(99)
	require.NotNil(t, got)
	assert.Equal(t, "proj", got.Name)
}

func TestKill_RemovesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, _, _ := newManager(t)
	mgr.Create(ctx, "proj", "/tmp/proj", 5, "") //nolint:errcheck

	require.NoError(t, mgr.Kill(ctx, "proj"))
	assert.Nil(t, mgr.Get("proj"))
	assert.Nil(t, mgr.GetByChat(5))
}

func TestAttach_RebinskChat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, _, _ := newManager(t)
	mgr.Create(ctx, "proj", "/tmp/proj", 1, "") //nolint:errcheck

	_, err := mgr.Attach(ctx, "proj", 2)
	require.NoError(t, err)
	assert.Nil(t, mgr.GetByChat(1))
	assert.NotNil(t, mgr.GetByChat(2))
}

func TestSendInput_DelegatesToAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, agent, _ := newManager(t)
	sess, _ := mgr.Create(ctx, "p", "/tmp/p", 7, "")
	require.NoError(t, mgr.SendInput(sess, "hello"))

	agent.mu.Lock()
	defer agent.mu.Unlock()
	assert.Contains(t, agent.inputs, "hello")
}

func TestSendKey_DelegatesToAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, agent, _ := newManager(t)
	sess, _ := mgr.Create(ctx, "p", "/tmp/p", 8, "")
	require.NoError(t, mgr.SendKey(sess, "1"))

	agent.mu.Lock()
	defer agent.mu.Unlock()
	assert.Contains(t, agent.keys, "1")
}

func TestListAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, _, _ := newManager(t)
	mgr.Create(ctx, "a", "/tmp/a", 1, "") //nolint:errcheck
	mgr.Create(ctx, "b", "/tmp/b", 2, "") //nolint:errcheck

	list := mgr.ListAll()
	assert.Len(t, list, 2)
}

func TestSetMessageLevel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, _, _ := newManager(t)
	sess, _ := mgr.Create(ctx, "p", "/tmp/p", 3, "")
	require.NoError(t, mgr.SetMessageLevel(ctx, sess, "interactive"))
	assert.Equal(t, core.LevelInteractive, sess.Level)
}

func TestStartup_ReconnectsLiveSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := &mockAgent{existing: true}
	platform := &mockPlatform{}
	st := memory.New()
	cfg := session.Config{
		TmuxSessionPrefix: "tc-",
		SessionsDir:       t.TempDir(),
		ClaudeProjectsDir: t.TempDir(),
		MessageLevel:      "all",
	}

	// Pre-populate the store
	st.Save(&core.Session{ //nolint:errcheck
		Name:      "alive",
		TmuxName:  "tc-claude-alive",
		SessionID: "sess-xyz",
		CWD:       "/tmp/alive",
		JSONLPath: "/tmp/alive.jsonl",
		ChatID:    10,
		CreatedAt: time.Now(),
		Level:     core.LevelAll,
		AgentType: "claude_code",
	})

	mgr := session.New(cfg, st, map[string]core.AIAgent{"claude_code": agent}, platform)
	require.NoError(t, mgr.Startup(ctx))

	// A short wait for the background goroutine to register the session
	time.Sleep(10 * time.Millisecond)
	assert.NotNil(t, mgr.Get("alive"))
}
