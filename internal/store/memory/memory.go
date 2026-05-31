// Package memory provides an in-memory SessionStore for tests.
package memory

import (
	"fmt"
	"sync"

	"github.com/pickmoment/remode/internal/core"
)

// Store is a thread-safe in-memory implementation of store.SessionStore.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*core.Session
}

func New() *Store {
	return &Store{sessions: make(map[string]*core.Session)}
}

func (s *Store) Save(sess *core.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sess
	s.sessions[sess.Name] = &cp
	return nil
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, name)
	return nil
}

func (s *Store) UpdateOffset(name string, offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[name]
	if !ok {
		return fmt.Errorf("session not found: %s", name)
	}
	sess.JSONLOffset = offset
	return nil
}

func (s *Store) UpdateMessageLevel(name string, level string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[name]
	if !ok {
		return fmt.Errorf("session not found: %s", name)
	}
	sess.Level = core.MessageLevel(level)
	return nil
}

func (s *Store) UpdateChatID(name string, chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[name]
	if !ok {
		return fmt.Errorf("session not found: %s", name)
	}
	sess.ChatID = chatID
	return nil
}

func (s *Store) List() ([]*core.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*core.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		cp := *sess
		out = append(out, &cp)
	}
	return out, nil
}

func (s *Store) Get(name string) (*core.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[name]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", name)
	}
	cp := *sess
	return &cp, nil
}

// BackfillTransport sets transport to defaultTransport for all entries with an empty transport.
func (s *Store) BackfillTransport(defaultTransport string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.Transport == "" {
			sess.Transport = defaultTransport
		}
	}
	return nil
}

// Compile-time check: Store satisfies core.SessionUpdater.
var _ interface {
	UpdateOffset(string, int64) error
	Save(*core.Session) error
} = (*Store)(nil)
