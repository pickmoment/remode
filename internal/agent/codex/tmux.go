package codex

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

var (
	codexPlanPatterns = []string{
		"Do you want to proceed",
		"Approve this plan",
		"Plan approved",
	}
	codexApprovalPatterns = []string{
		"Allow this action",
		"Allow command",
		"Approve",
	}
	codexOptionRE = regexp.MustCompile(`(?m)^\s*1\.`)
)

func codexCreate(sess *core.Session, settingsPath, model, resumeID string) error {
	exec.Command("tmux", "kill-session", "-t", sess.TmuxName).Run() //nolint:errcheck

	if err := exec.Command(
		"tmux", "new-session", "-d", "-s", sess.TmuxName, "-c", sess.CWD,
	).Run(); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}

	bypass := "--dangerously-bypass-approvals-and-sandbox"
	var cmd string
	if resumeID != "" {
		cmd = fmt.Sprintf("codex resume %s %s", resumeID, bypass)
	} else {
		cmd = fmt.Sprintf("codex %s", bypass)
	}
	if model != "" {
		cmd += " -m " + model
	}

	return exec.Command("tmux", "send-keys", "-t", sess.TmuxName, cmd, "Enter").Run()
}

func codexSendText(sess *core.Session, text string) error {
	// Always use bracketed paste, then send Enter after a brief settle
	target := sess.TmuxName
	payload := "\x1b[200~" + text + "\x1b[201~"
	if err := exec.Command("tmux", "send-keys", "-t", target, payload).Run(); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return exec.Command("tmux", "send-keys", "-t", target, "Enter").Run()
}

func codexSendKey(sess *core.Session, key string) error {
	return exec.Command("tmux", "send-keys", "-t", sess.TmuxName, key).Run()
}

func codexCapture(sess *core.Session) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", sess.TmuxName).Output()
	if err != nil {
		return "", fmt.Errorf("capture-pane: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n"), nil
}

func codexKill(sess *core.Session) error {
	exec.Command("tmux", "send-keys", "-t", sess.TmuxName, "q", "Enter").Run() //nolint:errcheck
	time.Sleep(500 * time.Millisecond)
	return exec.Command("tmux", "kill-session", "-t", sess.TmuxName).Run()
}

func codexExists(sess *core.Session) bool {
	return exec.Command("tmux", "has-session", "-t", sess.TmuxName).Run() == nil
}

// ── TUI detection ─────────────────────────────────────────────────────────────

func codexIsPlanBanner(content string) bool {
	for _, p := range codexPlanPatterns {
		if strings.Contains(content, p) {
			return true
		}
	}
	return false
}

func codexIsApprovalDialog(content string) bool {
	if !strings.Contains(content, "Esc") {
		return false
	}
	for _, p := range codexApprovalPatterns {
		if strings.Contains(content, p) {
			return codexOptionRE.MatchString(content)
		}
	}
	return false
}

func codexIsInfoPanel(content string) bool {
	return strings.Contains(content, "Esc") && !codexOptionRE.MatchString(content)
}

func codexIsMultistepWizard(content string) bool {
	return strings.Contains(content, "Tab") && strings.Contains(content, "Arrow")
}

func codexExtractApprovalText(content string) string {
	lines := strings.Split(content, "\n")
	escIdx := -1
	for i, l := range lines {
		if strings.Contains(l, "Esc") {
			escIdx = i
			break
		}
	}
	if escIdx < 0 {
		tail := content
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return tail
	}
	firstOpt := -1
	for i := escIdx - 1; i >= 0; i-- {
		if codexOptionRE.MatchString(lines[i]) {
			firstOpt = i
			break
		}
	}
	if firstOpt < 0 {
		tail := content
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return tail
	}
	q := firstOpt - 1
	for q >= 0 && strings.TrimSpace(lines[q]) == "" {
		q--
	}
	var result []string
	end := escIdx + 1
	if end > len(lines) {
		end = len(lines)
	}
	for _, l := range lines[q:end] {
		if strings.TrimSpace(l) != "" {
			result = append(result, strings.TrimRight(l, " "))
		}
	}
	return strings.Join(result, "\n")
}

func codexExtractQuestionLine(content string) string {
	lines := strings.Split(content, "\n")
	escIdx := -1
	for i, l := range lines {
		if strings.Contains(l, "Esc") {
			escIdx = i
			break
		}
	}
	if escIdx < 0 {
		return ""
	}
	firstOpt := -1
	for i := escIdx - 1; i >= 0; i-- {
		if codexOptionRE.MatchString(lines[i]) {
			firstOpt = i
			break
		}
	}
	if firstOpt <= 0 {
		return ""
	}
	q := firstOpt - 1
	for q >= 0 && strings.TrimSpace(lines[q]) == "" {
		q--
	}
	if q < 0 {
		return ""
	}
	return strings.TrimSpace(lines[q])
}

func codexExtractInfoPanelText(content string) string {
	lines := strings.Split(content, "\n")
	escIdx := len(lines)
	for i, l := range lines {
		if strings.Contains(l, "Esc") {
			escIdx = i
			break
		}
	}
	var body []string
	for _, l := range lines[:escIdx] {
		if strings.TrimSpace(l) != "" {
			body = append(body, l)
		}
	}
	text := strings.Join(body, "\n")
	if len(text) > 800 {
		return text[:800]
	}
	return text
}
