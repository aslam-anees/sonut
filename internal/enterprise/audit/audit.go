// Portions derived from DEEIX-Chat, Copyright 2026 DEEIX-AI, Apache-2.0.
// Modified for @code; see WIRING.md and third_party/licenses/DEEIX-Chat-LICENSE.txt.
// Package audit provides an append-only audit trail for security-relevant
// actions (auth events, permission checks, admin actions). Adapted from
// DEEIX-Chat/backend's internal/domain/audit and internal/application/audit
// packages: the original Service persisted through a Postgres-backed
// repository.AuditRepository and logged via zap. Here that dependency is
// replaced by a small Store interface with an in-memory default, so the
// package runs standalone with no external services.
package audit

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Log is one audit record.
type Log struct {
	ID          uint64
	RequestID   string
	ActorUserID uint64
	Action      string
	Resource    string
	ResourceID  string
	IP          string
	UserAgent   string
	DetailJSON  string
	CreatedAt   time.Time
}

// ListFilter narrows a List query.
type ListFilter struct {
	Resource    string
	Action      string
	ActorUserID uint64
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

// Store persists audit logs. MemoryStore is the default, standalone
// implementation; production embedders may supply their own (e.g. backed by
// a file or database) by implementing this interface.
type Store interface {
	Create(ctx context.Context, log *Log) error
	List(ctx context.Context, offset, limit int, filter ListFilter) ([]Log, int64, error)
}

// Sink receives every audit record as it is written, in addition to Store
// persistence. Useful for structured logging. Optional.
type Sink func(Log)

// Service records and queries audit logs.
type Service struct {
	store Store
	sink  Sink

	mu     sync.Mutex
	nextID uint64
}

// NewService creates an audit Service backed by store. If sink is non-nil it
// is invoked synchronously after every successful write (e.g. to forward the
// record to a structured logger).
func NewService(store Store, sink Sink) *Service {
	if store == nil {
		store = NewMemoryStore()
	}
	return &Service{store: store, sink: sink}
}

const (
	defaultPageSize = 20
	maxPageSize     = 1000
)

// Write records an audit log entry. Persistence errors are returned to the
// caller rather than swallowed, since callers embedding this package decide
// their own failure policy (the original DEEIX-Chat service only logged
// persistence failures and continued).
func (s *Service) Write(
	ctx context.Context,
	requestID string,
	actorUserID uint64,
	action string,
	resource string,
	resourceID string,
	ip string,
	userAgent string,
	detail interface{},
) error {
	detailJSON := "{}"
	if detail != nil {
		if raw, err := json.Marshal(detail); err == nil {
			detailJSON = string(raw)
		}
	}

	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.mu.Unlock()

	log := &Log{
		ID:          id,
		RequestID:   requestID,
		ActorUserID: actorUserID,
		Action:      action,
		Resource:    resource,
		ResourceID:  resourceID,
		IP:          ip,
		UserAgent:   userAgent,
		DetailJSON:  detailJSON,
		CreatedAt:   time.Now().UTC(),
	}

	if err := s.store.Create(ctx, log); err != nil {
		return err
	}
	if s.sink != nil {
		s.sink(*log)
	}
	return nil
}

// List returns a page of audit logs matching filter.
func (s *Service) List(ctx context.Context, page int, pageSize int, filter ListFilter) ([]Log, int64, error) {
	offset, limit := normalizePage(page, pageSize)
	return s.store.List(ctx, offset, limit, filter)
}

func normalizePage(page int, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	offset := (page - 1) * pageSize
	if offset < 0 {
		offset = 0
	}
	return offset, pageSize
}
