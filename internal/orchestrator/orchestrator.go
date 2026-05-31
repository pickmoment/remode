package orchestrator

import (
	"context"
	"fmt"
	"sync"

	"github.com/pickmoment/remode/internal/core"
)

// OrcDriver is the subset of session.Manager needed by the orchestrator.
type OrcDriver interface {
	SendInputBy(name, text string) error
	Capture(sess *core.Session) (string, error)
	Get(name string) *core.Session
	Create(ctx context.Context, name, cwd string, chatID int64, agentType, transport string) (*core.Session, error)
	Kill(ctx context.Context, name string) error
}

// BroadcastResult holds the outcome of a Broadcast call for one session.
type BroadcastResult struct {
	Name string
	Err  error
}

// ChainConfig defines a one-shot chain between two sessions.
type ChainConfig struct {
	// FromSession is the session whose IDLE state triggers the chain.
	FromSession string
	// ToSession is the session that receives the rendered prompt.
	ToSession string
	// PromptTemplate may contain {{output}} which is replaced with the captured output.
	PromptTemplate string
}

// Orchestrator coordinates multi-session operations.
type Orchestrator struct {
	driver  OrcDriver
	tracker *TurnTracker
}

// New creates an Orchestrator.
func New(driver OrcDriver, tracker *TurnTracker) *Orchestrator {
	return &Orchestrator{driver: driver, tracker: tracker}
}

// ── 4a Broadcast ──────────────────────────────────────────────────────────────

// Broadcast sends prompt to all named sessions concurrently.
// If collectResults is true, it waits for each session to become IDLE and
// returns a Capture snapshot for each.
func (o *Orchestrator) Broadcast(ctx context.Context, names []string, prompt string, collectResults bool) []BroadcastResult {
	results := make([]BroadcastResult, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		i, name := i, name
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := o.driver.SendInputBy(name, prompt); err != nil {
				results[i] = BroadcastResult{Name: name, Err: err}
				return
			}
			if collectResults {
				if err := o.tracker.WaitIdle(ctx, name); err != nil {
					results[i] = BroadcastResult{Name: name, Err: fmt.Errorf("wait idle: %w", err)}
					return
				}
				sess := o.driver.Get(name)
				if sess != nil {
					screen, err := o.driver.Capture(sess)
					results[i] = BroadcastResult{Name: name, Err: err}
					_ = screen // callers access via Capture directly if needed
				} else {
					results[i] = BroadcastResult{Name: name, Err: fmt.Errorf("session not found after idle")}
				}
				return
			}
			results[i] = BroadcastResult{Name: name}
		}()
	}
	wg.Wait()
	return results
}

// ── 4b Chain ──────────────────────────────────────────────────────────────────

// RunChain waits for cfg.FromSession to become IDLE, captures its output,
// renders cfg.PromptTemplate (replacing {{output}} with the capture), and
// sends the result to cfg.ToSession.
// It refuses to fire if FromSession is BLOCKED.
func (o *Orchestrator) RunChain(ctx context.Context, cfg ChainConfig) error {
	// Wait for the source session to become idle.
	if err := o.tracker.WaitIdle(ctx, cfg.FromSession); err != nil {
		return fmt.Errorf("wait idle for %s: %w", cfg.FromSession, err)
	}

	// Double-check: if it came out of WaitIdle but is somehow BLOCKED, abort.
	if o.tracker.State(cfg.FromSession) == TurnBlocked {
		return fmt.Errorf("session %s is BLOCKED, chain aborted", cfg.FromSession)
	}

	// Capture the output.
	sessFrom := o.driver.Get(cfg.FromSession)
	var output string
	if sessFrom != nil {
		screen, err := o.driver.Capture(sessFrom)
		if err != nil {
			return fmt.Errorf("capture %s: %w", cfg.FromSession, err)
		}
		output = screen
	}

	// Render the prompt template.
	prompt := renderTemplate(cfg.PromptTemplate, output)

	// Send to the destination session.
	return o.driver.SendInputBy(cfg.ToSession, prompt)
}

// renderTemplate replaces {{output}} in template with the captured output.
func renderTemplate(template, output string) string {
	if template == "" {
		return output
	}
	result := template
	const placeholder = "{{output}}"
	for i := 0; i < len(result); {
		idx := indexOf(result[i:], placeholder)
		if idx < 0 {
			break
		}
		pos := i + idx
		result = result[:pos] + output + result[pos+len(placeholder):]
		i = pos + len(output)
	}
	return result
}

func indexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
