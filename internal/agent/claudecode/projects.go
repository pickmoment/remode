package claudecode

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

const tailBytes = 16384

// EncodePath converts an absolute path to the Claude project directory name.
// Claude uses the path with "/" replaced by "-".
func EncodePath(cwd string) string {
	return strings.ReplaceAll(cwd, "/", "-")
}

// ProjectDirFor returns the Claude project directory for a given working directory.
func ProjectDirFor(cwd, projectsDir string) string {
	return filepath.Join(projectsDir, EncodePath(cwd))
}

// ListProjects scans projectsDir and returns all projects with their sessions.
func ListProjects(projectsDir string) ([]core.Project, error) {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var projects []core.Project
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, entry.Name())
		sessions := readProjectSessions(projDir)
		if len(sessions) == 0 {
			continue
		}
		cwd := readCWD(projDir)
		displayPath := cwd
		if displayPath == "" {
			displayPath = entry.Name()
		}
		projects = append(projects, core.Project{
			CWD:         cwd,
			DisplayPath: displayPath,
			Sessions:    sessions,
		})
	}

	// Sort by most recently modified session
	sort.Slice(projects, func(i, j int) bool {
		li := projects[i].Sessions[0].LastModified
		lj := projects[j].Sessions[0].LastModified
		return li > lj
	})
	return projects, nil
}

func readProjectSessions(projDir string) []core.ProjectSession {
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return nil
	}

	type fileInfo struct {
		name    string
		modTime time.Time
	}
	var jsonls []fileInfo
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			info, err := e.Info()
			if err != nil {
				continue
			}
			jsonls = append(jsonls, fileInfo{e.Name(), info.ModTime()})
		}
	}
	if len(jsonls) == 0 {
		return nil
	}
	sort.Slice(jsonls, func(i, j int) bool {
		return jsonls[i].modTime.After(jsonls[j].modTime)
	})

	var sessions []core.ProjectSession
	for _, jf := range jsonls {
		path := filepath.Join(projDir, jf.name)
		sessionID := strings.TrimSuffix(jf.name, ".jsonl")
		createdAt := readCreatedAt(path)
		title := readTitle(path)
		sessions = append(sessions, core.ProjectSession{
			SessionID:    sessionID,
			CreatedAt:    createdAt,
			LastModified: jf.modTime.Format(time.RFC3339),
			Title:        title,
		})
	}

	// Sort by created_at descending
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt > sessions[j].CreatedAt
	})
	return sessions
}

func readCreatedAt(jsonlPath string) string {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return modTimeOf(jsonlPath)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for i := 0; i < 20 && scanner.Scan(); i++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if ts, ok := entry["timestamp"].(string); ok && ts != "" {
			return ts
		}
	}
	return modTimeOf(jsonlPath)
}

func readTitle(jsonlPath string) string {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	// Seek to tail
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := info.Size() - tailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}

	// Read and scan from the end backward
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
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
		if entry["type"] == "custom-title" {
			if title, ok := entry["customTitle"].(string); ok {
				return title
			}
		}
	}
	return ""
}

func readCWD(projDir string) string {
	entries, _ := os.ReadDir(projDir)
	var jsonls []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			jsonls = append(jsonls, filepath.Join(projDir, e.Name()))
		}
	}
	// Check up to 3 most recently modified
	sort.Slice(jsonls, func(i, j int) bool {
		si, _ := os.Stat(jsonls[i])
		sj, _ := os.Stat(jsonls[j])
		if si == nil || sj == nil {
			return false
		}
		return si.ModTime().After(sj.ModTime())
	})
	if len(jsonls) > 3 {
		jsonls = jsonls[:3]
	}
	for _, path := range jsonls {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var entry map[string]any
			if json.Unmarshal([]byte(line), &entry) != nil {
				continue
			}
			if entry["type"] == "user" {
				if cwd, ok := entry["cwd"].(string); ok {
					f.Close()
					return cwd
				}
			}
		}
		f.Close()
	}
	return ""
}

func modTimeOf(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return info.ModTime().Format(time.RFC3339)
}
