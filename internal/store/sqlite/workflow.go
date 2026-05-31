package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

// ── WorkflowStore implementation ──────────────────────────────────────────────

func (s *Store) SaveWorkflow(wf *core.Workflow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(`
		INSERT INTO workflows (id, name, enabled, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, enabled=excluded.enabled`,
		wf.ID, wf.Name, boolToInt(wf.Enabled), wf.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return err
	}

	// Replace nodes
	if _, err := tx.Exec(`DELETE FROM workflow_nodes WHERE workflow_id = ?`, wf.ID); err != nil {
		return err
	}
	for _, n := range wf.Nodes {
		tmpl, _ := n.Template.MarshalJSON()
		_, err := tx.Exec(`
			INSERT INTO workflow_nodes (id, workflow_id, node_key, session_name, session_template, prompt)
			VALUES (?, ?, ?, ?, ?, ?)`,
			n.ID, wf.ID, n.NodeKey, n.SessionName, string(tmpl), n.Prompt,
		)
		if err != nil {
			return err
		}
	}

	// Replace edges
	if _, err := tx.Exec(`DELETE FROM workflow_edges WHERE workflow_id = ?`, wf.ID); err != nil {
		return err
	}
	for _, e := range wf.Edges {
		_, err := tx.Exec(`
			INSERT INTO workflow_edges (workflow_id, from_node, to_node) VALUES (?, ?, ?)`,
			wf.ID, e.FromNode, e.ToNode,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) GetWorkflow(id string) (*core.Workflow, error) {
	row := s.db.QueryRow(`SELECT id, name, enabled, created_at FROM workflows WHERE id = ?`, id)
	wf, err := scanWorkflow(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("workflow not found: %s", id)
	}
	if err != nil {
		return nil, err
	}
	if err := s.loadWorkflowRelations(wf); err != nil {
		return nil, err
	}
	return wf, nil
}

func (s *Store) ListWorkflows() ([]*core.Workflow, error) {
	rows, err := s.db.Query(`SELECT id, name, enabled, created_at FROM workflows`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Workflow
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		if err := s.loadWorkflowRelations(wf); err != nil {
			return nil, err
		}
		out = append(out, wf)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWorkflow(id string) error {
	_, err := s.db.Exec(`DELETE FROM workflows WHERE id = ?`, id)
	return err
}

// ── WorkflowRun state ─────────────────────────────────────────────────────────

func (s *Store) CreateWorkflowRun(run *core.WorkflowRun) error {
	_, err := s.db.Exec(`
		INSERT INTO workflow_runs (id, workflow_id, status, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		run.ID, run.WorkflowID, string(run.Status),
		run.StartedAt.Format(time.RFC3339), run.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) UpdateWorkflowRun(runID string, status core.WorkflowStatus) error {
	_, err := s.db.Exec(`UPDATE workflow_runs SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), time.Now().Format(time.RFC3339), runID)
	return err
}

func (s *Store) GetWorkflowRun(runID string) (*core.WorkflowRun, error) {
	row := s.db.QueryRow(`SELECT id, workflow_id, status, started_at, updated_at FROM workflow_runs WHERE id = ?`, runID)
	return scanWorkflowRun(row)
}

func (s *Store) ListWorkflowRunsForWorkflow(workflowID string) ([]*core.WorkflowRun, error) {
	rows, err := s.db.Query(`SELECT id, workflow_id, status, started_at, updated_at FROM workflow_runs WHERE workflow_id = ?`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.WorkflowRun
	for rows.Next() {
		run, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Store) UpsertNodeRun(nr *core.WorkflowNodeRun) error {
	_, err := s.db.Exec(`
		INSERT INTO workflow_node_runs (run_id, node_key, status, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id, node_key) DO UPDATE SET status=excluded.status, updated_at=excluded.updated_at`,
		nr.RunID, nr.NodeKey, string(nr.Status), nr.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *Store) ListNodeRuns(runID string) ([]*core.WorkflowNodeRun, error) {
	rows, err := s.db.Query(`SELECT run_id, node_key, status, updated_at FROM workflow_node_runs WHERE run_id = ?`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.WorkflowNodeRun
	for rows.Next() {
		nr, err := scanNodeRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, nr)
	}
	return out, rows.Err()
}

// ── scan helpers ──────────────────────────────────────────────────────────────

func scanWorkflow(row scanner) (*core.Workflow, error) {
	var wf core.Workflow
	var createdAt string
	var enabled int
	err := row.Scan(&wf.ID, &wf.Name, &enabled, &createdAt)
	if err != nil {
		return nil, err
	}
	wf.Enabled = enabled != 0
	wf.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &wf, nil
}

func (s *Store) loadWorkflowRelations(wf *core.Workflow) error {
	// Load nodes
	rows, err := s.db.Query(`SELECT id, workflow_id, node_key, session_name, session_template, prompt FROM workflow_nodes WHERE workflow_id = ?`, wf.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n core.WorkflowNode
		var tmplJSON string
		if err := rows.Scan(&n.ID, &n.WorkflowID, &n.NodeKey, &n.SessionName, &tmplJSON, &n.Prompt); err != nil {
			return err
		}
		n.Template.UnmarshalJSON([]byte(tmplJSON)) //nolint:errcheck
		wf.Nodes = append(wf.Nodes, n)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Load edges
	erows, err := s.db.Query(`SELECT workflow_id, from_node, to_node FROM workflow_edges WHERE workflow_id = ?`, wf.ID)
	if err != nil {
		return err
	}
	defer erows.Close()
	for erows.Next() {
		var e core.WorkflowEdge
		if err := erows.Scan(&e.WorkflowID, &e.FromNode, &e.ToNode); err != nil {
			return err
		}
		wf.Edges = append(wf.Edges, e)
	}
	return erows.Err()
}

func scanWorkflowRun(row scanner) (*core.WorkflowRun, error) {
	var run core.WorkflowRun
	var status, startedAt, updatedAt string
	err := row.Scan(&run.ID, &run.WorkflowID, &status, &startedAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	run.Status = core.WorkflowStatus(status)
	run.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
	run.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &run, nil
}

func scanNodeRun(row scanner) (*core.WorkflowNodeRun, error) {
	var nr core.WorkflowNodeRun
	var status, updatedAt string
	err := row.Scan(&nr.RunID, &nr.NodeKey, &status, &updatedAt)
	if err != nil {
		return nil, err
	}
	nr.Status = core.WorkflowStatus(status)
	nr.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &nr, nil
}
