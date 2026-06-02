package web

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// projectMessage is one turn in a Claude Code session conversation.
type projectMessage struct {
	Role      string `json:"role"`      // "user" | "assistant" | "tool"
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
}

// GET /api/projects/{sessionID}/messages
func (s *Server) handleGetProjectSessionMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if !isValidSessionID(sessionID) {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	jsonlPath := s.sm.FindSessionJSONL(sessionID)
	if jsonlPath == "" {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	msgs, err := parseSessionMessages(jsonlPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, msgs)
}

// isValidSessionID rejects non-UUID inputs before they reach the filesystem.
func isValidSessionID(s string) bool {
	if len(s) < 10 || len(s) > 50 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// parseSessionMessages reads a Claude Code JSONL file and returns conversation turns.
func parseSessionMessages(jsonlPath string) ([]projectMessage, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	var msgs []projectMessage
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entryType, _ := entry["type"].(string)
		ts, _ := entry["timestamp"].(string)
		switch entryType {
		case "user":
			msgs = append(msgs, parseUserBlocks(entry, ts)...)
		case "assistant":
			msgs = append(msgs, parseAssistantBlocks(entry, ts)...)
		}
	}
	return msgs, scanner.Err()
}

func parseUserBlocks(entry map[string]any, ts string) []projectMessage {
	msg, _ := entry["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	switch v := msg["content"].(type) {
	case string:
		t := strings.TrimSpace(v)
		if t == "" {
			return nil
		}
		return []projectMessage{{Role: "user", Text: t, Timestamp: ts}}
	case []any:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "text" {
				if t, ok := m["text"].(string); ok {
					if t = strings.TrimSpace(t); t != "" {
						parts = append(parts, t)
					}
				}
			}
			// tool_result blocks are skipped (tool output fed back to model, not user text)
		}
		if len(parts) == 0 {
			return nil
		}
		return []projectMessage{{Role: "user", Text: strings.Join(parts, "\n"), Timestamp: ts}}
	}
	return nil
}

func parseAssistantBlocks(entry map[string]any, ts string) []projectMessage {
	msg, _ := entry["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	rawContent, _ := msg["content"].([]any)

	var textParts []string
	var toolMsgs []projectMessage
	for _, item := range rawContent {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "text":
			if t, ok := m["text"].(string); ok {
				t = strings.TrimSpace(t)
				if t != "" && t != "No response requested" {
					textParts = append(textParts, t)
				}
			}
		case "tool_use":
			name, _ := m["name"].(string)
			if name == "" {
				continue
			}
			input, _ := m["input"].(map[string]any)
			toolMsgs = append(toolMsgs, projectMessage{
				Role:      "tool",
				Text:      formatToolUse(name, input),
				Timestamp: ts,
			})
		// "thinking" blocks are skipped
		}
	}

	var result []projectMessage
	if len(textParts) > 0 {
		result = append(result, projectMessage{
			Role:      "assistant",
			Text:      strings.Join(textParts, "\n\n"),
			Timestamp: ts,
		})
	}
	return append(result, toolMsgs...)
}

func formatToolUse(name string, input map[string]any) string {
	if len(input) == 0 {
		return name
	}
	// Show the most human-readable primary parameter
	for _, key := range []string{"command", "file_path", "path", "query", "url", "pattern", "description", "prompt"} {
		if v, ok := input[key]; ok {
			s := fmt.Sprintf("%v", v)
			if len(s) > 100 {
				s = s[:97] + "…"
			}
			return name + ": " + s
		}
	}
	// Fallback: first key found
	for k, v := range input {
		s := fmt.Sprintf("%v", v)
		if len(s) > 80 {
			s = s[:77] + "…"
		}
		return fmt.Sprintf("%s(%s: %s)", name, k, s)
	}
	return name
}
