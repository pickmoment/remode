// Package sqlite provides a SQLite-backed SessionStore.
package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
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
	agent_type        TEXT NOT NULL DEFAULT 'claude_code',
	transport         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS schedules (
	id                   TEXT PRIMARY KEY,
	name                 TEXT NOT NULL,
	cron_spec            TEXT NOT NULL,
	action_type          TEXT NOT NULL,
	target_session_name  TEXT NOT NULL DEFAULT '',
	session_template     TEXT NOT NULL DEFAULT '{}',
	payload              TEXT NOT NULL DEFAULT '',
	initial_prompt       TEXT NOT NULL DEFAULT '',
	deadline_secs        INTEGER NOT NULL DEFAULT 0,
	enabled              INTEGER NOT NULL DEFAULT 1,
	last_run             TEXT NOT NULL DEFAULT '',
	next_run             TEXT NOT NULL DEFAULT '',
	created_at           TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workflows (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	enabled    INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_nodes (
	id               TEXT PRIMARY KEY,
	workflow_id      TEXT NOT NULL,
	node_key         TEXT NOT NULL,
	session_name     TEXT NOT NULL DEFAULT '',
	session_template TEXT NOT NULL DEFAULT '{}',
	prompt           TEXT NOT NULL DEFAULT '',
	UNIQUE(workflow_id, node_key)
);

CREATE TABLE IF NOT EXISTS workflow_edges (
	workflow_id TEXT NOT NULL,
	from_node   TEXT NOT NULL,
	to_node     TEXT NOT NULL,
	PRIMARY KEY (workflow_id, from_node, to_node)
);

CREATE TABLE IF NOT EXISTS workflow_runs (
	id          TEXT PRIMARY KEY,
	workflow_id TEXT NOT NULL,
	status      TEXT NOT NULL,
	started_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_node_runs (
	run_id     TEXT NOT NULL,
	node_key   TEXT NOT NULL,
	status     TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (run_id, node_key)
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
	// Idempotent migration: add transport column to pre-existing databases.
	// CREATE TABLE IF NOT EXISTS does not add new columns to existing tables.
	if err := addColumnIfMissing(db, "sessions", "transport", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return nil, fmt.Errorf("migrate transport column: %w", err)
	}
	return &Store{db: db}, nil
}

// addColumnIfMissing adds a column to a table only if it doesn't already exist.
func addColumnIfMissing(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil // already exists
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	// Tolerate "duplicate column" in case of a race (shouldn't happen with single writer).
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Save(sess *core.Session) error {
	_, err := s.db.Exec(`
		INSERT INTO sessions (name, tmux_name, session_id, cwd, jsonl_path, chat_id,
		                      created_at, jsonl_offset, message_level, agent_type, transport)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
		  tmux_name=excluded.tmux_name,
		  session_id=excluded.session_id,
		  cwd=excluded.cwd,
		  jsonl_path=excluded.jsonl_path,
		  chat_id=excluded.chat_id,
		  created_at=excluded.created_at,
		  jsonl_offset=excluded.jsonl_offset,
		  message_level=excluded.message_level,
		  agent_type=excluded.agent_type,
		  transport=excluded.transport`,
		sess.Name, sess.TmuxName, sess.SessionID, sess.CWD, sess.JSONLPath,
		sess.ChatID, sess.CreatedAt.Format(time.RFC3339), sess.JSONLOffset,
		string(sess.Level), sess.AgentType, sess.Transport,
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

// BackfillTransport sets transport to defaultTransport for all rows with an empty transport.
// Idempotent — only touches rows created before transport tracking was added.
func (s *Store) BackfillTransport(defaultTransport string) error {
	_, err := s.db.Exec(`UPDATE sessions SET transport = ? WHERE transport = ''`, defaultTransport)
	return err
}

func (s *Store) List() ([]*core.Session, error) {
	rows, err := s.db.Query(`SELECT name, tmux_name, session_id, cwd, jsonl_path, chat_id,
	                                created_at, jsonl_offset, message_level, agent_type, transport
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
	                             created_at, jsonl_offset, message_level, agent_type, transport
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
		&sess.ChatID, &createdAt, &sess.JSONLOffset, &level, &sess.AgentType, &sess.Transport,
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
