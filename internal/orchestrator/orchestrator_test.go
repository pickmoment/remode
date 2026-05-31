package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/orchestrator"
	memstore "github.com/pickmoment/remode/internal/store/memory"
)

// ── mock driver ────────────────────────────────────────────────────────────────

type mockDriver struct {
	mu       sync.Mutex
	inputs   map[string][]string
	creates  []string
	kills    []string
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
	return "captured: " + sess.Name, nil
}

func (d *mockDriver) Get(name string) *core.Session {
	d.mu.Lock(); defer d.mu.Unlock()
	return d.sessions[name]
}

func (d *mockDriver) Create(_ context.Context, name, cwd string, chatID int64, agentType, transport string) (*core.Session, error) {
	d.mu.Lock(); defer d.mu.Unlock()
	sess := &core.Session{Name: name, CWD: cwd, Transport: transport}
	d.sessions[name] = sess
	d.creates = append(d.creates, name)
	return sess, nil
}

func (d *mockDriver) Kill(_ context.Context, name string) error {
	d.mu.Lock(); defer d.mu.Unlock()
	d.kills = append(d.kills, name)
	delete(d.sessions, name)
	return nil
}

// ── TurnTracker tests ─────────────────────────────────────────────────────────

func TestTurnTracker_EventText_BecomesActive(t *testing.T) {
	tr := orchestrator.NewTurnTracker(500, nil)
	tr.OnEvent("sess", core.AgentEvent{Type: core.EventText, Text: "hello"})
	assert.Equal(t, orchestrator.TurnActive, tr.State("sess"))
}

func TestTurnTracker_ApprovalPrompt_BecomesBlocked(t *testing.T) {
	tr := orchestrator.NewTurnTracker(500, nil)
	tr.OnEvent("sess", core.AgentEvent{Type: core.EventApprovalPrompt})
	assert.Equal(t, orchestrator.TurnBlocked, tr.State("sess"))
}

func TestTurnTracker_ForceIdle_NotifiesWaiter(t *testing.T) {
	tr := orchestrator.NewTurnTracker(60000, nil) // very high idle MS so ticker won't fire

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Pre-populate with active state
	tr.OnEvent("sess", core.AgentEvent{Type: core.EventText})

	done := make(chan error, 1)
	go func() {
		done <- tr.WaitIdle(ctx, "sess")
	}()

	// Simulate idle by force
	time.Sleep(10 * time.Millisecond)
	tr.ForceIdle("sess")

	err := <-done
	require.NoError(t, err)
	assert.Equal(t, orchestrator.TurnIdle, tr.State("sess"))
}

