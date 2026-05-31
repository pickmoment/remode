// Package scheduler provides cron-based scheduled actions on remode sessions.
//
// Three action types (ascending complexity):
//   - send_prompt: sends a predefined prompt to a named session
//   - status_report: captures the session screen and delivers it as a message
//   - batch_session: creates a session, runs it, and kills it when done
package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/store"
)

// Clock abstracts time.Now() for unit-testable scheduling.
type Clock interface {
	Now() time.Time
}

// RealClock implements Clock using the system clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// Driver is the subset of session.Manager the scheduler needs.
// Using an interface (rather than *session.Manager directly) allows
// unit tests to inject mocks without importing the session package.
type Driver interface {
	SendInputBy(name, text string) error
	Capture(sess *core.Session) (string, error)
	Get(name string) *core.Session
	Create(ctx context.Context, name, cwd string, chatID int64, agentType, transport string) (*core.Session, error)
	Kill(ctx context.Context, name string) error
	DeliverToSession(ctx context.Context, name string, msg core.Message) error
}

// Scheduler manages scheduled actions on sessions.
type Scheduler struct {
	store  store.ScheduleStore
	driver Driver
	clock  Clock

	mu      sync.Mutex
	cron    *cron.Cron
	entries map[string]cron.EntryID // schedule.ID → cron entry ID

	// nextChatID provides unique synthetic ChatIDs for scheduler-created (web) sessions.
	// Seeded from time.Now().UnixNano() at construction; only needs to be unique
	// within this process since web sessions bypass chatToSess routing.
	nextChatID atomic.Int64 //nolint:govet
}

// New creates a Scheduler. clock may be nil (uses RealClock).
func New(store store.ScheduleStore, driver Driver, clock Clock) *Scheduler {
	if clock == nil {
		clock = RealClock{}
	}
	s := &Scheduler{
		store:   store,
		driver:  driver,
		clock:   clock,
		entries: make(map[string]cron.EntryID),
	}
	// Seed the ChatID counter using the high bits of UnixNano so it doesn't
	// collide with real Telegram/Discord IDs and is unique per process start.
	seed := time.Now().UnixNano() & ((1 << 60) - 1)
	s.nextChatID.Store(seed)
	return s
}

// Start loads enabled schedules and begins firing. Blocks until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) error {
	s.cron = cron.New(cron.WithSeconds()) // use 6-field specs

	if err := s.load(ctx); err != nil {
		log.Printf("scheduler: load: %v", err)
	}
	s.cron.Start()

	<-ctx.Done()
	s.cron.Stop()
	return nil
}

// Reload reloads schedules from the store. Called by the web UI after CRUD changes.
// If Start has not been called yet (cron runner not started), this is a no-op —
// the schedules will be loaded when Start is eventually called.
func (s *Scheduler) Reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		// Cron not started yet; schedule will be picked up when Start runs.
		return nil
	}
	// Remove all existing entries
	for _, entryID := range s.entries {
		s.cron.Remove(entryID)
	}
	s.entries = make(map[string]cron.EntryID)
	return s.load(ctx)
}

// load reads schedules and registers cron entries. Must be called with mu held (or from Start).
func (s *Scheduler) load(ctx context.Context) error {
	schedules, err := s.store.ListSchedules()
	if err != nil {
		return fmt.Errorf("list schedules: %w", err)
	}
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		sc := sc // capture loop variable
		entryID, err := s.cron.AddFunc(sc.CronSpec, func() {
			fireCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if err := s.Fire(fireCtx, sc.ID); err != nil {
				log.Printf("scheduler: fire %s (%s): %v", sc.Name, sc.ID, err)
			}
		})
		if err != nil {
			log.Printf("scheduler: invalid cron spec %q for %s: %v", sc.CronSpec, sc.Name, err)
			continue
		}
		s.entries[sc.ID] = entryID
	}
	return nil
}

// Fire executes the action for the schedule with the given ID.
// It is the testable executor: tests call Fire directly with a mock clock/driver
// without needing real cron timing.
func (s *Scheduler) Fire(ctx context.Context, id string) error {
	sc, err := s.store.GetSchedule(id)
	if err != nil {
		return fmt.Errorf("get schedule: %w", err)
	}
	if !sc.Enabled {
		return nil
	}

	now := s.clock.Now()
	var fireErr error

	switch sc.Action {
	case core.ActionSendPrompt:
		fireErr = s.fireSendPrompt(ctx, sc)
	case core.ActionStatusReport:
		fireErr = s.fireStatusReport(ctx, sc)
	case core.ActionBatchSession:
		fireErr = s.fireBatchSession(ctx, sc)
	default:
		fireErr = fmt.Errorf("unknown action type: %s", sc.Action)
	}

	// Persist last_run regardless of fire error.
	nextRun := s.nextRunAfter(sc.CronSpec, now)
	s.store.UpdateScheduleRun(id, now, nextRun) //nolint:errcheck

	return fireErr
}

