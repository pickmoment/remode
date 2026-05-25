// Package codex drives the Codex CLI via tmux.
package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

const codexTailBytes = 16384

// iterJSONLFiles yields all Codex rollout-*.jsonl files under sessionsDir.
func iterJSONLFiles(sessionsDir string) []string {
	var out []string
	filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error { //nolint:errcheck
		if err != nil || d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// ReadSessionMeta reads the session_meta payload from the first 10 lines of a JSONL file.
func ReadSessionMeta(jsonlPath string) map[string]any {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for i := 0; i < 10 && scanner.Scan(); i++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["type"] == "session_meta" {
			if payload, ok := entry["payload"].(map[string]any); ok {
				return payload
			}
		}
	}
	return nil
}

// FindJSONLByID searches for the JSONL file whose session_meta.id matches sessionID.
func FindJSONLByID(sessionID, sessionsDir string) string {
	for _, path := range iterJSONLFiles(sessionsDir) {
		meta := ReadSessionMeta(path)
		if id, _ := meta["id"].(string); id == sessionID {
			return path
		}
	}
	return ""
}

// ListProjects returns Codex projects grouped by working directory.
func ListProjects(sessionsDir string) ([]core.Project, error) {
	files := iterJSONLFiles(sessionsDir)
	// Sort by mtime descending
	sort.Slice(files, func(i, j int) bool {
		si, _ := os.Stat(files[i])
		sj, _ := os.Stat(files[j])
		if si == nil || sj == nil {
			return false
		}
		return si.ModTime().After(sj.ModTime())
	})

	type projKey = string
	byCWD := make(map[projKey]*core.Project)
	var order []string

	for _, path := range files {
		meta := ReadSessionMeta(path)
		sessionID, _ := meta["id"].(string)
		if sessionID == "" {
			sessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		}
		cwdStr, _ := meta["cwd"].(string)
		tsStr, _ := meta["timestamp"].(string)

		createdAt := modTimeStr(path)
		if tsStr != "" {
			createdAt = tsStr
		}

		info, _ := os.Stat(path)
		lastMod := ""
		if info != nil {
			lastMod = info.ModTime().Format(time.RFC3339)
		}

		title := readCodexTitle(path)
		sess := core.ProjectSession{
			SessionID:    sessionID,
			CreatedAt:    createdAt,
			LastModified: lastMod,
			Title:        title,
		}

		key := cwdStr
		if key == "" {
			key = filepath.Base(path)
		}
		if _, ok := byCWD[key]; !ok {
			order = append(order, key)
			byCWD[key] = &core.Project{
				CWD:         cwdStr,
				DisplayPath: cwdStr,
			}
			if cwdStr == "" {
				byCWD[key].DisplayPath = "(unknown)"
			}
		}
		byCWD[key].Sessions = append(byCWD[key].Sessions, sess)
	}

	projects := make([]core.Project, 0, len(byCWD))
	for _, key := range order {
		projects = append(projects, *byCWD[key])
	}
	return projects, nil
}

func readCodexTitle(jsonlPath string) string {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := info.Size() - codexTailBytes
	if start < 0 {
		start = 0
	}
	f.Seek(start, io.SeekStart) //nolint:errcheck
	data, _ := io.ReadAll(f)

	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["type"] != "response_item" {
			continue
		}
		p, _ := entry["payload"].(map[string]any)
		if p == nil {
			continue
		}
		if p["type"] == "message" && p["role"] == "assistant" && p["phase"] == "final" {
			for _, item := range toSlice(p["content"]) {
				m, _ := item.(map[string]any)
				if m == nil {
					continue
				}
				if m["type"] == "output_text" {
					text := strings.TrimSpace(fmt.Sprintf("%v", m["text"]))
					if text != "" {
						if len(text) > 80 {
							return text[:80]
						}
						return text
					}
				}
			}
		}
	}
	return ""
}

func toSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func modTimeStr(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return info.ModTime().Format(time.RFC3339)
}
