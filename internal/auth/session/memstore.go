package session

import (
	"context"
	"sync"
)

// MemoryStore is a Store in memory, for tests and development. The database store comes with the schema (M3).
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{records: map[string]Record{}} }

func (s *MemoryStore) Get(_ context.Context, key string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[key]
	if !ok {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func (s *MemoryStore) Put(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[r.Key] = r
	return nil
}

func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, key)
	return nil
}

func (s *MemoryStore) DeleteForUser(_ context.Context, userID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.records {
		if r.UserID == userID {
			delete(s.records, k)
		}
	}
	return nil
}

// Len returns how many records the store holds.
func (s *MemoryStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// Keys returns the keys of all records.
func (s *MemoryStore) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.records))
	for k := range s.records {
		out = append(out, k)
	}
	return out
}
