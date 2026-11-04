//go:build !darwin && !windows && !linux

package wakelock

// acquire is a no-op on platforms without a supported sleep-inhibit mechanism
// (e.g. *BSD). The public Acquire still returns a safe release function.
func acquire(reason string) func() { return nil }
