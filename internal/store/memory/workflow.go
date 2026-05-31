package memory

import (
	"fmt"
	"sync"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

// WorkflowStore is an in-memory implementation of orchestrator.WorkflowStore.
type WorkflowStore struct {
	mu       sync.RWMutex
	wfs      map[string]*core.Workflow
	runs     map[string]*core.WorkflowRun      // runID → run
	nodeRuns map[string]*core.WorkflowNodeRun  // "runID:nodeKey" → nodeRun
}

// NewWorkflowStore creates an in-memory WorkflowStore for tests.
func NewWorkflowStore() *WorkflowStore {
	return &WorkflowStore{
		wfs:      make(map[string]*core.Workflow),
		runs:     make(map[string]*core.WorkflowRun),
		nodeRuns: make(map[string]*core.WorkflowNodeRun),
	}
}

func (s *WorkflowStore) SaveWorkflow(wf *core.Workflow) error {
	s.mu.Lock(); defer s.mu.Unlock()
	cp := *wf
	s.wfs[wf.ID] = &cp
	return nil
}

func (s *WorkflowStore) GetWorkflow(id string) (*core.Workflow, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	wf, ok := s.wfs[id]
	if !ok {
		return nil, fmt.Errorf("workflow not found: %s", id)
	}
	cp := *wf
	return &cp, nil
}

func (s *WorkflowStore) ListWorkflows() ([]*core.Workflow, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	out := make([]*core.Workflow, 0, len(s.wfs))
	for _, wf := range s.wfs {
		cp := *wf
		out = append(out, &cp)
	}
	return out, nil
}

func (s *WorkflowStore) DeleteWorkflow(id string) error {
	s.mu.Lock(); defer s.mu.Unlock()
	delete(s.wfs, id)
	return nil
}

func (s *WorkflowStore) CreateWorkflowRun(run *core.WorkflowRun) error {
	s.mu.Lock(); defer s.mu.Unlock()
	cp := *run
	s.runs[run.ID] = &cp
	return nil
}

func (s *WorkflowStore) UpdateWorkflowRun(runID string, status core.WorkflowStatus) error {
	s.mu.Lock(); defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return fmt.Errorf("run not found: %s", runID)
	}
	run.Status = status
	run.UpdatedAt = time.Now()
	return nil
}

func (s *WorkflowStore) GetWorkflowRun(runID string) (*core.WorkflowRun, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	run, ok := s.runs[runID]
	if !ok {
		return nil, fmt.Errorf("run not found: %s", runID)
	}
	cp := *run
	return &cp, nil
}

func (s *WorkflowStore) ListWorkflowRunsForWorkflow(workflowID string) ([]*core.WorkflowRun, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	var out []*core.WorkflowRun
	for _, run := range s.runs {
		if run.WorkflowID == workflowID {
			cp := *run
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *WorkflowStore) UpsertNodeRun(nr *core.WorkflowNodeRun) error {
	s.mu.Lock(); defer s.mu.Unlock()
	key := nr.RunID + ":" + nr.NodeKey
	cp := *nr
	s.nodeRuns[key] = &cp
	return nil
}

func (s *WorkflowStore) ListNodeRuns(runID string) ([]*core.WorkflowNodeRun, error) {
	s.mu.RLock(); defer s.mu.RUnlock()
	var out []*core.WorkflowNodeRun
	prefix := runID + ":"
	for k, nr := range s.nodeRuns {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			cp := *nr
			out = append(out, &cp)
		}
	}
	return out, nil
}
