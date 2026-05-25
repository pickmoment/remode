package core

// AgentEventType discriminates the AgentEvent union.
type AgentEventType string

const (
	EventText           AgentEventType = "text"
	EventToolUse        AgentEventType = "tool_use"
	EventApprovalPrompt AgentEventType = "approval_prompt"
	EventPlanPrompt     AgentEventType = "plan_prompt"
	EventInfoPanel      AgentEventType = "info_panel"
)

// AgentEvent is a tagged union of all events an AIAgent can emit.
type AgentEvent struct {
	Type AgentEventType

	// EventText
	Text string

	// EventToolUse
	ToolName  string
	ToolInput map[string]any

	// EventApprovalPrompt
	DialogText  string
	OptionCount int
	IsWizard    bool

	// EventInfoPanel
	PanelText string
}
