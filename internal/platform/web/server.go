package web

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pickmoment/remode/internal/orchestrator"
	"github.com/pickmoment/remode/internal/scheduler"
	"github.com/pickmoment/remode/internal/session"
)

// RunConfig configures the web management service.
type RunConfig struct {
	ListenAddr    string // e.g. "127.0.0.1:8765"
	AuthToken     string // required; service refuses to start if empty
	NewProjectDir string
}

// BroadcastResult is the JSON-serialisable outcome of a broadcast operation.
type BroadcastResult struct {
	Name string `json:"name"`
	Err  string `json:"error,omitempty"`
}

// ChainConfig is the JSON request body for POST /api/chain.
type ChainConfig struct {
	FromSession    string `json:"from_session"`
	ToSession      string `json:"to_session"`
	PromptTemplate string `json:"prompt_template"`
}

// Server holds the HTTP server state.
type Server struct {
	sm       *session.Manager
	platform *Platform
	cfg      RunConfig
	sched    *scheduler.Scheduler    // nil if scheduler not configured
	dag      *orchestrator.DAGEngine // nil if DAG not configured
	orc      *orchestrator.Orchestrator // nil if orchestrator not configured
}

// NewServer creates a Server. This is the primary constructor; Run calls it internally.
// Useful in tests to get an http.Handler without starting the blocking server loop.
func NewServer(cfg RunConfig, sm *session.Manager, p *Platform,
	sched *scheduler.Scheduler, dag *orchestrator.DAGEngine, orc *orchestrator.Orchestrator) *Server {
	return &Server{sm: sm, platform: p, cfg: cfg, sched: sched, dag: dag, orc: orc}
}

// Handler returns an http.Handler for the web API. Call after NewServer.
// Static files (index.html) are served without auth so the login page loads;
// only /api/* routes require a Bearer token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)
	mux.Handle("GET /", http.FileServer(http.FS(staticFS)))
	return apiAuthMiddleware(s.cfg.AuthToken, mux)
}

// apiAuthMiddleware applies Bearer token auth only to /api/* paths.
// Static assets are passed through without authentication.
func apiAuthMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			got := extractToken(r)
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Run starts the HTTP management server. It blocks until ctx is cancelled.
// Must be called after sm.Startup (platform is already registered).
// sched, dag, and orc may be nil if those subsystems are not yet wired.
func Run(ctx context.Context, cfg RunConfig, sm *session.Manager, p *Platform,
	sched *scheduler.Scheduler, dag *orchestrator.DAGEngine, orc *orchestrator.Orchestrator) error {
	if cfg.AuthToken == "" {
		log.Printf("web: auth_token is empty — refusing to start (fail closed)")
		return nil
	}

	// Seed the ChatID counter from existing web sessions.
	p.SeedChatID(sm.ListAll())

	handler := NewServer(cfg, sm, p, sched, dag, orc).Handler()

	httpServer := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // no timeout for SSE streams
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("web: listening on http://%s", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// registerAPIRoutes registers only /api/* handlers onto mux.
// Paths are registered without the /api prefix so the mux strip works correctly
// when mounted under /api/ via http.StripPrefix is not needed — Go 1.22 pattern
// routing matches the full path, so we keep the /api/ prefix here.
func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	// Sessions
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{name}", s.handleGetSession)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("POST /api/sessions/{name}/input", s.handleSendInput)
	mux.HandleFunc("POST /api/sessions/{name}/key", s.handleSendKey)
	mux.HandleFunc("POST /api/sessions/{name}/kill", s.handleKillSession)
	mux.HandleFunc("POST /api/sessions/{name}/level", s.handleSetLevel)
	// Projects
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("GET /api/projects/{sessionID}/messages", s.handleGetProjectSessionMessages)
	// SSE output stream
	mux.HandleFunc("GET /api/sessions/{name}/stream", s.handleStream)

	// Schedules (Phase 3)
	if s.sched != nil {
		mux.HandleFunc("GET /api/schedules", s.handleListSchedules)
		mux.HandleFunc("POST /api/schedules", s.handleCreateSchedule)
		mux.HandleFunc("GET /api/schedules/{id}", s.handleGetSchedule)
		mux.HandleFunc("PUT /api/schedules/{id}", s.handleUpdateSchedule)
		mux.HandleFunc("DELETE /api/schedules/{id}", s.handleDeleteSchedule)
		mux.HandleFunc("POST /api/schedules/{id}/fire", s.handleFireSchedule)
	}

	// Workflows / DAG (Phase 4c)
	if s.dag != nil {
		mux.HandleFunc("GET /api/workflows", s.handleListWorkflows)
		mux.HandleFunc("POST /api/workflows", s.handleCreateWorkflow)
		mux.HandleFunc("GET /api/workflows/{id}", s.handleGetWorkflow)
		mux.HandleFunc("PUT /api/workflows/{id}", s.handleUpdateWorkflow)
		mux.HandleFunc("DELETE /api/workflows/{id}", s.handleDeleteWorkflow)
		mux.HandleFunc("POST /api/workflows/{id}/run", s.handleStartRun)
		mux.HandleFunc("GET /api/workflows/{id}/runs", s.handleListRuns)
		mux.HandleFunc("GET /api/runs/{runID}", s.handleGetRun)
	}

	// Orchestration (Phase 4a/4b)
	if s.orc != nil {
		mux.HandleFunc("POST /api/broadcast", s.handleBroadcast)
		mux.HandleFunc("POST /api/chain", s.handleChain)
	}
}
