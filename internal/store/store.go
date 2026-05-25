package store

import "github.com/pickmoment/remode/internal/core"

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
}
