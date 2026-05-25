// Package sqlite provides a SQLite-backed SessionStore.
package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pickmoment/remode/internal/core"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	name              TEXT PRIMARY KEY,
	tmux_name         TEXT NOT NULL,
	session_id        TEXT NOT NULL DEFAULT '',
	cwd               TEXT NOT NULL,
	jsonl_path        TEXT NOT NULL DEFAULT '',
	chat_id           INTEGER NOT NULL,
	created_at        TEXT NOT NULL,
	jsonl_offset      INTEGER NOT NULL DEFAULT 0,
	message_level     TEXT NOT NULL DEFAULT 'all',
	agent_type        TEXT NOT NULL DEFAULT 'claude_code'
);
`

// Store is a SQLite-backed implementation of store.SessionStore.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Save(sess *core.Session) error {
	_, err := s.db.Exec(`
		INSERT INTO sessions (name, tmux_name, session_id, cwd, jsonl_path, chat_id,
		                      created_at, jsonl_offset, message_level, agent_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
		  tmux_name=excluded.tmux_name,
		  session_id=excluded.session_id,
		  cwd=excluded.cwd,
		  jsonl_path=excluded.jsonl_path,
		  chat_id=excluded.chat_id,
		  created_at=excluded.created_at,
		  jsonl_offset=excluded.jsonl_offset,
		  message_level=excluded.message_level,
		  agent_type=excluded.agent_type`,
		sess.Name, sess.TmuxName, sess.SessionID, sess.CWD, sess.JSONLPath,
		sess.ChatID, sess.CreatedAt.Format(time.RFC3339), sess.JSONLOffset,
		string(sess.Level), sess.AgentType,
	)
	return err
}

func (s *Store) Delete(name string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE name = ?`, name)
	return err
}

func (s *Store) UpdateOffset(name string, offset int64) error {
	_, err := s.db.Exec(`UPDATE sessions SET jsonl_offset = ? WHERE name = ?`, offset, name)
	return err
}

func (s *Store) UpdateMessageLevel(name string, level string) error {
	_, err := s.db.Exec(`UPDATE sessions SET message_level = ? WHERE name = ?`, level, name)
	return err
}

func (s *Store) UpdateChatID(name string, chatID int64) error {
	_, err := s.db.Exec(`UPDATE sessions SET chat_id = ? WHERE name = ?`, chatID, name)
	return err
}

func (s *Store) List() ([]*core.Session, error) {
	rows, err := s.db.Query(`SELECT name, tmux_name, session_id, cwd, jsonl_path, chat_id,
	                                created_at, jsonl_offset, message_level, agent_type
	                         FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) Get(name string) (*core.Session, error) {
	row := s.db.QueryRow(`SELECT name, tmux_name, session_id, cwd, jsonl_path, chat_id,
	                             created_at, jsonl_offset, message_level, agent_type
	                      FROM sessions WHERE name = ?`, name)
	sess, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", name)
	}
	return sess, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSession(row scanner) (*core.Session, error) {
	var sess core.Session
	var createdAt, level string
	err := row.Scan(
		&sess.Name, &sess.TmuxName, &sess.SessionID, &sess.CWD, &sess.JSONLPath,
		&sess.ChatID, &createdAt, &sess.JSONLOffset, &level, &sess.AgentType,
	)
	if err != nil {
		return nil, err
	}
	sess.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	sess.Level = core.MessageLevel(level)
	return &sess, nil
}

// Compile-time check: Store satisfies core.SessionUpdater.
var _ interface {
	UpdateOffset(string, int64) error
	Save(*core.Session) error
} = (*Store)(nil)
