package core

import "context"

// ProjectSession is a single recorded agent session under a project.
type ProjectSession struct {
	SessionID    string
	CreatedAt    string // ISO-8601
	LastModified string // ISO-8601
	Title        string
}

// Project groups sessions by working directory.
type Project struct {
	CWD         string
	DisplayPath string
	Sessions    []ProjectSession
}

// SessionUpdater is the minimal callback surface that agent watchers use to
// persist offset and session-ID changes without depending on the full store.
type SessionUpdater interface {
	UpdateOffset(name string, offset int64) error
	Save(sess *Session) error
}

// AIAgent abstracts everything needed to control a single CLI agent process
// running inside a tmux session.
type AIAgent interface {
	Create(ctx context.Context, sess *Session, settingsPath string) error
	Resume(ctx context.Context, sess *Session, settingsPath, resumeID string) error
	SendInput(sess *Session, text string) error
	SendKey(sess *Session, key string) error
	Capture(sess *Session) (string, error)
	Kill(sess *Session) error
	Exists(sess *Session) bool

	// Discover polls until the agent assigns a session ID and output path.
	Discover(ctx context.Context, sess *Session, existing map[string]struct{}, projectsDir string) (sessionID, outputPath string, err error)

	// WatchEvents streams AgentEvents into onEvent until ctx is cancelled.
	// settleMS is the debounce window for JSONL file-change events.
	WatchEvents(ctx context.Context, sess *Session, onEvent func(AgentEvent), updater SessionUpdater, settleMS int) error

	ListProjects(projectsDir string) ([]Project, error)
	ProjectDirFor(cwd, projectsDir string) string
	NewestOutput(sess *Session, projectsDir string) string
	OutputSnapshot(sess *Session, projectsDir string) map[string]struct{}
}
