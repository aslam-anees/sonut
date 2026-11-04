// Portions derived from DEEIX-Chat, Copyright 2026 DEEIX-AI, Apache-2.0.
// Modified for @code; see WIRING.md and third_party/licenses/DEEIX-Chat-LICENSE.txt.
// Package ratelimit provides sliding- and fixed-window rate limiting.
// Adapted from the RateLimiter interface consumed by DEEIX-Chat/backend's
// internal/transport/http/middleware.RateLimit; the original relied on a
// Redis-backed implementation. MemoryLimiter here is a standalone, in-memory
// default so the enterprise layer runs without external services.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Limiter is the capability the security layer needs for request throttling.
type Limiter interface {
	// AllowSlidingWindow reports whether one more request under key is
	// permitted within the trailing window, allowing at most limit requests.
	AllowSlidingWindow(ctx context.Context, key string, limit int, window time.Duration, ttl time.Duration) (bool, error)
	// AllowFixedWindow reports whether one more request under any of keys is
	// permitted within the current fixed window, allowing at most limit
	// requests per key per window.
	AllowFixedWindow(ctx context.Context, keys []string, limit int, ttl time.Duration) (bool, error)
}

type slidingEntry struct {
	hits []time.Time
}

type fixedEntry struct {
	count      int
	windowFrom time.Time
	ttl        time.Duration
}

// MemoryLimiter is a thread-safe, single-process Limiter implementation.
type MemoryLimiter struct {
	mu      sync.Mutex
	sliding map[string]*slidingEntry
	fixed   map[string]*fixedEntry
	now     func() time.Time
}

// NewMemoryLimiter creates an in-memory Limiter.
func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{
		sliding: make(map[string]*slidingEntry),
		fixed:   make(map[string]*fixedEntry),
		now:     time.Now,
	}
}

// AllowSlidingWindow implements Limiter.
func (m *MemoryLimiter) AllowSlidingWindow(_ context.Context, key string, limit int, window time.Duration, _ time.Duration) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	entry, ok := m.sliding[key]
	if !ok {
		entry = &slidingEntry{}
		m.sliding[key] = entry
	}

	cutoff := now.Add(-window)
	filtered := entry.hits[:0]
	for _, t := range entry.hits {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}
	entry.hits = filtered

	if len(entry.hits) >= limit {
		return false, nil
	}
	entry.hits = append(entry.hits, now)
	return true, nil
}

// AllowFixedWindow implements Limiter. It checks and increments every key in
// keys and denies the request if any key is already at its limit for the
// current window.
func (m *MemoryLimiter) AllowFixedWindow(_ context.Context, keys []string, limit int, ttl time.Duration) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	for _, key := range keys {
		entry, ok := m.fixed[key]
		if !ok || now.Sub(entry.windowFrom) >= entry.ttl {
			entry = &fixedEntry{windowFrom: now, ttl: ttl}
			m.fixed[key] = entry
		}
		if entry.count >= limit {
			return false, nil
		}
	}
	for _, key := range keys {
		m.fixed[key].count++
	}
	return true, nil
}
