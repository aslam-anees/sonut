// Portions derived from DEEIX-Chat, Copyright 2026 DEEIX-AI, Apache-2.0.
// Modified for @code; see WIRING.md and third_party/licenses/DEEIX-Chat-LICENSE.txt.
package audit

import (
	"context"
	"sync"
)

// MemoryStore is a thread-safe in-memory Store. It is the default backend
// for Service when no persistent store is supplied, and is suitable for
// tests and single-process embedders.
type MemoryStore struct {
	mu   sync.Mutex
	logs []Log
}

// NewMemoryStore creates an empty in-memory audit store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

// Create appends log to the store.
func (m *MemoryStore) Create(_ context.Context, log *Log) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, *log)
	return nil
}

// List returns logs matching filter, newest first, paginated by offset/limit.
func (m *MemoryStore) List(_ context.Context, offset, limit int, filter ListFilter) ([]Log, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	matched := make([]Log, 0, len(m.logs))
	for i := len(m.logs) - 1; i >= 0; i-- {
		log := m.logs[i]
		if !matches(log, filter) {
			continue
		}
		matched = append(matched, log)
	}

	total := int64(len(matched))
	if offset >= len(matched) {
		return []Log{}, total, nil
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	out := make([]Log, end-offset)
	copy(out, matched[offset:end])
	return out, total, nil
}

func matches(log Log, filter ListFilter) bool {
	if filter.Resource != "" && log.Resource != filter.Resource {
		return false
	}
	if filter.Action != "" && log.Action != filter.Action {
		return false
	}
	if filter.ActorUserID != 0 && log.ActorUserID != filter.ActorUserID {
		return false
	}
	if filter.CreatedFrom != nil && log.CreatedAt.Before(*filter.CreatedFrom) {
		return false
	}
	if filter.CreatedTo != nil && log.CreatedAt.After(*filter.CreatedTo) {
		return false
	}
	return true
}
