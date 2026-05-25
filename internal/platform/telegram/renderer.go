// Package telegram provides Telegram bot platform implementation.
package telegram

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/platform/mdtable"
)

const maxMsgLen = 4096

// TelegramMessage is a Telegram-ready message.
type TelegramMessage struct {
	Text        string
	ParseMode   string
	ReplyMarkup *tgbotapi.InlineKeyboardMarkup
	Category    core.MessageCategory
}

// Render converts a platform-agnostic Message to one or more TelegramMessages.
// Long messages are split at newline boundaries.
func Render(msg core.Message, sessionPrefix string) []TelegramMessage {
	var body string
	if msg.Preformatted {
		if msg.Text != "" {
			body = "<pre>" + html.EscapeString(msg.Text) + "</pre>"
		}
	} else {
		body = mdToHTML(msg.Text)
	}

	if sessionPrefix != "" {
		body = "<b>[" + html.EscapeString(sessionPrefix) + "]</b>\n" + body
	}

	var markup *tgbotapi.InlineKeyboardMarkup
	if len(msg.Actions) > 0 {
		m := actionsToMarkup(msg.Actions)
		markup = &m
	}

	chunks := splitHTML(body)
	result := make([]TelegramMessage, len(chunks))
	for i, chunk := range chunks {
		var rm *tgbotapi.InlineKeyboardMarkup
		if i == len(chunks)-1 {
			rm = markup
		}
		result[i] = TelegramMessage{
			Text:        chunk,
			ParseMode:   "HTML",
			ReplyMarkup: rm,
			Category:    msg.Category,
		}
	}
	return result
}

func actionsToMarkup(actions [][]core.Action) tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, len(actions))
	for i, row := range actions {
		buttons := make([]tgbotapi.InlineKeyboardButton, len(row))
		for j, a := range row {
			buttons[j] = tgbotapi.NewInlineKeyboardButtonData(a.Label, a.ActionID)
		}
		rows[i] = buttons
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

var (
	tgCodeBlockRE  = regexp.MustCompile("(?s)```(\\w*)\\n?([\\s\\S]*?)\\n?```")
	tgInlineCodeRE = regexp.MustCompile("`([^`\n]+)`")
	tgLinkRE       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	tgHRRE         = regexp.MustCompile(`^[-*_]{3,}$`)
	tgHeadingRE    = regexp.MustCompile(`^(#{1,3}) +(.+)$`)
	tgBulletRE     = regexp.MustCompile(`^[-*] (.+)$`)
	tgBlockquoteRE = regexp.MustCompile(`^>[ ]?(.*)$`)
	tgStrikeRE     = regexp.MustCompile(`~~(.+?)~~`)
	tgBoldStarRE   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	tgBoldUndRE    = regexp.MustCompile(`__(.+?)__`)
	tgItalicRE     = regexp.MustCompile(`\*([^*\n]+?)\*`)
)

func mdToHTML(text string) string {
	if text == "" {
		return ""
	}

	placeholders := make([]string, 0, 8)
	capture := func(value string) string {
		i := len(placeholders)
		placeholders = append(placeholders, value)
		return fmt.Sprintf("\x00%d\x00", i)
	}

	// Extract code blocks before tables so table detection skips code content.
	text = tgCodeBlockRE.ReplaceAllStringFunc(text, func(m string) string {
		sub := tgCodeBlockRE.FindStringSubmatch(m)
		return capture("<pre><code>" + html.EscapeString(sub[2]) + "</code></pre>")
	})
	// Expand tables into vertical plain-text before inline code extraction.
	text = mdtable.ReplaceAll(text)
	text = tgInlineCodeRE.ReplaceAllStringFunc(text, func(m string) string {
		return capture("<code>" + html.EscapeString(m[1:len(m)-1]) + "</code>")
	})
	text = tgLinkRE.ReplaceAllStringFunc(text, func(m string) string {
		sub := tgLinkRE.FindStringSubmatch(m)
		return capture(`<a href="` + sub[2] + `">` + html.EscapeString(sub[1]) + `</a>`)
	})

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		switch {
		case tgHRRE.MatchString(line):
			lines[i] = "──────────"
		case tgHeadingRE.MatchString(line):
			m := tgHeadingRE.FindStringSubmatch(line)
			inner := tgInline(m[2])
			switch len(m[1]) {
			case 1:
				lines[i] = "<b>" + strings.ToUpper(inner) + "</b>"
			case 2:
				lines[i] = "<b>" + inner + "</b>"
			default:
				lines[i] = "<b><i>" + inner + "</i></b>"
			}
		case tgBulletRE.MatchString(line):
			m := tgBulletRE.FindStringSubmatch(line)
			lines[i] = "• " + tgInline(m[1])
		case tgBlockquoteRE.MatchString(line):
			m := tgBlockquoteRE.FindStringSubmatch(line)
			lines[i] = "│ " + tgInline(m[1])
		default:
			lines[i] = tgInline(line)
		}
	}

	result := strings.Join(lines, "\n")
	for i, val := range placeholders {
		result = strings.ReplaceAll(result, fmt.Sprintf("\x00%d\x00", i), val)
	}
	return result
}

func tgInline(s string) string {
	s = html.EscapeString(s)
	s = tgStrikeRE.ReplaceAllString(s, "<s>$1</s>")
	s = tgBoldStarRE.ReplaceAllString(s, "<b>$1</b>")
	s = tgBoldUndRE.ReplaceAllString(s, "<b>$1</b>")
	s = tgItalicRE.ReplaceAllString(s, "<i>$1</i>")
	return s
}

// splitHTML splits text into chunks of at most maxMsgLen, breaking at newlines.
func splitHTML(text string) []string {
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
