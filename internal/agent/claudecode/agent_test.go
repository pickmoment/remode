package claudecode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pickmoment/remode/internal/core"
)

// ── parseEntry ────────────────────────────────────────────────────────────────

func TestParseEntry_UserType(t *testing.T) {
	events := parseEntry(map[string]any{"type": "user"})
	assert.Empty(t, events)
}

func TestParseEntry_SummaryType(t *testing.T) {
	events := parseEntry(map[string]any{"type": "summary"})
	assert.Empty(t, events)
}

func TestParseEntry_AssistantText(t *testing.T) {
	entry := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "Hello"},
			},
		},
	}
	events := parseEntry(entry)
	require.Len(t, events, 1)
	assert.Equal(t, core.EventText, events[0].Type)
	assert.Equal(t, "Hello", events[0].Text)
}

func TestParseEntry_SuppressedText(t *testing.T) {
	entry := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "No response requested"},
			},
		},
	}
	assert.Empty(t, parseEntry(entry))
}

func TestParseEntry_ToolUse(t *testing.T) {
	entry := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"name":  "Bash",
					"input": map[string]any{"command": "ls -la"},
				},
			},
		},
	}
	events := parseEntry(entry)
	require.Len(t, events, 1)
	assert.Equal(t, core.EventToolUse, events[0].Type)
	assert.Equal(t, "Bash", events[0].ToolName)
	assert.Equal(t, "ls -la", events[0].ToolInput["command"])
}

func TestParseEntry_MultipleContent(t *testing.T) {
	entry := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "thinking..."},
				map[string]any{
					"type":  "tool_use",
					"name":  "Read",
					"input": map[string]any{"file_path": "/foo.go"},
				},
			},
		},
	}
	events := parseEntry(entry)
	require.Len(t, events, 2)
	assert.Equal(t, core.EventText, events[0].Type)
	assert.Equal(t, core.EventToolUse, events[1].Type)
}

// ── tmux detection ────────────────────────────────────────────────────────────

func TestIsPlanBanner(t *testing.T) {
	assert.True(t, IsPlanBanner("Would you like to proceed with this plan?"))
	assert.True(t, IsPlanBanner("Accept plan\nsome text"))
	assert.True(t, IsPlanBanner("Keep planning if needed"))
	assert.False(t, IsPlanBanner("normal output"))
}

func TestIsApprovalDialog(t *testing.T) {
	content := "Choose an option:\n❯ 1. Yes\n  2. No\nEsc to cancel"
	assert.True(t, IsApprovalDialog(content))
}

func TestIsApprovalDialog_NoNavigation(t *testing.T) {
	assert.False(t, IsApprovalDialog("❯ 1. Yes\n  2. No"))
}

func TestIsInfoPanel(t *testing.T) {
	// has navigation but no option lines
	content := "Info: something important\nEsc to cancel"
	assert.True(t, IsInfoPanel(content))
}

func TestIsInfoPanel_WithOptions(t *testing.T) {
	// has navigation AND numbered option lines → approval, not info
	content := "❯ 1. option\nEsc to cancel"
	assert.False(t, IsInfoPanel(content))
}

func TestIsInfoPanel_WithTextOptions(t *testing.T) {
	// has navigation AND text option lines → text-option dialog, not info
	content := "Which one?\n❯ Option A\n  Option B\nEsc to cancel"
	assert.False(t, IsInfoPanel(content))
}

func TestIsTextOptionDialog(t *testing.T) {
	content := "Which one?\n❯ Option A\n  Option B\nEsc to cancel"
	assert.True(t, IsTextOptionDialog(content))
}

func TestIsTextOptionDialog_NumberedNotText(t *testing.T) {
	content := "Which one?\n❯ 1. Option A\n  2. Option B\nEsc to cancel"
	assert.False(t, IsTextOptionDialog(content))
}

func TestIsTextOptionDialog_NoNavigation(t *testing.T) {
	content := "❯ Option A\n  Option B"
	assert.False(t, IsTextOptionDialog(content))
}

func TestExtractNonNumberedOptions(t *testing.T) {
	content := "Which library?\n\n❯ moment.js\n  day.js\n  date-fns\n\nEsc to cancel"
	opts := ExtractNonNumberedOptions(content)
	require.Equal(t, []string{"moment.js", "day.js", "date-fns"}, opts)
}

func TestExtractNonNumberedOptions_CursorNotFirst(t *testing.T) {
	// cursor is on second option
	content := "Which?\n\n  Option A\n❯ Option B\n  Option C\n\nEsc to cancel"
	opts := ExtractNonNumberedOptions(content)
	require.Equal(t, []string{"Option A", "Option B", "Option C"}, opts)
}

func TestIsMultistepWizard(t *testing.T) {
	assert.True(t, IsMultistepWizard("Tab/Arrow keys to navigate"))
	assert.False(t, IsMultistepWizard("Esc to cancel"))
}

// ── text extraction ───────────────────────────────────────────────────────────

func TestExtractQuestionLine(t *testing.T) {
	content := "What should I do?\n❯ 1. Option A\n  2. Option B\nEsc to cancel"
	assert.Equal(t, "What should I do?", ExtractQuestionLine(content))
}

func TestExtractApprovalText(t *testing.T) {
	content := "What should I do?\n❯ 1. Option A\n  2. Option B\nEsc to cancel"
	text := ExtractApprovalText(content)
	assert.Contains(t, text, "What should I do?")
	assert.Contains(t, text, "Option A")
}

func TestExtractApprovalText_StripsSeparators(t *testing.T) {
	// separator between question and options should be stripped
	content := "Question?\n───────────────\n❯ 1. Yes\n  2. No\nEsc to cancel"
	text := ExtractApprovalText(content)
	assert.Contains(t, text, "Question?")
	assert.NotContains(t, text, "───")
}

func TestExtractApprovalText_SeparatorBeforeQuestion(t *testing.T) {
	// separator above question should be excluded (q should land on question line)
	content := "───────────────\nReal question?\n❯ 1. Yes\n  2. No\nEsc to cancel"
	text := ExtractApprovalText(content)
	assert.Contains(t, text, "Real question?")
	assert.NotContains(t, text, "───")
}

func TestParseEntry_AskUserQuestion(t *testing.T) {
	// AskUserQuestion is detected via tmux screen capture, not JSONL.
	// parseEntry silently drops this tool_use so the screen-capture path
	// handles it without double-firing.
	entry := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "tool_use",
					"name": "AskUserQuestion",
					"input": map[string]any{
						"questions": []any{
							map[string]any{
								"question": "Which option?",
								"header":   "Option",
								"options": []any{
									map[string]any{"label": "Option A", "description": "desc A"},
									map[string]any{"label": "Option B", "description": "desc B"},
								},
							},
						},
					},
				},
			},
		},
	}
	events := parseEntry(entry)
	assert.Empty(t, events)
}

func TestCountOptions(t *testing.T) {
	assert.Equal(t, 3, countOptions("❯ 1. A\n  2. B\n  3. C\nEsc to cancel"))
	assert.Equal(t, 2, countOptions("no options here"))
}

// ── path encoding ─────────────────────────────────────────────────────────────

func TestEncodePath(t *testing.T) {
	assert.Equal(t, "-Users-user-projects-myapp", EncodePath("/Users/user/projects/myapp"))
}

func TestProjectDirFor(t *testing.T) {
	dir := ProjectDirFor("/tmp/proj", "/home/user/.claude/projects")
	assert.Equal(t, "/home/user/.claude/projects/-tmp-proj", dir)
}
