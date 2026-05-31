package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/orchestrator"
)

// GET /api/schedules
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := s.sched.ListSchedules()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, schedules)
}

// GET /api/schedules/{id}
func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sc, err := s.sched.GetSchedule(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, sc)
}

// POST /api/schedules
func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var sc core.Schedule
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if sc.ID == "" {
		sc.ID = uuid.New().String()
	}
	if sc.CreatedAt.IsZero() {
		sc.CreatedAt = time.Now()
	}
	if err := s.sched.CreateSchedule(r.Context(), &sc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, sc)
}

// PUT /api/schedules/{id}
func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var sc core.Schedule
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	sc.ID = id
	if err := s.sched.UpdateSchedule(r.Context(), &sc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/schedules/{id}
func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.sched.DeleteSchedule(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/schedules/{id}/fire — immediately fires a schedule for testing
func (s *Server) handleFireSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.sched.Fire(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── workflows ─────────────────────────────────────────────────────────────────

// GET /api/workflows
func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	wfs, err := s.dag.ListWorkflows()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, wfs)
}

// POST /api/workflows
func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	var wf core.Workflow
	if err := json.NewDecoder(r.Body).Decode(&wf); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if wf.ID == "" {
		wf.ID = uuid.New().String()
	}
	if wf.CreatedAt.IsZero() {
		wf.CreatedAt = time.Now()
	}
	// Assign node IDs
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == "" {
			wf.Nodes[i].ID = uuid.New().String()
		}
		wf.Nodes[i].WorkflowID = wf.ID
	}
	for i := range wf.Edges {
		wf.Edges[i].WorkflowID = wf.ID
	}
	if err := s.dag.ValidateWorkflow(&wf); err != nil {
		http.Error(w, "invalid DAG: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.dag.SaveWorkflow(&wf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, wf)
}

// GET /api/workflows/{id}
func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wf, err := s.dag.GetWorkflow(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, wf)
}

// PUT /api/workflows/{id}
func (s *Server) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var wf core.Workflow
	if err := json.NewDecoder(r.Body).Decode(&wf); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	wf.ID = id
	if err := s.dag.ValidateWorkflow(&wf); err != nil {
		http.Error(w, "invalid DAG: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.dag.SaveWorkflow(&wf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/workflows/{id}
func (s *Server) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.dag.DeleteWorkflow(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/workflows/{id}/run
func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	runID, err := s.dag.StartRun(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"run_id": runID})
}

// GET /api/workflows/{id}/runs
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	runs, err := s.dag.ListRuns(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, runs)
}

// GET /api/runs/{runID}
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	run, err := s.dag.GetRun(runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	nodeRuns, err := s.dag.ListNodeRuns(runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"run": run, "nodes": nodeRuns})
}

// ── orchestration ─────────────────────────────────────────────────────────────

// POST /api/broadcast   body: {"names":["a","b"],"prompt":"...","collect_results":false}
func (s *Server) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names          []string `json:"names"`
		Prompt         string   `json:"prompt"`
		CollectResults bool     `json:"collect_results"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	raw := s.orc.Broadcast(r.Context(), req.Names, req.Prompt, req.CollectResults)
	results := make([]BroadcastResult, len(raw))
	for i, res := range raw {
		results[i] = BroadcastResult{Name: res.Name}
		if res.Err != nil {
			results[i].Err = res.Err.Error()
		}
	}
	writeJSON(w, results)
}

// POST /api/chain   body: {"from_session":"...","to_session":"...","prompt_template":"..."}
func (s *Server) handleChain(w http.ResponseWriter, r *http.Request) {
	var req ChainConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	orcCfg := orchestrator.ChainConfig{
		FromSession:    req.FromSession,
		ToSession:      req.ToSession,
		PromptTemplate: req.PromptTemplate,
	}
	if err := s.orc.RunChain(r.Context(), orcCfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
