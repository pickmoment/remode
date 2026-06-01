package claudecode

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

var suppressedTexts = map[string]bool{
	"No response requested": true,
}

// Agent implements core.AIAgent for Claude Code (tmux + JSONL).
type Agent struct {
	pollInterval time.Duration
	dialogGrace  time.Duration
}

// New returns an Agent with a 500 ms TUI poll interval and 800 ms dialog grace.
func New() *Agent {
	return &Agent{pollInterval: 500 * time.Millisecond, dialogGrace: 800 * time.Millisecond}
}

// NewWithPoll returns an Agent with a custom TUI poll interval and 800 ms dialog grace.
func NewWithPoll(poll time.Duration) *Agent {
	return &Agent{pollInterval: poll, dialogGrace: 800 * time.Millisecond}
}

// NewWithPollAndGrace returns an Agent with custom TUI poll interval and dialog grace delay.
func NewWithPollAndGrace(poll, grace time.Duration) *Agent {
	return &Agent{pollInterval: poll, dialogGrace: grace}
}

// ── lifecycle ─────────────────────────────────────────────────────────────────

func (a *Agent) Create(ctx context.Context, sess *core.Session, settingsPath string) error {
	return tmuxCreate(sess, settingsPath, "")
}

func (a *Agent) Resume(ctx context.Context, sess *core.Session, settingsPath, resumeID string) error {
	return tmuxCreate(sess, settingsPath, resumeID)
}

func (a *Agent) SendInput(sess *core.Session, text string) error {
	return tmuxSendText(sess, text)
}

func (a *Agent) SendKey(sess *core.Session, key string) error {
	return tmuxSendKey(sess, key)
}

func (a *Agent) Capture(sess *core.Session) (string, error) {
	return tmuxCapture(sess)
}

func (a *Agent) Kill(sess *core.Session) error {
	return tmuxKill(sess)
}

func (a *Agent) Exists(sess *core.Session) bool {
	return tmuxExists(sess)
}

// ── discovery ─────────────────────────────────────────────────────────────────

func (a *Agent) Discover(
	ctx context.Context,
	sess *core.Session,
	existing map[string]struct{},
	projectsDir string,
) (sessionID, outputPath string, err error) {
	projDir := ProjectDirFor(sess.CWD, projectsDir)
	for {
		if info, err := os.Stat(projDir); err == nil && info.IsDir() {
			entries, _ := os.ReadDir(projDir)
			var candidates []string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
					full := filepath.Join(projDir, e.Name())
					if _, known := existing[full]; !known {
						candidates = append(candidates, full)
					}
				}
			}
			if len(candidates) > 0 {
				// Pick the most recently modified
				sort.Slice(candidates, func(i, j int) bool {
					si, _ := os.Stat(candidates[i])
					sj, _ := os.Stat(candidates[j])
					if si == nil || sj == nil {
						return false
					}
					return si.ModTime().After(sj.ModTime())
				})
				found := candidates[0]
				sid := strings.TrimSuffix(filepath.Base(found), ".jsonl")
				return sid, found, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// ── event streaming ───────────────────────────────────────────────────────────

func (a *Agent) WatchEvents(
	ctx context.Context,
	sess *core.Session,
	onEvent func(core.AgentEvent),
	updater core.SessionUpdater,
	settleMS int,
) error {
	var wg sync.WaitGroup
	wg.Add(2)

	jsonlCtx, cancelJSONL := context.WithCancel(ctx)
	monCtx, cancelMon := context.WithCancel(ctx)
	defer cancelJSONL()
	defer cancelMon()

	// JSONL watcher goroutine
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("claudecode JSONL goroutine panic (%s): %v", sess.Name, r)
			}
		}()
		onEntry := func(entry map[string]any) {
			for _, ev := range parseEntry(entry) {
				onEvent(ev)
			}
		}
		for {
			err := Watch(jsonlCtx, sess, onEntry, updater, settleMS)
			if jsonlCtx.Err() != nil {
				return
			}
			if err != nil {
				log.Printf("claudecode watcher crashed (%s): %v, restarting in 5s", sess.Name, err)
				select {
				case <-jsonlCtx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}
	}()

	// TUI monitor goroutine
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("claudecode TUI monitor panic (%s): %v", sess.Name, r)
			}
		}()
		ticker := time.NewTicker(a.pollInterval)
		defer ticker.Stop()

		var prompted string
		var lastInfoText string
		var lastQuestionLine string
		var wasInWizard bool // true after a multi-step wizard dialog is detected

		// graceWait pauses for dialogGrace to let the JSONL watcher deliver any
		// preceding text messages before we emit an interactive-prompt event.
		// Returns false if the context is cancelled (caller should return).
		graceWait := func() bool {
			if a.dialogGrace <= 0 {
				return true
			}
			select {
			case <-monCtx.Done():
				return false
			case <-time.After(a.dialogGrace):
				return true
			}
		}

		for {
			select {
			case <-monCtx.Done():
				return
			case <-ticker.C:
				content, err := tmuxCapture(sess)
				if err != nil {
					continue
				}
				if IsPlanBanner(content) {
					if prompted != "plan" {
						if !graceWait() {
							return
						}
						c2, err2 := tmuxCapture(sess)
						if err2 != nil || !IsPlanBanner(c2) {
							continue
						}
						onEvent(core.AgentEvent{Type: core.EventPlanPrompt})
						prompted = "plan"
					}
				} else if IsApprovalDialog(content) {
					question := ExtractQuestionLine(content)
					if prompted != "approval" || question != lastQuestionLine {
						if !graceWait() {
							return
						}
						c2, err2 := tmuxCapture(sess)
						if err2 != nil || !IsApprovalDialog(c2) {
							continue
						}
						dialogText := ExtractApprovalText(c2)
						isWizard := IsMultistepWizard(c2)
						if isWizard {
							wasInWizard = true
						}
						nOpts := countOptions(dialogText)
						onEvent(core.AgentEvent{
							Type:        core.EventApprovalPrompt,
							DialogText:  dialogText,
							OptionCount: nOpts,
							IsWizard:    isWizard,
						})
						prompted = "approval"
						lastQuestionLine = ExtractQuestionLine(c2)
					}
				} else if IsTextOptionDialog(content) {
					question := ExtractQuestionLine(content)
					if IsMultistepWizard(content) {
						wasInWizard = true
					}
					if prompted != "ask_user" || question != lastQuestionLine {
						if !graceWait() {
							return
						}
						c2, err2 := tmuxCapture(sess)
						if err2 != nil || !IsTextOptionDialog(c2) {
							continue
						}
						if IsMultistepWizard(c2) {
							wasInWizard = true
						}
						opts := ExtractNonNumberedOptions(c2)
						if len(opts) > 0 {
							onEvent(core.AgentEvent{
								Type:        core.EventAskUserQuestion,
								AskQuestion: ExtractQuestionLine(c2),
								AskOptions:  opts,
							})
						}
						prompted = "ask_user"
						lastQuestionLine = ExtractQuestionLine(c2)
					}
				} else if wasInWizard && IsWizardFinalStep(content) {
					// "Ready to submit your answers?" — final step of a multi-step wizard.
					// Has numbered options but no standard navigation hints.
					question := ExtractQuestionLine(content)
					if prompted != "approval" || question != lastQuestionLine {
						if !graceWait() {
							return
						}
						c2, err2 := tmuxCapture(sess)
						if err2 != nil {
							wasInWizard = false
							continue
						}
						dialogText := ExtractApprovalText(c2)
						nOpts := countOptions(dialogText)
						onEvent(core.AgentEvent{
							Type:        core.EventApprovalPrompt,
							DialogText:  dialogText,
							OptionCount: nOpts,
							IsWizard:    false,
						})
						prompted = "approval"
						lastQuestionLine = ExtractQuestionLine(c2)
						wasInWizard = false
					}
				} else if IsInfoPanel(content) {
					panelText := ExtractInfoPanelText(content)
					if prompted != "info_panel" || panelText != lastInfoText {
						if !graceWait() {
							return
						}
						c2, err2 := tmuxCapture(sess)
						if err2 != nil || !IsInfoPanel(c2) {
							continue
						}
						refreshedText := ExtractInfoPanelText(c2)
						onEvent(core.AgentEvent{
							Type:      core.EventInfoPanel,
							PanelText: refreshedText,
						})
						prompted = "info_panel"
						lastInfoText = refreshedText
					}
				} else {
					prompted = ""
					lastInfoText = ""
					lastQuestionLine = ""
				}
			}
		}
	}()

	wg.Wait()
	return nil
}

