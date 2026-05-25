package core

import "time"

// MessageLevel controls how many events are forwarded to the chat platform.
type MessageLevel string

const (
	LevelAll         MessageLevel = "all"
	LevelInteractive MessageLevel = "interactive"
	LevelFinal       MessageLevel = "final"
)

// Session holds the runtime state for a single agent session.
type Session struct {
	Name         string
	TmuxName     string
	SessionID    string // Claude/Codex session UUID
	CWD          string
	JSONLPath    string
	ChatID       int64
	CreatedAt    time.Time
	JSONLOffset  int64
	Level        MessageLevel
	AgentType    string
	LastOutbound []Message // ephemeral: last batch sent to the user
}
