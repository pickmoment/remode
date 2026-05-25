// Package discord provides Discord bot platform implementation.
package discord

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/platform/mdtable"
)

const maxMsgLen = 2000

var (
	dcCodeSegRE    = regexp.MustCompile("(?s)```[\\s\\S]*?```|`[^`\n]+`")
	dcDoubleUndRE  = regexp.MustCompile(`(?s)__(.+?)__`)
	dcLinkRE       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	dcBlankLinesRE = regexp.MustCompile(`\n{3,}`)
)

// DiscordMessage is a Discord-ready message.
type DiscordMessage struct {
	Content    string
	Components []discordgo.MessageComponent
	Category   core.MessageCategory
}

// Render converts a platform-agnostic Message to one or more DiscordMessages.
func Render(msg core.Message, sessionPrefix string) []DiscordMessage {
	var body string
	if msg.Preformatted {
		if msg.Text != "" {
			body = "```\n" + msg.Text + "\n```"
		}
	} else {
		body = normalizeMD(msg.Text)
	}

	if sessionPrefix != "" {
		body = "**[" + sessionPrefix + "]**\n" + body
	}

	var components []discordgo.MessageComponent
	if len(msg.Actions) > 0 {
		components = actionsToComponents(msg.Actions)
	}

	chunks := splitText(body)
	result := make([]DiscordMessage, len(chunks))
	for i, chunk := range chunks {
		var comps []discordgo.MessageComponent
		if i == len(chunks)-1 {
			comps = components
		}
		result[i] = DiscordMessage{
			Content:    chunk,
			Components: comps,
			Category:   msg.Category,
		}
	}
	return result
}

// actionsToComponents converts action rows to Discord message components.
// Discord allows max 5 action rows, max 5 buttons per row.
func actionsToComponents(actions [][]core.Action) []discordgo.MessageComponent {
	var rows []discordgo.MessageComponent
	for rowIdx, row := range actions {
		if rowIdx >= 5 {
			break
		}
		var buttons []discordgo.MessageComponent
		for _, a := range row {
			if len(buttons) >= 5 {
				break
			}
			buttons = append(buttons, discordgo.Button{
				Label:    a.Label,
				Style:    discordgo.PrimaryButton,
				CustomID: a.ActionID,
			})
		}
		rows = append(rows, discordgo.ActionsRow{Components: buttons})
	}
	return rows
}

// normalizeMD adapts Markdown for Discord: converts __bold__, rewrites
// [text](url) links (not rendered in bot messages), and collapses excess blank lines.
// Code blocks and inline code are left unchanged.
func normalizeMD(text string) string {
	placeholders := make([]string, 0, 4)
	capture := func(value string) string {
		i := len(placeholders)
		placeholders = append(placeholders, value)
		return fmt.Sprintf("\x00%d\x00", i)
	}

	// Protect existing code blocks from table detection and other processing.
	text = dcCodeSegRE.ReplaceAllStringFunc(text, func(m string) string {
		return capture(m)
	})

	// Expand tables into vertical plain-text.
	text = mdtable.ReplaceAll(text)

	text = dcDoubleUndRE.ReplaceAllString(text, "**$1**")
	text = dcLinkRE.ReplaceAllString(text, "$1 ($2)")
	text = dcBlankLinesRE.ReplaceAllString(text, "\n\n")

	for i, val := range placeholders {
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00%d\x00", i), val)
	}
	return text
}

func splitText(text string) []string {
	if len(text) <= maxMsgLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxMsgLen {
			chunks = append(chunks, text)
			break
		}
		splitAt := strings.LastIndex(text[:maxMsgLen], "\n")
		if splitAt == -1 {
			splitAt = maxMsgLen
		}
		chunks = append(chunks, text[:splitAt])
		text = strings.TrimLeft(text[splitAt:], "\n")
	}
	return chunks
}
