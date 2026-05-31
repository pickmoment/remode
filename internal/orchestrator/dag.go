package orchestrator

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/pickmoment/remode/internal/core"
)

// WorkflowStore persists workflow definitions and run state.
type WorkflowStore interface {
	// Workflow definitions
	SaveWorkflow(wf *core.Workflow) error
	GetWorkflow(id string) (*core.Workflow, error)
	ListWorkflows() ([]*core.Workflow, error)
	DeleteWorkflow(id string) error

	// Run state
	CreateWorkflowRun(run *core.WorkflowRun) error
	UpdateWorkflowRun(runID string, status core.WorkflowStatus) error
	GetWorkflowRun(runID string) (*core.WorkflowRun, error)
	ListWorkflowRunsForWorkflow(workflowID string) ([]*core.WorkflowRun, error)

	// Node run state
	UpsertNodeRun(nr *core.WorkflowNodeRun) error
	ListNodeRuns(runID string) ([]*core.WorkflowNodeRun, error)
}

// DAGEngine runs workflow DAGs with topological ordering.
type DAGEngine struct {
	store   WorkflowStore
	driver  OrcDriver
	tracker *TurnTracker

	nextChatID atomic.Int64
}

// NewDAGEngine creates a DAGEngine. clock is used for run timestamps.
func NewDAGEngine(ws WorkflowStore, driver OrcDriver, tracker *TurnTracker) *DAGEngine {
	e := &DAGEngine{store: ws, driver: driver, tracker: tracker}
	seed := time.Now().UnixNano() & ((1 << 60) - 1)
	e.nextChatID.Store(seed)
	return e
}

// ── workflow CRUD delegation ───────────────────────────────────────────────────

func (e *DAGEngine) SaveWorkflow(wf *core.Workflow) error          { return e.store.SaveWorkflow(wf) }
func (e *DAGEngine) GetWorkflow(id string) (*core.Workflow, error)  { return e.store.GetWorkflow(id) }
func (e *DAGEngine) ListWorkflows() ([]*core.Workflow, error)       { return e.store.ListWorkflows() }
func (e *DAGEngine) DeleteWorkflow(id string) error                 { return e.store.DeleteWorkflow(id) }
func (e *DAGEngine) GetRun(runID string) (*core.WorkflowRun, error) { return e.store.GetWorkflowRun(runID) }
func (e *DAGEngine) ListRuns(wfID string) ([]*core.WorkflowRun, error) {
	return e.store.ListWorkflowRunsForWorkflow(wfID)
}
func (e *DAGEngine) ListNodeRuns(runID string) ([]*core.WorkflowNodeRun, error) {
	return e.store.ListNodeRuns(runID)
}

// ValidateWorkflow checks that the workflow is a valid DAG (no cycles).
func (e *DAGEngine) ValidateWorkflow(wf *core.Workflow) error {
	_, err := topologicalOrder(wf)
	return err
}

// StartRun creates a WorkflowRun and begins executing it asynchronously.
func (e *DAGEngine) StartRun(ctx context.Context, workflowID string) (string, error) {
	wf, err := e.store.GetWorkflow(workflowID)
	if err != nil {
		return "", fmt.Errorf("get workflow: %w", err)
	}
	if !wf.Enabled {
		return "", fmt.Errorf("workflow %s is disabled", workflowID)
	}
	if _, err := topologicalOrder(wf); err != nil {
		return "", fmt.Errorf("invalid DAG: %w", err)
	}

	run := &core.WorkflowRun{
		ID:         uuid.New().String(),
		WorkflowID: workflowID,
		Status:     core.StatusRunning,
		StartedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := e.store.CreateWorkflowRun(run); err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}

	// Initialise all nodes as "waiting".
	for _, node := range wf.Nodes {
		e.store.UpsertNodeRun(&core.WorkflowNodeRun{ //nolint:errcheck
			RunID:     run.ID,
			NodeKey:   node.NodeKey,
			Status:    core.StatusWaiting,
			UpdatedAt: time.Now(),
		})
	}

	go e.execute(ctx, run.ID, wf)
	return run.ID, nil
}

// ResumeRuns re-enters any persisted "running" runs. Call after Startup.
func (e *DAGEngine) ResumeRuns(ctx context.Context) error {
	wfs, err := e.store.ListWorkflows()
	if err != nil {
		return err
	}
	for _, wf := range wfs {
		runs, err := e.store.ListWorkflowRunsForWorkflow(wf.ID)
		if err != nil {
			continue
		}
		for _, run := range runs {
			if run.Status == core.StatusRunning {
				wf := wf
				run := run
				go e.execute(ctx, run.ID, wf)
			}
		}
	}
	return nil
}