func TestTurnTracker_WaitIdle_CtxCancelled(t *testing.T) {
	tr := orchestrator.NewTurnTracker(60000, nil)
	tr.OnEvent("sess", core.AgentEvent{Type: core.EventText})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := tr.WaitIdle(ctx, "sess")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestTurnTracker_AlreadyIdle_ReturnsImmediately(t *testing.T) {
	tr := orchestrator.NewTurnTracker(10, nil)
	tr.ForceIdle("sess")

	ctx := context.Background()
	err := tr.WaitIdle(ctx, "sess")
	require.NoError(t, err)
}

// ── Broadcast tests ───────────────────────────────────────────────────────────

func TestBroadcast_SendsToAllSessions(t *testing.T) {
	tr := orchestrator.NewTurnTracker(100, nil)
	drv := newMockDriver()
	drv.sessions["a"] = &core.Session{Name: "a"}
	drv.sessions["b"] = &core.Session{Name: "b"}

	o := orchestrator.New(drv, tr)
	results := o.Broadcast(context.Background(), []string{"a", "b"}, "hello", false)

	assert.Len(t, results, 2)
	for _, r := range results {
		assert.NoError(t, r.Err)
	}

	drv.mu.Lock()
	defer drv.mu.Unlock()
	assert.Equal(t, []string{"hello"}, drv.inputs["a"])
	assert.Equal(t, []string{"hello"}, drv.inputs["b"])
}

// ── Chain tests ───────────────────────────────────────────────────────────────

func TestRunChain_SendsRenderedPrompt(t *testing.T) {
	tr := orchestrator.NewTurnTracker(100, nil)
	drv := newMockDriver()
	drv.sessions["src"] = &core.Session{Name: "src"}
	drv.sessions["dst"] = &core.Session{Name: "dst"}

	o := orchestrator.New(drv, tr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Trigger chain in background
	done := make(chan error, 1)
	go func() {
		done <- o.RunChain(ctx, orchestrator.ChainConfig{
			FromSession:    "src",
			ToSession:      "dst",
			PromptTemplate: "결과: {{output}}",
		})
	}()

	// Simulate src becoming idle
	time.Sleep(20 * time.Millisecond)
	tr.ForceIdle("src")

	require.NoError(t, <-done)

	drv.mu.Lock()
	defer drv.mu.Unlock()
	require.Len(t, drv.inputs["dst"], 1)
	assert.Contains(t, drv.inputs["dst"][0], "결과: captured: src")
}

// ── DAG tests ────────────────────────────────────────────────────────────────

func TestDAG_Diamond_ExecutesInOrder(t *testing.T) {
	ws := memstore.NewWorkflowStore()
	tr := orchestrator.NewTurnTracker(100, nil)
	drv := newMockDriver()

	// Pre-register sessions for all nodes
	for _, name := range []string{"A", "B", "C", "D"} {
		drv.sessions[name] = &core.Session{Name: name}
	}

	// Diamond: A→B, A→C, B→D, C→D
	wf := &core.Workflow{
		ID:      "wf-diamond",
		Name:    "diamond",
		Enabled: true,
		Nodes: []core.WorkflowNode{
			{ID: "n1", WorkflowID: "wf-diamond", NodeKey: "A", SessionName: "A", Prompt: "run A"},
			{ID: "n2", WorkflowID: "wf-diamond", NodeKey: "B", SessionName: "B", Prompt: "run B"},
			{ID: "n3", WorkflowID: "wf-diamond", NodeKey: "C", SessionName: "C", Prompt: "run C"},
			{ID: "n4", WorkflowID: "wf-diamond", NodeKey: "D", SessionName: "D", Prompt: "run D"},
		},
		Edges: []core.WorkflowEdge{
			{WorkflowID: "wf-diamond", FromNode: "A", ToNode: "B"},
			{WorkflowID: "wf-diamond", FromNode: "A", ToNode: "C"},
			{WorkflowID: "wf-diamond", FromNode: "B", ToNode: "D"},
			{WorkflowID: "wf-diamond", FromNode: "C", ToNode: "D"},
		},
		CreatedAt: time.Now(),
	}
	require.NoError(t, ws.SaveWorkflow(wf))

	dag := orchestrator.NewDAGEngine(ws, drv, tr)

	// Force idle for all sessions as soon as they receive input
	// (simulates agents completing immediately)
	go func() {
		for {
			time.Sleep(10 * time.Millisecond)
			drv.mu.Lock()
			for name := range drv.inputs {
				tr.ForceIdle(name)
			}
			drv.mu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runID, err := dag.StartRun(ctx, "wf-diamond")
	require.NoError(t, err)

	// Wait for the run to complete.
	var finalStatus core.WorkflowStatus
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		run, err := ws.GetWorkflowRun(runID)
		require.NoError(t, err)
		if run.Status == core.StatusDone || run.Status == core.StatusFailed {
			finalStatus = run.Status
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.Equal(t, core.StatusDone, finalStatus)

	// All four sessions must have received their prompts.
	drv.mu.Lock()
	defer drv.mu.Unlock()
	for _, name := range []string{"A", "B", "C", "D"} {
		assert.NotEmpty(t, drv.inputs[name], "session %s should have received a prompt", name)
	}
}

func TestDAG_CycleDetection(t *testing.T) {
	ws := memstore.NewWorkflowStore()
	tr := orchestrator.NewTurnTracker(100, nil)
	drv := newMockDriver()

	// A → B → A (cycle)
	wf := &core.Workflow{
		ID:      "wf-cycle",
		Name:    "cycle",
		Enabled: true,
		Nodes: []core.WorkflowNode{
			{ID: "n1", WorkflowID: "wf-cycle", NodeKey: "A", SessionName: "A"},
			{ID: "n2", WorkflowID: "wf-cycle", NodeKey: "B", SessionName: "B"},
		},
		Edges: []core.WorkflowEdge{
			{WorkflowID: "wf-cycle", FromNode: "A", ToNode: "B"},
			{WorkflowID: "wf-cycle", FromNode: "B", ToNode: "A"},
		},
		CreatedAt: time.Now(),
	}
	require.NoError(t, ws.SaveWorkflow(wf))

	dag := orchestrator.NewDAGEngine(ws, drv, tr)
	_, err := dag.StartRun(context.Background(), "wf-cycle")
	assert.Error(t, err, "should reject cyclic workflow")
}
