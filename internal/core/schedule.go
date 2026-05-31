package core

import (
	"encoding/json"
	"time"
)

// ScheduleAction is the type of scheduled operation.
type ScheduleAction string

const (
	// ActionSendPrompt sends a predefined prompt/input to a named session.
	ActionSendPrompt ScheduleAction = "send_prompt"
	// ActionStatusReport captures the session screen and delivers a report.
	ActionStatusReport ScheduleAction = "status_report"
	// ActionBatchSession creates a session, runs it, and kills it when done.
	ActionBatchSession ScheduleAction = "batch_session"
)

// SessionTemplate holds parameters for creating a session from a schedule.
type SessionTemplate struct {
	CWD       string `json:"cwd"`
	AgentType string `json:"agent_type"`
}

// MarshalJSON serialises a SessionTemplate to JSON bytes.
func (s SessionTemplate) MarshalJSON() ([]byte, error) {
	type alias SessionTemplate
	return json.Marshal(alias(s))
}

// UnmarshalJSON deserialises a SessionTemplate from JSON bytes.
func (s *SessionTemplate) UnmarshalJSON(data []byte) error {
	type alias SessionTemplate
	return json.Unmarshal(data, (*alias)(s))
}

// Schedule defines a recurring or one-off scheduled action on a session.
type Schedule struct {
	ID            string
	Name          string
	CronSpec      string         // robfig/cron/v3 spec (e.g. "0 9 * * *")
	Action        ScheduleAction
	TargetSession string          // for send_prompt and status_report
	Template      SessionTemplate // for batch_session
	Payload       string          // prompt text (send_prompt) or JSON opts (status_report)
	InitialPrompt string          // optional first prompt for batch_session
	DeadlineSecs  int             // max runtime for batch_session; 0 = no deadline
	Enabled       bool
	LastRun       time.Time
	NextRun       time.Time
	CreatedAt     time.Time
}
