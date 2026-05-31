package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

// sessionResponse is the JSON representation of a session for the REST API.
type sessionResponse struct {
	Name      string    `json:"name"`
	CWD       string    `json:"cwd"`
	AgentType string    `json:"agent_type"`
	Transport string    `json:"transport"`
	Level     string    `json:"level"`
	SessionID string    `json:"session_id"`
	ChatID    int64     `json:"chat_id"`
	CreatedAt time.Time `json:"created_at"`
}

func toSessionResponse(s *core.Session) sessionResponse {
	return sessionResponse{
		Name:      s.Name,
		CWD:       s.CWD,
		AgentType: s.AgentType,
		Transport: s.Transport,
		Level:     string(s.Level),
		SessionID: s.SessionID,
		ChatID:    s.ChatID,
		CreatedAt: s.CreatedAt,
	}
}

// GET /api/sessions
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	all := s.sm.ListAll()
	resp := make([]sessionResponse, 0, len(all))
	for _, sess := range all {
		resp = append(resp, toSessionResponse(sess))
	}
	writeJSON(w, resp)
}

// GET /api/sessions/{name}
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sess := s.sm.Get(name)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	capture, _ := s.sm.Capture(sess)
	writeJSON(w, map[string]any{
		"session": toSessionResponse(sess),
		"screen":  capture,
	})
}

// POST /api/sessions   body: {"name":"...","cwd":"...","agent_type":"..."}
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		CWD       string `json:"cwd"`
		AgentType string `json:"agent_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.CWD == "" {
		http.Error(w, "name and cwd are required", http.StatusBadRequest)
		return
	}
	if s.sm.Get(req.Name) != nil {
		http.Error(w, "session already exists", http.StatusConflict)
		return
	}

	chatID := s.platform.NextChatID()
	sess, err := s.sm.Create(r.Context(), req.Name, req.CWD, chatID, req.AgentType, "web")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, toSessionResponse(sess))
}

// POST /api/sessions/{name}/input   body: {"text":"..."}
func (s *Server) handleSendInput(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sess := s.sm.Get(name)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	var req struct{ Text string `json:"text"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.sm.SendInput(sess, req.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/sessions/{name}/key   body: {"key":"..."}
func (s *Server) handleSendKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sess := s.sm.Get(name)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	var req struct{ Key string `json:"key"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.sm.SendKey(sess, req.Key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/sessions/{name}/kill
func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.sm.Kill(r.Context(), name); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/sessions/{name}/level   body: {"level":"..."}
func (s *Server) handleSetLevel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sess := s.sm.Get(name)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	var req struct{ Level string `json:"level"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.sm.SetMessageLevel(r.Context(), sess, req.Level); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/projects?agent=
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	agentType := r.URL.Query().Get("agent")
	projects, err := s.sm.ListProjects(agentType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, projects)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
