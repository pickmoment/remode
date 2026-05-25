// Package claudecode drives Claude Code via tmux.
package claudecode

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/pickmoment/remode/internal/core"
)

var (
	planPatterns = []string{
		"Would you like to proceed",
		"Accept plan",
		"Keep planning",
	}
	trustPatterns = []string{
		"Yes, I trust this folder",
		"Enter to confirm",
	}
	// matches "❯ 1" or "> 1" or "  1." at start of a line
	optionLineRE = regexp.MustCompile(`(?m)[>❯]\s*1\b|^\s{0,6}1\.`)
)

// tmuxCreate starts a new tmux session and launches claude inside it.
// If resumeID is non-empty, claude is invoked with --resume.
func tmuxCreate(sess *core.Session, settingsPath string, resumeID string) error {
	// Kill existing session with the same name
	exec.Command("tmux", "kill-session", "-t", sess.TmuxName).Run() //nolint:errcheck

	var cmd string
	if resumeID != "" {
		cmd = fmt.Sprintf(
			"claude --resume %s -n %s --settings %s --dangerously-skip-permissions",
			resumeID, sess.Name, settingsPath,
		)
	} else {
		cmd = fmt.Sprintf(
			"claude -n %s --settings %s --dangerously-skip-permissions",
			sess.Name, settingsPath,
		)
	}

	// Create the tmux session
	if err := exec.Command(
		"tmux", "new-session", "-d", "-s", sess.TmuxName, "-c", sess.CWD,
	).Run(); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}

	// Launch claude in the pane
	if err := exec.Command("tmux", "send-keys", "-t", sess.TmuxName, cmd, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w", err)
	}
	return nil
}

// tmuxSendText sends text followed by Enter. Multiline text uses bracketed paste.
func tmuxSendText(sess *core.Session, text string) error {
	target := sess.TmuxName
	if strings.Contains(text, "\n") {
		// Write to a temp file and use tmux load-buffer / paste-buffer
		f, err := os.CreateTemp("", "remode-paste-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err := f.WriteString(text); err != nil {
			f.Close()
			return err
		}
		f.Close()
		if err := exec.Command("tmux", "load-buffer", f.Name()).Run(); err != nil {
			return fmt.Errorf("tmux load-buffer: %w", err)
		}
		if err := exec.Command("tmux", "paste-buffer", "-d", "-t", target).Run(); err != nil {
			return fmt.Errorf("tmux paste-buffer: %w", err)
		}
		return exec.Command("tmux", "send-keys", "-t", target, "", "Enter").Run()
	}
	if err := exec.Command("tmux", "send-keys", "-t", target, "-l", text).Run(); err != nil {
		return err
	}
	return exec.Command("tmux", "send-keys", "-t", target, "Enter").Run()
}

// tmuxSendKey sends a tmux key name (e.g. "1", "Tab", "Escape").
func tmuxSendKey(sess *core.Session, key string) error {
	return exec.Command("tmux", "send-keys", "-t", sess.TmuxName, key).Run()
}

// tmuxCapture returns the visible text of the active pane, trailing blanks stripped.
func tmuxCapture(sess *core.Session) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", sess.TmuxName).Output()
	if err != nil {
		return "", fmt.Errorf("capture-pane: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	// strip trailing blank lines
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	// strip trailing spaces per line
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n"), nil
}

// tmuxKill sends /exit then kills the tmux session.
func tmuxKill(sess *core.Session) error {
	exec.Command("tmux", "send-keys", "-t", sess.TmuxName, "/exit", "Enter").Run() //nolint:errcheck
	return exec.Command("tmux", "kill-session", "-t", sess.TmuxName).Run()
}

// tmuxExists reports whether the named tmux session is alive.
func tmuxExists(sess *core.Session) bool {
	return exec.Command("tmux", "has-session", "-t", sess.TmuxName).Run() == nil
}

// ── TUI content detection ─────────────────────────────────────────────────────

func IsPlanBanner(content string) bool {
	for _, p := range planPatterns {
		if strings.Contains(content, p) {
			return true
		}
	}
	return false
}

func hasDialogNavigation(content string) bool {
	return strings.Contains(content, "Esc to cancel") ||
		strings.Contains(content, "Tab/Arrow keys to navigate")
}

func IsApprovalDialog(content string) bool {
	return hasDialogNavigation(content) && optionLineRE.MatchString(content)
}

func IsInfoPanel(content string) bool {
	return hasDialogNavigation(content) && !optionLineRE.MatchString(content)
}

func IsMultistepWizard(content string) bool {
	return strings.Contains(content, "Tab/Arrow keys to navigate")
}

// ── text extraction ───────────────────────────────────────────────────────────

func findDialogEnd(lines []string) int {
	for i, l := range lines {
		if strings.Contains(l, "Esc to cancel") || strings.Contains(l, "Tab/Arrow keys to navigate") {
			return i
		}
	}
	return len(lines)
}

func ExtractInfoPanelText(content string) string {
	lines := strings.Split(content, "\n")
	end := findDialogEnd(lines)
	body := lines[:end]
	// trim leading blank lines
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	text := strings.Join(body, "\n")
	if len(text) > 800 {
		return text[:800]
	}
	return text
}

func ExtractApprovalText(content string) string {
	lines := strings.Split(content, "\n")
	endIdx := findDialogEnd(lines)

	// Find the line containing the first option (❯ 1 or 1.)
	firstOpt := -1
	for i := endIdx - 1; i >= 0; i-- {
		if optionLineRE.MatchString(lines[i]) {
			firstOpt = i
			break
		}
	}
	if firstOpt == -1 {
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

	var dialogLines []string
	end := endIdx
	if end >= len(lines) {
		end = len(lines) - 1
	}
	for _, l := range lines[q : end+1] {
		if strings.TrimSpace(l) != "" {
			dialogLines = append(dialogLines, strings.TrimRight(l, " "))
		}
	}
	return strings.Join(dialogLines, "\n")
}

func ExtractQuestionLine(content string) string {
	lines := strings.Split(content, "\n")
	endIdx := findDialogEnd(lines)

	firstOpt := -1
	for i := endIdx - 1; i >= 0; i-- {
		if optionLineRE.MatchString(lines[i]) {
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

func IsTrustPrompt(content string) bool {
	for _, p := range trustPatterns {
		if strings.Contains(content, p) {
			return true
		}
	}
	return false
}
