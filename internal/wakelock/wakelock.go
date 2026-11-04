// Package wakelock keeps the operating system awake while atcode is running so
// long agent turns, builds, and downloads are not interrupted by idle system
// sleep. It is best-effort and cross-platform: on an unsupported platform, or
// when the platform's sleep-inhibit helper is unavailable, Acquire is a no-op.
package wakelock

import (
	"os"
	"sync"
)

// Acquire asks the OS to stay awake and returns a release function. The release
// is safe to call multiple times and from any goroutine — only the first call
// takes effect. Setting SONUT_ALLOW_SLEEP to any non-empty value disables the
// wake lock entirely (Acquire then returns a no-op release), so users who want
// the machine to sleep normally can opt out.
func Acquire(reason string) func() {
	if os.Getenv("SONUT_ALLOW_SLEEP") != "" {
		return func() {}
	}
	raw := acquire(reason)
	if raw == nil {
		return func() {}
	}
	var once sync.Once
	return func() { once.Do(raw) }
}