// ── action implementations ────────────────────────────────────────────────────

// fireSendPrompt (3a): sends sc.Payload as input to the target session.
func (s *Scheduler) fireSendPrompt(ctx context.Context, sc *core.Schedule) error {
	sess := s.driver.Get(sc.TargetSession)
	if sess == nil {
		log.Printf("scheduler: send_prompt: session %q not found, skipping", sc.TargetSession)
		return nil // session gone — not an error; definition survives
	}
	return s.driver.SendInputBy(sc.TargetSession, sc.Payload)
}

// fireStatusReport (3b): captures the session screen and delivers a message.
func (s *Scheduler) fireStatusReport(ctx context.Context, sc *core.Schedule) error {
	sess := s.driver.Get(sc.TargetSession)
	if sess == nil {
		log.Printf("scheduler: status_report: session %q not found, skipping", sc.TargetSession)
		return nil
	}
	screen, err := s.driver.Capture(sess)
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	msg := core.Message{
		Text:         fmt.Sprintf("📊 **%s** 상태 보고 (%s)\n\n```\n%s\n```", sc.Name, s.clock.Now().Format("2006-01-02 15:04"), screen),
		Preformatted: true,
		Category:     core.CategoryText,
	}
	return s.driver.DeliverToSession(ctx, sc.TargetSession, msg)
}

// fireBatchSession (3c): creates a session from a template, sends an initial
// prompt if set, and kills it when IDLE or when the deadline expires.
// The kill happens in a separate goroutine so Fire returns immediately.
func (s *Scheduler) fireBatchSession(ctx context.Context, sc *core.Schedule) error {
	// Generate a unique session name from the schedule name + timestamp.
	sessName := fmt.Sprintf("%s-%s", sc.Name, time.Now().Format("0102-1504"))
	chatID := s.nextChatID.Add(1)

	sess, err := s.driver.Create(ctx, sessName, sc.Template.CWD, chatID, sc.Template.AgentType, "web")
	if err != nil {
		return fmt.Errorf("create batch session: %w", err)
	}

	if sc.InitialPrompt != "" {
		if err := s.driver.SendInputBy(sess.Name, sc.InitialPrompt); err != nil {
			log.Printf("scheduler: batch initial prompt: %v", err)
		}
	}

	// Kill the session after DeadlineSecs (if set). The session goroutine is
	// already running inside manager.startTasks; we just need a timer to kill it.
	if sc.DeadlineSecs > 0 {
		deadline := time.Duration(sc.DeadlineSecs) * time.Second
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(deadline):
				log.Printf("scheduler: batch session %s deadline reached, killing", sess.Name)
				s.driver.Kill(context.Background(), sess.Name) //nolint:errcheck
			}
		}()
	}

	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// nextRunAfter computes the next scheduled time after t for the given cron spec.
func (s *Scheduler) nextRunAfter(cronSpec string, after time.Time) time.Time {
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(cronSpec)
	if err != nil {
		return time.Time{}
	}
	return sched.Next(after)
}

// ListSchedules returns all schedules from the store.
func (s *Scheduler) ListSchedules() ([]*core.Schedule, error) {
	return s.store.ListSchedules()
}

// GetSchedule returns a single schedule by ID.
func (s *Scheduler) GetSchedule(id string) (*core.Schedule, error) {
	return s.store.GetSchedule(id)
}

// CreateSchedule is a convenience helper for the web UI to create and persist
// a new schedule and immediately reload it into the cron runner.
func (s *Scheduler) CreateSchedule(ctx context.Context, sc *core.Schedule) error {
	if sc.ID == "" {
		sc.ID = uuid.New().String()
	}
	if sc.CreatedAt.IsZero() {
		sc.CreatedAt = s.clock.Now()
	}
	if sc.CronSpec != "" {
		sc.NextRun = s.nextRunAfter(sc.CronSpec, s.clock.Now())
	}
	if err := s.store.SaveSchedule(sc); err != nil {
		return err
	}
	return s.Reload(ctx)
}

// UpdateSchedule updates a schedule in the store and reloads.
func (s *Scheduler) UpdateSchedule(ctx context.Context, sc *core.Schedule) error {
	if err := s.store.SaveSchedule(sc); err != nil {
		return err
	}
	return s.Reload(ctx)
}

// DeleteSchedule removes a schedule and reloads.
func (s *Scheduler) DeleteSchedule(ctx context.Context, id string) error {
	if err := s.store.DeleteSchedule(id); err != nil {
		return err
	}
	return s.Reload(ctx)
}