// execute drives a workflow run to completion.
func (e *DAGEngine) execute(ctx context.Context, runID string, wf *core.Workflow) {
	order, err := topologicalOrder(wf)
	if err != nil {
		log.Printf("dag: %s: bad topology: %v", runID, err)
		e.store.UpdateWorkflowRun(runID, core.StatusFailed) //nolint:errcheck
		return
	}

	// Build a dependency map: node_key → set of prerequisite node_keys.
	deps := buildDeps(wf)
	// Build a reverse map: node_key → WorkflowNode.
	nodeByKey := make(map[string]core.WorkflowNode, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodeByKey[n.NodeKey] = n
	}

	var mu sync.Mutex
	done := make(map[string]bool) // node_key → completed

	for _, key := range order {
		key := key
		node := nodeByKey[key]

		// Wait for all dependencies.
		for dep := range deps[key] {
			mu.Lock()
			alreadyDone := done[dep]
			mu.Unlock()
			if !alreadyDone {
				// Poll with a short interval — in a real system we'd use condition
				// variables or a proper fan-in, but for simplicity polling is fine
				// since the hot path is WaitIdle (long-running).
				if err := waitForKey(ctx, dep, &done, &mu); err != nil {
					log.Printf("dag: %s: node %s: dep %s wait: %v", runID, key, dep, err)
					e.store.UpdateWorkflowRun(runID, core.StatusFailed) //nolint:errcheck
					return
				}
			}
		}

		// Update node status to "running".
		e.store.UpsertNodeRun(&core.WorkflowNodeRun{ //nolint:errcheck
			RunID:     runID,
			NodeKey:   key,
			Status:    core.StatusRunning,
			UpdatedAt: time.Now(),
		})

		// Execute the node.
		if err := e.runNode(ctx, node); err != nil {
			log.Printf("dag: %s: node %s: %v", runID, key, err)
			e.store.UpsertNodeRun(&core.WorkflowNodeRun{ //nolint:errcheck
				RunID:     runID,
				NodeKey:   key,
				Status:    core.StatusFailed,
				UpdatedAt: time.Now(),
			})
			e.store.UpdateWorkflowRun(runID, core.StatusFailed) //nolint:errcheck
			return
		}

		// Mark done.
		e.store.UpsertNodeRun(&core.WorkflowNodeRun{ //nolint:errcheck
			RunID:     runID,
			NodeKey:   key,
			Status:    core.StatusDone,
			UpdatedAt: time.Now(),
		})
		mu.Lock()
		done[key] = true
		mu.Unlock()
		log.Printf("dag: %s: node %s done", runID, key)
	}

	e.store.UpdateWorkflowRun(runID, core.StatusDone) //nolint:errcheck
	log.Printf("dag: run %s complete", runID)
}

// runNode: ensure the session exists, send prompt, wait for IDLE.
func (e *DAGEngine) runNode(ctx context.Context, node core.WorkflowNode) error {
	sessName := node.SessionName
	if sessName == "" {
		// Create a new session from the template.
		sessName = fmt.Sprintf("wf-%s-%s", node.NodeKey, time.Now().Format("0102-1504"))
		chatID := e.nextChatID.Add(1)
		sess, err := e.driver.Create(ctx, sessName, node.Template.CWD, chatID,
			node.Template.AgentType, "web")
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		sessName = sess.Name
	}

	if node.Prompt != "" {
		if err := e.driver.SendInputBy(sessName, node.Prompt); err != nil {
			return fmt.Errorf("send prompt: %w", err)
		}
	}

	// Wait for the session to reach IDLE (turn complete).
	// Cold-restart window: if the session was already running when we resumed,
	// one fresh TurnIdleMS window of silence is treated as "done".
	if err := e.tracker.WaitIdle(ctx, sessName); err != nil {
		return fmt.Errorf("wait idle: %w", err)
	}
	if e.tracker.State(sessName) == TurnBlocked {
		return fmt.Errorf("session %s is BLOCKED — human input required", sessName)
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func waitForKey(ctx context.Context, key string, done *map[string]bool, mu *sync.Mutex) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			mu.Lock()
			ok := (*done)[key]
			mu.Unlock()
			if ok {
				return nil
			}
		}
	}
}

// topologicalOrder returns node_keys in execution order (dependencies first).
// Returns an error if a cycle is detected.
func topologicalOrder(wf *core.Workflow) ([]string, error) {
	// Build adjacency and in-degree maps.
	inDegree := make(map[string]int)
	adj := make(map[string][]string) // to → list of from
	for _, n := range wf.Nodes {
		if _, ok := inDegree[n.NodeKey]; !ok {
			inDegree[n.NodeKey] = 0
		}
	}
	for _, e := range wf.Edges {
		inDegree[e.ToNode]++
		adj[e.FromNode] = append(adj[e.FromNode], e.ToNode)
	}

	// Kahn's algorithm.
	var queue []string
	for key, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, key)
		}
	}

	var order []string
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)
		for _, next := range adj[n] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(order) != len(inDegree) {
		return nil, fmt.Errorf("cycle detected in workflow %s", wf.ID)
	}
	return order, nil
}

// buildDeps returns a map of node_key → set of prerequisite node_keys.
func buildDeps(wf *core.Workflow) map[string]map[string]bool {
	deps := make(map[string]map[string]bool)
	for _, n := range wf.Nodes {
		deps[n.NodeKey] = make(map[string]bool)
	}
	for _, e := range wf.Edges {
		deps[e.ToNode][e.FromNode] = true
	}
	return deps
}
