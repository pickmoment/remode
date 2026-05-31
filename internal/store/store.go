package store

import (
	"time"

	"github.com/pickmoment/remode/internal/core"
)

// SessionStore persists Session records across bot restarts.
// It also satisfies core.SessionUpdater via duck typing.
type SessionStore interface {
	Save(sess *core.Session) error
	Delete(name string) error
	UpdateOffset(name string, offset int64) error
	UpdateMessageLevel(name string, level string) error
	UpdateChatID(name string, chatID int64) error
	List() ([]*core.Session, error)
	Get(name string) (*core.Session, error)
	// BackfillTransport sets transport to defaultTransport for all rows where
	// transport is empty. Called once at startup to migrate pre-transport rows.
	BackfillTransport(defaultTransport string) error
}

// ScheduleStore persists Schedule definitions.
type ScheduleStore interface {
	SaveSchedule(s *core.Schedule) error
	DeleteSchedule(id string) error
	ListSchedules() ([]*core.Schedule, error)
	GetSchedule(id string) (*core.Schedule, error)
	UpdateScheduleRun(id string, last, next time.Time) error
}
