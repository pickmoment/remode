package formatter_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/formatter"
)

func TestFormatEvent_Text(t *testing.T) {
	msgs := formatter.FormatEvent(core.AgentEvent{Type: core.EventText, Text: "hello"})
	require.Len(t, msgs, 1)
	assert.Equal(t, "hello", msgs[0].Text)
	assert.Equal(t, core.CategoryText, msgs[0].Category)
}

func TestFormatEvent_EmptyText(t *testing.T) {
	msgs := formatter.FormatEvent(core.AgentEvent{Type: core.EventText, Text: "   "})
	assert.Empty(t, msgs)
}

func TestFormatEvent_ToolUse_Bash(t *testing.T) {
	msgs := formatter.FormatEvent(core.AgentEvent{
		Type:     core.EventToolUse,
		ToolName: "Bash",
		ToolInput: map[string]any{
			"command": "echo hello\necho world",
		},
	})
	require.Len(t, msgs, 1)
	assert.Equal(t, core.CategoryTool, msgs[0].Category)
	assert.Contains(t, msgs[0].Text, "Bash")
	assert.Contains(t, msgs[0].Text, "`echo hello`")
	assert.Contains(t, msgs[0].Text, "+1 줄")
}

func TestFormatEvent_ToolUse_Edit(t *testing.T) {
	msgs := formatter.FormatEvent(core.AgentEvent{
		Type:     core.EventToolUse,
		ToolName: "Edit",
		ToolInput: map[string]any{"file_path": "/foo/bar.go"},
	})
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Text, "`/foo/bar.go`")
}

func TestFormatEvent_PlanPrompt(t *testing.T) {
	msgs := formatter.FormatEvent(core.AgentEvent{Type: core.EventPlanPrompt})
	require.Len(t, msgs, 1)
	msg := msgs[0]
	assert.Equal(t, core.CategoryInteractive, msg.Category)
	assert.Len(t, msg.Actions, 2)
	assert.Equal(t, "key:1", msg.Actions[0][0].ActionID)
	assert.Equal(t, "key:3", msg.Actions[1][0].ActionID)
}

func TestFormatApprovalPrompt_Default(t *testing.T) {
	msg := formatter.FormatApprovalPrompt("proceed?", 2, false)
	assert.Equal(t, core.CategoryInteractive, msg.Category)
	assert.True(t, msg.Preformatted)
	// row 0: buttons 1,2; row 1: Tab + Escape
	require.Len(t, msg.Actions, 2)
	assert.Equal(t, "key:1", msg.Actions[0][0].ActionID)
	assert.Equal(t, "key:Tab", msg.Actions[1][0].ActionID)
	assert.Equal(t, "key:Escape", msg.Actions[1][1].ActionID)
}

func TestFormatApprovalPrompt_Wizard(t *testing.T) {
	msg := formatter.FormatApprovalPrompt("pick one", 3, true)
	last := msg.Actions[len(msg.Actions)-1]
	assert.Equal(t, "key:Left", last[0].ActionID)
	assert.Equal(t, "key:Tab", last[1].ActionID)
	assert.Equal(t, "key:Escape", last[2].ActionID)
}

func TestFormatApprovalPrompt_ManyOptions(t *testing.T) {
	// 7 options → rows of 5 + 2, then control row
	msg := formatter.FormatApprovalPrompt("big dialog", 7, false)
	assert.Equal(t, 5, len(msg.Actions[0]))
	assert.Equal(t, 2, len(msg.Actions[1]))
}

func TestFormatInfoPanel(t *testing.T) {
	msg := formatter.FormatInfoPanel("some info")
	assert.True(t, strings.HasPrefix(msg.Text, "ℹ️"))
	assert.Contains(t, msg.Text, "some info")
	require.Len(t, msg.Actions, 1)
	assert.Equal(t, "key:Tab", msg.Actions[0][0].ActionID)
}

func TestFormatInfoPanel_Empty(t *testing.T) {
	msg := formatter.FormatInfoPanel("")
	assert.Equal(t, "ℹ️", msg.Text)
}

func TestFormatAskUserQuestion(t *testing.T) {
	msg := formatter.FormatAskUserQuestion("Which option?", []string{"Option A", "Option B", "Option C"})
	assert.Equal(t, core.CategoryInteractive, msg.Category)
	assert.Contains(t, msg.Text, "Which option?")
	// 3 options → 2 rows (2+1), plus cancel row
	require.Len(t, msg.Actions, 3)
	assert.Equal(t, "arrowkey:0", msg.Actions[0][0].ActionID)
	assert.Equal(t, "arrowkey:1", msg.Actions[0][1].ActionID)
	assert.Equal(t, "arrowkey:2", msg.Actions[1][0].ActionID)
	assert.Equal(t, "key:Escape", msg.Actions[2][0].ActionID)
}

func TestFormatEvent_AskUserQuestion(t *testing.T) {
	msgs := formatter.FormatEvent(core.AgentEvent{
		Type:        core.EventAskUserQuestion,
		AskQuestion: "Pick one",
		AskOptions:  []string{"A", "B"},
	})
	require.Len(t, msgs, 1)
	assert.Equal(t, core.CategoryInteractive, msgs[0].Category)
	assert.Contains(t, msgs[0].Text, "Pick one")
}
