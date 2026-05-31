package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

func (s *Store) SaveSchedule(sc *core.Schedule) error {
	tmpl, _ := json.Marshal(sc.Template)
	_, err := s.db.Exec(`
		INSERT INTO schedules
		  (id, name, cron_spec, action_type, target_session_name, session_template,
		   payload, initial_prompt, deadline_secs, enabled, last_run, next_run, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  name=excluded.name,
		  cron_spec=excluded.cron_spec,
		  action_type=excluded.action_type,
		  target_session_name=excluded.target_session_name,
		  session_template=excluded.session_template,
		  payload=excluded.payload,
		  initial_prompt=excluded.initial_prompt,
		  deadline_secs=excluded.deadline_secs,
		  enabled=excluded.enabled,
		  last_run=excluded.last_run,
		  next_run=excluded.next_run`,
		sc.ID, sc.Name, sc.CronSpec, string(sc.Action), sc.TargetSession, string(tmpl),
		sc.Payload, sc.InitialPrompt, sc.DeadlineSecs, boolToInt(sc.Enabled),
		timeToStr(sc.LastRun), timeToStr(sc.NextRun), sc.CreatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) DeleteSchedule(id string) error {
	_, err := s.db.Exec(`DELETE FROM schedules WHERE id = ?`, id)
	return err
}

func (s *Store) ListSchedules() ([]*core.Schedule, error) {
	rows, err := s.db.Query(`SELECT id, name, cron_spec, action_type, target_session_name,
		session_template, payload, initial_prompt, deadline_secs, enabled,
		last_run, next_run, created_at FROM schedules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) GetSchedule(id string) (*core.Schedule, error) {
	row := s.db.QueryRow(`SELECT id, name, cron_spec, action_type, target_session_name,
		session_template, payload, initial_prompt, deadline_secs, enabled,
		last_run, next_run, created_at FROM schedules WHERE id = ?`, id)
	sc, err := scanSchedule(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("schedule not found: %s", id)
	}
	return sc, err
}

func (s *Store) UpdateScheduleRun(id string, last, next time.Time) error {
	_, err := s.db.Exec(`UPDATE schedules SET last_run = ?, next_run = ? WHERE id = ?`,
		timeToStr(last), timeToStr(next), id)
	return err
}

func scanSchedule(row scanner) (*core.Schedule, error) {
	var sc core.Schedule
	var actionType, tmplJSON, lastRun, nextRun, createdAt string
	var enabled int
	err := row.Scan(
		&sc.ID, &sc.Name, &sc.CronSpec, &actionType, &sc.TargetSession,
		&tmplJSON, &sc.Payload, &sc.InitialPrompt, &sc.DeadlineSecs, &enabled,
		&lastRun, &nextRun, &createdAt,
	)
	if err != nil {
		return nil, err
	}
	sc.Action = core.ScheduleAction(actionType)
	sc.Enabled = enabled != 0
	json.Unmarshal([]byte(tmplJSON), &sc.Template) //nolint:errcheck
	sc.LastRun = strToTime(lastRun)
	sc.NextRun = strToTime(nextRun)
	sc.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	return &sc, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func timeToStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func strToTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
