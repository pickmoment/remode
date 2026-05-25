package codex

import (
	"context"
	"encoding/json"
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

var codexNumOptRE = regexp.MustCompile(`(?m)^\s*(\d+)\.`)

// Agent implements core.AIAgent for Codex CLI (tmux + JSONL).
type Agent struct {
	pollInterval time.Duration
	model        string
	sessionsDir  string
}

// New returns an Agent with defaults (~/.codex/sessions, 500 ms poll).
func New() *Agent {
	home, _ := os.UserHomeDir()
	return &Agent{
		pollInterval: 500 * time.Millisecond,
		sessionsDir:  filepath.Join(home, ".codex", "sessions"),
	}
}

// NewWithConfig returns an Agent with custom settings.
func NewWithConfig(poll time.Duration, model, sessionsDir string) *Agent {
	if sessionsDir == "" {
		home, _ := os.UserHomeDir()
		sessionsDir = filepath.Join(home, ".codex", "sessions")
	}
	return &Agent{pollInterval: poll, model: model, sessionsDir: sessionsDir}
}

// ── lifecycle ─────────────────────────────────────────────────────────────────

func (a *Agent) Create(_ context.Context, sess *core.Session, settingsPath string) error {
	return codexCreate(sess, settingsPath, a.model, "")
}

func (a *Agent) Resume(_ context.Context, sess *core.Session, settingsPath, resumeID string) error {
	return codexCreate(sess, settingsPath, a.model, resumeID)
}

func (a *Agent) SendInput(sess *core.Session, text string) error {
	return codexSendText(sess, text)
}

func (a *Agent) SendKey(sess *core.Session, key string) error {
	return codexSendKey(sess, key)
}

func (a *Agent) Capture(sess *core.Session) (string, error) {
	return codexCapture(sess)
}

func (a *Agent) Kill(sess *core.Session) error {
	return codexKill(sess)
}

func (a *Agent) Exists(sess *core.Session) bool {
	return codexExists(sess)
}

// ── discovery ─────────────────────────────────────────────────────────────────

func (a *Agent) Discover(
	ctx context.Context,
	sess *core.Session,
	existing map[string]struct{},
	_ string,
) (sessionID, outputPath string, err error) {
	for {
		for _, path := range iterJSONLFiles(a.sessionsDir) {
			if _, known := existing[path]; known {
				continue
			}
			meta := ReadSessionMeta(path)
			if cwd, _ := meta["cwd"].(string); cwd == sess.CWD {
				if id, _ := meta["id"].(string); id != "" {
					return id, path, nil
				}
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
	// Resolve the real JSONL path if needed
	if _, err := os.Stat(sess.JSONLPath); err != nil && sess.SessionID != "" {
		if actual := FindJSONLByID(sess.SessionID, a.sessionsDir); actual != "" {
			sess.JSONLPath = actual
			updater.Save(sess) //nolint:errcheck
			log.Printf("codex: resolved JSONL for %s → %s", sess.Name, filepath.Base(actual))
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)

	jsonlCtx, cancelJSONL := context.WithCancel(ctx)
	monCtx, cancelMon := context.WithCancel(ctx)
	defer cancelJSONL()
	defer cancelMon()

	go func() {
		defer wg.Done()
		onEntry := func(entry map[string]any) {
			for _, ev := range codexParseEntry(entry) {
				onEvent(ev)
			}
		}
		for {
			// Use the same Watch helper as claudecode (it's generic JSONL tail)
			err := watchCodexJSONL(jsonlCtx, sess, onEntry, updater, settleMS)
			if jsonlCtx.Err() != nil {
				return
			}
			if err != nil {
				log.Printf("codex watcher crashed (%s): %v, restarting in 5s", sess.Name, err)
				select {
				case <-jsonlCtx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(a.pollInterval)
		defer ticker.Stop()

		var prompted string
		var lastInfoText, lastQuestion string

		for {
			select {
			case <-monCtx.Done():
				return
			case <-ticker.C:
				content, err := codexCapture(sess)
				if err != nil {
					continue
				}
				if codexIsPlanBanner(content) {
					if prompted != "plan" {
						onEvent(core.AgentEvent{Type: core.EventPlanPrompt})
						prompted = "plan"
					}
				} else if codexIsApprovalDialog(content) {
					question := codexExtractQuestionLine(content)
					if prompted != "approval" || question != lastQuestion {
						dialogText := codexExtractApprovalText(content)
						nOpts := countCodexOptions(dialogText)
						onEvent(core.AgentEvent{
							Type:        core.EventApprovalPrompt,
							DialogText:  dialogText,
							OptionCount: nOpts,
							IsWizard:    false,
						})
						prompted = "approval"
						lastQuestion = question
					}
				} else if codexIsInfoPanel(content) {
					panelText := codexExtractInfoPanelText(content)
					if prompted != "info_panel" || panelText != lastInfoText {
						onEvent(core.AgentEvent{Type: core.EventInfoPanel, PanelText: panelText})
						prompted = "info_panel"
						lastInfoText = panelText
					}
				} else {
					prompted = ""
					lastInfoText = ""
					lastQuestion = ""
				}
			}
		}
	}()

	wg.Wait()
	return nil
}

// ── project helpers ───────────────────────────────────────────────────────────

func (a *Agent) ListProjects(_ string) ([]core.Project, error) {
	return ListProjects(a.sessionsDir)
}

func (a *Agent) ProjectDirFor(_, _ string) string {
	return a.sessionsDir
}

func (a *Agent) NewestOutput(sess *core.Session, _ string) string {
	var best string
	var bestTime time.Time
	for _, path := range iterJSONLFiles(a.sessionsDir) {
		meta := ReadSessionMeta(path)
		if cwd, _ := meta["cwd"].(string); cwd != sess.CWD {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			best = path
		}
	}
	return best
}

func (a *Agent) OutputSnapshot(_ *core.Session, _ string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, path := range iterJSONLFiles(a.sessionsDir) {
		out[path] = struct{}{}
	}
	return out
}

// ── JSONL watcher ─────────────────────────────────────────────────────────────

// watchCodexJSONL watches a single Codex JSONL file for new lines.
// Unlike the claude_code watcher, it watches the file directly (no directory switch).
func watchCodexJSONL(
	ctx context.Context,
	sess *core.Session,
	onEntry func(map[string]any),
	updater core.SessionUpdater,
	settleMS int,
) error {
	// Wait for file to exist
	for {
		if _, err := os.Stat(sess.JSONLPath); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	flushCodexLines(sess, onEntry, updater)

	// Poll with fallback — fsnotify watching a single file is less reliable
	// across platforms; polling at 200ms is responsive enough for Codex.
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if settleMS > 0 {
				time.Sleep(time.Duration(settleMS) * time.Millisecond)
			}
			flushCodexLines(sess, onEntry, updater)
		}
	}
}

func flushCodexLines(sess *core.Session, onEntry func(map[string]any), updater core.SessionUpdater) {
	if sess.JSONLPath == "" {
		return
	}
	f, err := os.Open(sess.JSONLPath)
	if err != nil {
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return
	}
	size := info.Size()
	offset := sess.JSONLOffset
	if offset > size {
		offset = 0
	}
	f.Seek(offset, 0) //nolint:errcheck

	var data []byte
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		data = append(data, buf[:n]...)
		if err != nil {
			break
		}
	}
	if len(data) == 0 {
		return
	}

	newOffset := offset + int64(len(data))
	sess.JSONLOffset = newOffset
	updater.UpdateOffset(sess.Name, newOffset) //nolint:errcheck

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		onEntry(entry)
	}
}

// ── JSONL parsing ─────────────────────────────────────────────────────────────

func codexParseEntry(entry map[string]any) []core.AgentEvent {
	if entry["type"] != "response_item" {
		return nil
	}
	payload, _ := entry["payload"].(map[string]any)
	if payload == nil {
		return nil
	}
	ptype, _ := payload["type"].(string)
	role, _ := payload["role"].(string)
	switch ptype {
	case "message":
		if role == "assistant" {
			return parseAssistantMsg(payload)
		}
	case "function_call":
		return parseFunctionCall(payload)
	}
	return nil
}

func parseAssistantMsg(payload map[string]any) []core.AgentEvent {
	var events []core.AgentEvent
	for _, item := range toSlice(payload["content"]) {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if m["type"] == "output_text" {
			text := strings.TrimSpace(fmt.Sprintf("%v", m["text"]))
			if text != "" {
				events = append(events, core.AgentEvent{Type: core.EventText, Text: text})
			}
		}
	}
	return events
}

func parseFunctionCall(payload map[string]any) []core.AgentEvent {
	name, _ := payload["name"].(string)
	rawArgs := payload["arguments"]
	var args map[string]any
	switch v := rawArgs.(type) {
	case string:
		json.Unmarshal([]byte(v), &args) //nolint:errcheck
		if args == nil {
			args = map[string]any{"raw": v}
		}
	case map[string]any:
		args = v
	}
	return []core.AgentEvent{{Type: core.EventToolUse, ToolName: name, ToolInput: args}}
}

func countCodexOptions(dialogText string) int {
	matches := codexNumOptRE.FindAllStringSubmatch(dialogText, -1)
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

// sortByModTime sorts file paths descending by mtime.
func sortByModTime(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		si, _ := os.Stat(paths[i])
		sj, _ := os.Stat(paths[j])
		if si == nil || sj == nil {
			return false
		}
		return si.ModTime().After(sj.ModTime())
	})
}
