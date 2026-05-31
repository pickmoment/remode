package core

import "time"

// WorkflowStatus represents the state of a workflow run or node run.
type WorkflowStatus string

const (
	StatusPending  WorkflowStatus = "pending"
	StatusRunning  WorkflowStatus = "running"
	StatusDone     WorkflowStatus = "done"
	StatusFailed   WorkflowStatus = "failed"
	StatusWaiting  WorkflowStatus = "waiting" // node waiting for dependencies
	StatusSkipped  WorkflowStatus = "skipped"
)

// Workflow is a named DAG of session nodes connected by dependency edges.
type Workflow struct {
	ID        string
	Name      string
	Enabled   bool
	Nodes     []WorkflowNode
	Edges     []WorkflowEdge
	CreatedAt time.Time
}

// WorkflowNode is a step in a workflow. It either references an existing named
// session or creates a new one from a SessionTemplate.
type WorkflowNode struct {
	ID          string
	WorkflowID  string
	NodeKey     string // stable key within the workflow (used by edges)
	SessionName string // existing session name (may be empty if template is set)
	Template    SessionTemplate // used when SessionName is empty
	Prompt      string          // input sent to the session when this node runs
}

// WorkflowEdge declares that the destination node depends on the source node.
type WorkflowEdge struct {
	WorkflowID string
	FromNode   string // node_key
	ToNode     string // node_key
}

// WorkflowRun is a single execution of a Workflow.
type WorkflowRun struct {
	ID         string
	WorkflowID string
	Status     WorkflowStatus
	StartedAt  time.Time
	UpdatedAt  time.Time
}

// WorkflowNodeRun is the state of one node within a WorkflowRun.
type WorkflowNodeRun struct {
	RunID     string
	NodeKey   string
	Status    WorkflowStatus
	UpdatedAt time.Time
}
