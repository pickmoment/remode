// Package formatter converts AgentEvents to platform-agnostic Messages.
package formatter

import (
	"fmt"
	"strings"

	"github.com/pickmoment/remode/internal/core"
)

// FormatEvent converts a single AgentEvent into zero or more Messages.
func FormatEvent(event core.AgentEvent) []core.Message {
	switch event.Type {
	case core.EventText:
		if strings.TrimSpace(event.Text) == "" {
			return nil
		}
		return []core.Message{{Text: event.Text, Category: core.CategoryText}}
	case core.EventToolUse:
		return []core.Message{formatToolUse(event.ToolName, event.ToolInput)}
	case core.EventPlanPrompt:
		return []core.Message{FormatPlanPrompt()}
	case core.EventApprovalPrompt:
		return []core.Message{FormatApprovalPrompt(event.DialogText, event.OptionCount, event.IsWizard)}
	case core.EventInfoPanel:
		return []core.Message{FormatInfoPanel(event.PanelText)}
	case core.EventAskUserQuestion:
		return []core.Message{FormatAskUserQuestion(event.AskQuestion, event.AskOptions)}
	}
	return nil
}

// FormatPlanPrompt returns the plan-mode button message.
func FormatPlanPrompt() core.Message {
	return core.Message{
		Text: "📋 **Plan 모드**: 진행하시겠습니까?",
		Actions: [][]core.Action{
			{
				{Label: "1️⃣ Auto mode", ActionID: "key:1"},
				{Label: "2️⃣ Manual approve", ActionID: "key:2"},
			},
			{
				{Label: "3️⃣ Ultraplan", ActionID: "key:3"},
				{Label: "4️⃣ Give feedback", ActionID: "key:4"},
			},
		},
		Category: core.CategoryInteractive,
	}
}

// FormatApprovalPrompt returns a message with numbered option buttons.
func FormatApprovalPrompt(dialogText string, optionCount int, isWizard bool) core.Message {
	if optionCount < 1 {
		optionCount = 2
	}
	var rows [][]core.Action
	var row []core.Action
	for i := 1; i <= optionCount; i++ {
		row = append(row, core.Action{Label: fmt.Sprintf("%d", i), ActionID: fmt.Sprintf("key:%d", i)})
		if len(row) == 5 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	if isWizard {
		rows = append(rows, []core.Action{
			{Label: "← 이전", ActionID: "key:Left"},
			{Label: "→ 다음", ActionID: "key:Tab"},
			{Label: "✖ 취소", ActionID: "key:Escape"},
		})
	} else {
		rows = append(rows, []core.Action{
			{Label: "↹ Tab", ActionID: "key:Tab"},
			{Label: "✖ 취소", ActionID: "key:Escape"},
		})
	}

	return core.Message{
		Text:         dialogText,
		Actions:      rows,
		Category:     core.CategoryInteractive,
		Preformatted: true,
	}
}

// FormatAskUserQuestion returns an interactive message with one button per option.
// Button ActionIDs use "arrowkey:N" (0-based index) so the handler can navigate
// the TUI cursor: Up×20 to reset, Down×N, then Enter.
func FormatAskUserQuestion(question string, options []string) core.Message {
	var rows [][]core.Action
	var row []core.Action
	for i, opt := range options {
		label := opt
		row = append(row, core.Action{
			Label:    label,
			ActionID: fmt.Sprintf("arrowkey:%d", i),
		})
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []core.Action{
		{Label: "✖ 취소", ActionID: "key:Escape"},
	})

	text := "❓ " + question
	return core.Message{
		Text:     text,
		Actions:  rows,
		Category: core.CategoryInteractive,
	}
}

// FormatInfoPanel returns an info-panel message with Tab/Close buttons.
func FormatInfoPanel(panelText string) core.Message {
	text := "ℹ️"
	if panelText != "" {
		text = "ℹ️\n" + panelText
	}
	return core.Message{
		Text: text,
		Actions: [][]core.Action{{
			{Label: "↹ Tab", ActionID: "key:Tab"},
			{Label: "✖ 닫기", ActionID: "key:Escape"},
		}},
		Category:     core.CategoryInteractive,
		Preformatted: true,
	}
}

// ── tool use ─────────────────────────────────────────────────────────────────

func formatToolUse(toolName string, input map[string]any) core.Message {
	summary := toolInputSummary(toolName, input)
	return core.Message{
		Text:     fmt.Sprintf("🔧 **%s**\n%s", toolName, summary),
		Category: core.CategoryTool,
	}
}

func toolInputSummary(toolName string, input map[string]any) string {
	str := func(key string) string {
		v, _ := input[key].(string)
		return v
	}
	switch toolName {
	case "Bash":
		cmd := str("command")
		firstLine := cmd
		if i := strings.Index(cmd, "\n"); i >= 0 {
			firstLine = cmd[:i]
		}
		if len(firstLine) > 300 {
			firstLine = firstLine[:300]
		}
		extra := ""
		if strings.Contains(cmd, "\n") {
			extra = fmt.Sprintf(" _(+%d 줄)_", strings.Count(cmd, "\n"))
		}
		return fmt.Sprintf("`%s`%s", firstLine, extra)
	case "Edit", "MultiEdit", "Write":
		path := str("file_path")
		if path == "" {
			path = str("path")
		}
		return fmt.Sprintf("`%s`", path)
	case "Read":
		return fmt.Sprintf("`%s`", str("file_path"))
	case "WebSearch", "WebFetch":
		val := str("query")
		if val == "" {
			val = str("url")
		}
		if len(val) > 200 {
			val = val[:200]
		}
		return fmt.Sprintf("`%s`", val)
	default:
		s := fmt.Sprintf("%v", input)
		if len(s) > 200 {
			s = s[:200]
		}
		return fmt.Sprintf("`%s`", s)
	}
}
