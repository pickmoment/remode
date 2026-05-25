package core

// MessageCategory controls which message_level filters it passes.
type MessageCategory string

const (
	CategoryText        MessageCategory = "text"
	CategoryTool        MessageCategory = "tool"
	CategoryInteractive MessageCategory = "interactive"
)

// Action represents an inline button in a Message.
type Action struct {
	Label    string
	ActionID string
}

// Message is a platform-agnostic renderable message with optional inline buttons.
type Message struct {
	Text         string
	Actions      [][]Action // rows of buttons
	Category     MessageCategory
	Preformatted bool
}
