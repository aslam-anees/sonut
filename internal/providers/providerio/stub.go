package providerio

import (
	"os"
	"time"
)

type TokenResolver func(ctx interface{}, forceRefresh bool) (string, string, bool, error)

// defaultStreamIdleTimeout is how long the stream may stay silent before the
// TUI treats it as unusually quiet and the content-stall watchdog arms.
const defaultStreamIdleTimeout = 120 * time.Second

// ResolveStreamIdleTimeout returns the stream idle timeout: the
// SONUT_STREAM_IDLE_TIMEOUT env override when set, otherwise the caller's
// value, otherwise the default.
func ResolveStreamIdleTimeout(d time.Duration) time.Duration {
	if raw := os.Getenv("SONUT_STREAM_IDLE_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	if d > 0 {
		return d
	}
	return defaultStreamIdleTimeout
}

// ContentStallTimeout is the ceiling after which the watchdog recovers a
// stalled stream: 1.2x the idle timeout.
func ContentStallTimeout(idle time.Duration) time.Duration {
	if idle <= 0 {
		idle = defaultStreamIdleTimeout
	}
	return idle * 6 / 5
}
