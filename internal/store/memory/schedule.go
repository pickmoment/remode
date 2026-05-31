package memory

import (
	"fmt"
	"sync"
	"time"

	"github.com/pickmoment/remode/internal/core"
)

// ScheduleStore extends the in-memory Store with schedule persistence.
// Kept as a separate type so the memory.Store struct stays focused on sessions.
type ScheduleStore struct {
	mu        sync.RWMutex
	schedules map[string]*core.Schedule
}

// NewScheduleStore creates an in-memory ScheduleStore for tests.
func NewScheduleStore() *ScheduleStore {
	return &ScheduleStore{schedules: make(map[string]*core.Schedule)}
}

func (s *ScheduleStore) SaveSchedule(sc *core.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sc
	s.schedules[sc.ID] = &cp
	return nil
}

func (s *ScheduleStore) DeleteSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.schedules, id)
	return nil
}

func (s *ScheduleStore) ListSchedules() ([]*core.Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*core.Schedule, 0, len(s.schedules))
	for _, sc := range s.schedules {
		cp := *sc
		out = append(out, &cp)
	}
	return out, nil
}

func (s *ScheduleStore) GetSchedule(id string) (*core.Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, ok := s.schedules[id]
	if !ok {
		return nil, fmt.Errorf("schedule not found: %s", id)
	}
	cp := *sc
	return &cp, nil
}

func (s *ScheduleStore) UpdateScheduleRun(id string, last, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.schedules[id]
	if !ok {
		return fmt.Errorf("schedule not found: %s", id)
	}
	sc.LastRun = last
	sc.NextRun = next
	return nil
}
