package claudecode

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/pickmoment/remode/internal/core"
)

const fallbackInterval = 10 * time.Second

// Watch tails sess.JSONLPath, calling onEntry for each new JSONL record.
// It also detects new .jsonl files appearing in the same directory and
// transparently switches to the newest one (updating sess in place).
// Blocks until ctx is cancelled.
func Watch(
	ctx context.Context,
	sess *core.Session,
	onEntry func(map[string]any),
	updater core.SessionUpdater,
	settleMS int,
) error {
	dirPath := filepath.Dir(sess.JSONLPath)

	// Wait until the directory exists
	for {
		if _, err := os.Stat(dirPath); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	// Initial flush
	flushNewLines(sess, onEntry, updater)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	if err := w.Add(dirPath); err != nil {
		return err
	}

	fallback := time.NewTicker(fallbackInterval)
	defer fallback.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-fallback.C:
			flushNewLines(sess, onEntry, updater)

		case event, ok := <-w.Events:
			if !ok {
				return nil
			}
			path := filepath.Clean(event.Name)
			if !strings.HasSuffix(path, ".jsonl") {
				continue
			}
			if path != filepath.Clean(sess.JSONLPath) {
				// New JSONL file detected — switch to it
				info, err := os.Stat(path)
				if err != nil || info.Size() == 0 {
					continue
				}
				// Only switch if newer
				curInfo, err := os.Stat(sess.JSONLPath)
				if err == nil && info.ModTime().Before(curInfo.ModTime()) {
					continue
				}
				sess.SessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
				sess.JSONLPath = path
				sess.JSONLOffset = 0
				if err := updater.Save(sess); err != nil {
					log.Printf("watcher: save after JSONL switch: %v", err)
				}
				log.Printf("watcher: switched to new JSONL %s", sess.SessionID)
				// Watch new directory if it changed
				w.Add(filepath.Dir(path)) //nolint:errcheck
			} else {
				// Current JSONL changed
				if settleMS > 0 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(time.Duration(settleMS) * time.Millisecond):
					}
				}
				flushNewLines(sess, onEntry, updater)
			}

		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Printf("watcher fsnotify error: %v", err)
		}
	}
}

// flushNewLines reads all unread bytes from sess.JSONLPath, parses each line
// as JSON, filters suppressed types, and calls onEntry. Updates sess.JSONLOffset.
func flushNewLines(sess *core.Session, onEntry func(map[string]any), updater core.SessionUpdater) {
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
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return
	}

	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return
	}

	newOffset := offset + int64(len(data))
	sess.JSONLOffset = newOffset
	if err := updater.UpdateOffset(sess.Name, newOffset); err != nil {
		log.Printf("watcher: update offset: %v", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entryType, _ := entry["type"].(string)
		if entryType == "summary" || entryType == "queue-operation" {
			continue
		}
		onEntry(entry)
	}
}
