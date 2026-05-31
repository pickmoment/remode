package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/orchestrator"
	"github.com/pickmoment/remode/internal/platform/web"
	"github.com/pickmoment/remode/internal/scheduler"
	"github.com/pickmoment/remode/internal/session"
	memstore "github.com/pickmoment/remode/internal/store/memory"
)

// ── mock agent (same pattern as manager_test.go) ──────────────────────────────

type mockAgent struct{}

func (a *mockAgent) Create(_ context.Context, _ *core.Session, _ string) error  { return nil }
func (a *mockAgent) Resume(_ context.Context, _ *core.Session, _, _ string) error { return nil }
func (a *mockAgent) SendInput(_ *core.Session, _ string) error                    { return nil }
func (a *mockAgent) SendKey(_ *core.Session, _ string) error                      { return nil }
func (a *mockAgent) Capture(_ *core.Session) (string, error)                      { return "screen", nil }
func (a *mockAgent) Kill(_ *core.Session) error                                   { return nil }
func (a *mockAgent) Exists(_ *core.Session) bool                                  { return false }
func (a *mockAgent) Discover(_ context.Context, _ *core.Session, _ map[string]struct{}, _ string) (string, string, error) {
	return "sid", "/tmp/out.jsonl", nil
}
func (a *mockAgent) WatchEvents(ctx context.Context, _ *core.Session, _ func(core.AgentEvent), _ core.SessionUpdater, _ int) error {
	<-ctx.Done(); return ctx.Err()
}
func (a *mockAgent) ListProjects(_ string) ([]core.Project, error)         { return nil, nil }
func (a *mockAgent) ProjectDirFor(_, _ string) string                      { return "/tmp" }
func (a *mockAgent) NewestOutput(_ *core.Session, _ string) string         { return "" }
func (a *mockAgent) OutputSnapshot(_ *core.Session, _ string) map[string]struct{} {
	return map[string]struct{}{}
}

// ── test helpers ──────────────────────────────────────────────────────────────

const testToken = "secret-token"

// buildTestServer creates a full Server under httptest.Server.
// Returns the httptest server, the session manager, and the schedule store.
func buildTestServer(t *testing.T) (*httptest.Server, *session.Manager, *memstore.ScheduleStore, *memstore.WorkflowStore) {
	t.Helper()

	rootCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	st := memstore.New()
	sstore := memstore.NewScheduleStore()
	wstore := memstore.NewWorkflowStore()

	agents := map[string]core.AIAgent{"claude_code": &mockAgent{}}
	cfg := session.Config{
		TmuxSessionPrefix: "tc-",
		SessionsDir:       t.TempDir(),
		ClaudeProjectsDir: t.TempDir(),
		MessageLevel:      "all",
	}
	mgr := session.New(cfg, st, agents, rootCtx)
	mgr.RegisterPlatform("web", web.New())

	tracker := orchestrator.NewTurnTracker(100, nil)
	mgr.RegisterObserver(tracker.OnEvent)
	go tracker.Run(rootCtx)

	sched := scheduler.New(sstore, mgr, nil)
	dag := orchestrator.NewDAGEngine(wstore, mgr, tracker)
	orc := orchestrator.New(mgr, tracker)

	p := web.New()
	p.SeedChatID(nil)

	handler := buildTestHandler(mgr, p, sched, dag, orc)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return ts, mgr, sstore, wstore
}

func buildTestHandler(mgr *session.Manager, p *web.Platform,
	sched *scheduler.Scheduler, dag *orchestrator.DAGEngine, orc *orchestrator.Orchestrator) http.Handler {
	srv := web.NewServer(web.RunConfig{
		ListenAddr: "127.0.0.1:0",
		AuthToken:  testToken,
	}, mgr, p, sched, dag, orc)
	return srv.Handler()
}

// ── auth test ─────────────────────────────────────────────────────────────────

func TestWebAPI_NoToken_Returns401(t *testing.T) {
	ts, _, _, _ := buildTestServer(t)
	resp, err := http.Get(ts.URL + "/api/sessions")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// ── schedule round-trip: web API → schedule fires ─────────────────────────────

func TestWebAPI_Schedule_CreateAndFire(t *testing.T) {
	ts, mgr, _, _ := buildTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a target session so send_prompt has somewhere to go.
	sess, err := mgr.Create(ctx, "target", "/tmp/target", 1, "claude_code", "web")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// POST /api/schedules — create a send_prompt schedule targeting "target".
	sc := core.Schedule{
		Name:          "test-schedule",
		CronSpec:      "0 9 * * * *",
		Action:        core.ActionSendPrompt,
		TargetSession: "target",
		Payload:       "안녕!",
		Enabled:       true,
		CreatedAt:     time.Now(),
	}
	body, _ := json.Marshal(sc)
	resp := doRequest(t, ts, "POST", "/api/schedules", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var created core.Schedule
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()
	require.NotEmpty(t, created.ID)

	// GET /api/schedules — verify it appears.
	resp = doRequest(t, ts, "GET", "/api/schedules", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list []core.Schedule
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	resp.Body.Close()
	assert.NotEmpty(t, list)

	// POST /api/schedules/{id}/fire — fire it immediately.
	resp = doRequest(t, ts, "POST", "/api/schedules/"+created.ID+"/fire", nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()
}

// ── workflow round-trip: web API → StartRun ──────────────────────────────────

func TestWebAPI_Workflow_CreateAndStartRun(t *testing.T) {
	ts, mgr, _, _ := buildTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Pre-register a session for the workflow node.
	sess, err := mgr.Create(ctx, "wfnode", "/tmp/wfnode", 2, "claude_code", "web")
	require.NoError(t, err)
	require.NotNil(t, sess)

	// POST /api/workflows
	wf := core.Workflow{
		Name:    "simple-wf",
		Enabled: true,
		Nodes: []core.WorkflowNode{
			{NodeKey: "step1", SessionName: "wfnode", Prompt: "hello"},
		},
		CreatedAt: time.Now(),
	}
	body, _ := json.Marshal(wf)
	resp := doRequest(t, ts, "POST", "/api/workflows", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var createdWF core.Workflow
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&createdWF))
	resp.Body.Close()
	require.NotEmpty(t, createdWF.ID)

	// GET /api/workflows — verify it appears.
	resp = doRequest(t, ts, "GET", "/api/workflows", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// POST /api/workflows/{id}/run — start a run (async, returns runID).
	// The run will call SendInputBy on "wfnode" and wait for IDLE. Since our
	// mock tracker never reaches IDLE naturally, the run will block.
	// We just verify the API accepts the request and returns a runID.
	resp = doRequest(t, ts, "POST", "/api/workflows/"+createdWF.ID+"/run", nil)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	var runResp map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&runResp))
	resp.Body.Close()
	assert.NotEmpty(t, runResp["run_id"])
}

// ── helpers ───────────────────────────────────────────────────────────────────

func doRequest(t *testing.T, ts *httptest.Server, method, path string, body []byte) *http.Response {
	t.Helper()
	var req *http.Request
	if body != nil {
		req, _ = http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, ts.URL+path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