// ── project helpers ───────────────────────────────────────────────────────────

func (a *Agent) ListProjects(projectsDir string) ([]core.Project, error) {
	return ListProjects(projectsDir)
}

func (a *Agent) ProjectDirFor(cwd, projectsDir string) string {
	return ProjectDirFor(cwd, projectsDir)
}

func (a *Agent) NewestOutput(sess *core.Session, projectsDir string) string {
	projDir := ProjectDirFor(sess.CWD, projectsDir)
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return ""
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			best = filepath.Join(projDir, e.Name())
		}
	}
	return best
}

func (a *Agent) OutputSnapshot(sess *core.Session, projectsDir string) map[string]struct{} {
	projDir := ProjectDirFor(sess.CWD, projectsDir)
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return map[string]struct{}{}
	}
	out := make(map[string]struct{})
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			out[filepath.Join(projDir, e.Name())] = struct{}{}
		}
	}
	return out
}

// ── JSONL parsing ─────────────────────────────────────────────────────────────

func parseEntry(entry map[string]any) []core.AgentEvent {
	entryType, _ := entry["type"].(string)
	if entryType != "assistant" {
		return nil
	}
	return parseAssistant(entry)
}

func parseAssistant(entry map[string]any) []core.AgentEvent {
	msg, _ := entry["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	rawContent, _ := msg["content"].([]any)
	var events []core.AgentEvent
	for _, item := range rawContent {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		itemType, _ := m["type"].(string)
		switch itemType {
		case "text":
			text := strings.TrimSpace(fmt.Sprintf("%v", m["text"]))
			if text != "" && !suppressedTexts[text] {
				events = append(events, core.AgentEvent{Type: core.EventText, Text: text})
			}
		case "tool_use":
			name, _ := m["name"].(string)
			input, _ := m["input"].(map[string]any)
			if name == "AskUserQuestion" {
				continue
			}
			events = append(events, core.AgentEvent{
				Type:      core.EventToolUse,
				ToolName:  name,
				ToolInput: input,
			})
		}
	}
	return events
}

var numOptRE = regexp.MustCompile(`(?m)^\s*[❯>]?\s*(\d+)\.`)

func countOptions(dialogText string) int {
	matches := numOptRE.FindAllStringSubmatch(dialogText, -1)
	max := 2
	for _, m := range matches {
		var n int
		fmt.Sscanf(m[1], "%d", &n)
		if n > max {
			max = n
		}
	}
	return max
}
