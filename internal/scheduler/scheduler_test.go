package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/scheduler"
	memstore "github.com/pickmoment/remode/internal/store/memory"
)

// ── fake clock ─────────────────────────────────────────────────────────────────

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }
func (c *fakeClock) Now() time.Time       { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Set(t time.Time)      { c.mu.Lock(); defer c.mu.Unlock(); c.now = t }

// ── mock driver ────────────────────────────────────────────────────────────────

type mockDriver struct {
	mu       sync.Mutex
	inputs   map[string][]string // sessionName → inputs
	kills    []string
	creates  []*core.Session
	delivers []core.Message
	sessions map[string]*core.Session
}

func newMockDriver() *mockDriver {
	return &mockDriver{
		inputs:   make(map[string][]string),
		sessions: make(map[string]*core.Session),
	}
}

func (d *mockDriver) SendInputBy(name, text string) error {
	d.mu.Lock(); defer d.mu.Unlock()
	d.inputs[name] = append(d.inputs[name], text)
	return nil
}

func (d *mockDriver) Capture(sess *core.Session) (string, error) {
	return "mock screen output", nil
}

func (d *mockDriver) Get(name string) *core.Session {
	d.mu.Lock(); defer d.mu.Unlock()
	return d.sessions[name]
}

func (d *mockDriver) Create(_ context.Context, name, cwd string, chatID int64, agentType, transport string) (*core.Session, error) {
	d.mu.Lock(); defer d.mu.Unlock()
	sess := &core.Session{Name: name, CWD: cwd, ChatID: chatID, AgentType: agentType, Transport: transport}
	d.sessions[name] = sess
	d.creates = append(d.creates, sess)
	return sess, nil
}

func (d *mockDriver) Kill(_ context.Context, name string) error {
	d.mu.Lock(); defer d.mu.Unlock()
	d.kills = append(d.kills, name)
	delete(d.sessions, name)
	return nil
}

func (d *mockDriver) DeliverToSession(_ context.Context, name string, msg core.Message) error {
	d.mu.Lock(); defer d.mu.Unlock()
	d.delivers = append(d.delivers, msg)
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func makeSchedule(action core.ScheduleAction, target, payload string) *core.Schedule {
	return &core.Schedule{
		ID:            "test-id",
		Name:          "test-sched",
		CronSpec:      "0 9 * * * *", // daily 9am (6-field)
		Action:        action,
		TargetSession: target,
		Payload:       payload,
		Enabled:       true,
		CreatedAt:     time.Now(),
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestFire_SendPrompt_DeliversInput verifies that fire(send_prompt) calls SendInputBy
// on the target session with the configured payload.
func TestFire_SendPrompt_DeliversInput(t *testing.T) {
	st := memstore.NewScheduleStore()
	drv := newMockDriver()
	clk := newFakeClock(time.Now())

	// Pre-register the target session so Get returns it.
	drv.sessions["mysess"] = &core.Session{Name: "mysess"}

	sc := makeSchedule(core.ActionSendPrompt, "mysess", "오늘 할 일 정리해줘")
	require.NoError(t, st.SaveSchedule(sc))

	s := scheduler.New(st, drv, clk)
	require.NoError(t, s.Fire(context.Background(), sc.ID))

	drv.mu.Lock()
	defer drv.mu.Unlock()
	assert.Equal(t, []string{"오늘 할 일 정리해줘"}, drv.inputs["mysess"])
}

// TestFire_SendPrompt_SessionGone is a no-op (doesn't error) when target is gone.
func TestFire_SendPrompt_SessionGone(t *testing.T) {
	st := memstore.NewScheduleStore()
	drv := newMockDriver()
	sc := makeSchedule(core.ActionSendPrompt, "gone", "prompt")
	require.NoError(t, st.SaveSchedule(sc))

	s := scheduler.New(st, drv, nil)
	require.NoError(t, s.Fire(context.Background(), sc.ID), "should not error when session is gone")
}

// TestFire_StatusReport_DeliversMessage verifies that fire(status_report) captures
// the screen and calls DeliverToSession.
func TestFire_StatusReport_DeliversMessage(t *testing.T) {
	st := memstore.NewScheduleStore()
	drv := newMockDriver()
	drv.sessions["mysess"] = &core.Session{Name: "mysess"}

	sc := makeSchedule(core.ActionStatusReport, "mysess", "")
	require.NoError(t, st.SaveSchedule(sc))

	s := scheduler.New(st, drv, nil)
	require.NoError(t, s.Fire(context.Background(), sc.ID))

	drv.mu.Lock()
	defer drv.mu.Unlock()
	require.Len(t, drv.delivers, 1)
	assert.Contains(t, drv.delivers[0].Text, "mock screen output")
}

// TestFire_BatchSession_CreatesAndKills verifies create + deadline kill.
func TestFire_BatchSession_CreatesAndKills(t *testing.T) {
	st := memstore.NewScheduleStore()
	drv := newMockDriver()

	sc := &core.Schedule{
		ID:        "batch-id",
		Name:      "daily-job",
		CronSpec:  "0 0 * * * *",
		Action:    core.ActionBatchSession,
		Template:  core.SessionTemplate{CWD: "/tmp/batchwork", AgentType: "claude_code"},
		InitialPrompt: "배치 작업 시작",
		DeadlineSecs:  1, // very short deadline for test
		Enabled:   true,
		CreatedAt: time.Now(),
	}
	require.NoError(t, st.SaveSchedule(sc))

	s := scheduler.New(st, drv, nil)
	require.NoError(t, s.Fire(context.Background(), sc.ID))

	drv.mu.Lock()
	require.Len(t, drv.creates, 1)
	sessName := drv.creates[0].Name
	assert.Equal(t, "/tmp/batchwork", drv.creates[0].CWD)
	assert.Equal(t, "web", drv.creates[0].Transport)
	drv.mu.Unlock()

	// The initial prompt should be sent to the new session.
	// Small delay to allow the goroutine to run.
	time.Sleep(5 * time.Millisecond)
	drv.mu.Lock()
	assert.Contains(t, drv.inputs[sessName], "배치 작업 시작")
	drv.mu.Unlock()

	// Deadline goroutine fires after 1s; wait for it.
	time.Sleep(1100 * time.Millisecond)
	drv.mu.Lock()
	defer drv.mu.Unlock()
	assert.Contains(t, drv.kills, sessName)
}

// TestFire_Disabled_NoOp verifies that a disabled schedule is not fired.
func TestFire_Disabled_NoOp(t *testing.T) {
	st := memstore.NewScheduleStore()
	drv := newMockDriver()
	drv.sessions["s"] = &core.Session{Name: "s"}

	sc := makeSchedule(core.ActionSendPrompt, "s", "should not send")
	sc.Enabled = false
	require.NoError(t, st.SaveSchedule(sc))

	s := scheduler.New(st, drv, nil)
	require.NoError(t, s.Fire(context.Background(), sc.ID))

	drv.mu.Lock()
	defer drv.mu.Unlock()
	assert.Empty(t, drv.inputs["s"])
}

// TestFire_UpdatesLastRun verifies that last_run and next_run are persisted.
func TestFire_UpdatesLastRun(t *testing.T) {
	st := memstore.NewScheduleStore()
	drv := newMockDriver()
	drv.sessions["s"] = &core.Session{Name: "s"}
	clk := newFakeClock(time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC))

	sc := makeSchedule(core.ActionSendPrompt, "s", "prompt")
	require.NoError(t, st.SaveSchedule(sc))

	s := scheduler.New(st, drv, clk)
	require.NoError(t, s.Fire(context.Background(), sc.ID))

	updated, err := st.GetSchedule(sc.ID)
	require.NoError(t, err)
	assert.Equal(t, clk.Now().UTC(), updated.LastRun.UTC())
	assert.True(t, updated.NextRun.After(clk.Now()), "next_run should be in the future")
}
