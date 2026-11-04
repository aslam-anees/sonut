package wakelock

import "testing"

// SONUT_ALLOW_SLEEP opts out entirely: Acquire must return a non-nil no-op
// release and never touch the platform helper.
func TestAcquireOptOut(t *testing.T) {
	t.Setenv("SONUT_ALLOW_SLEEP", "1")
	release := Acquire("test")
	if release == nil {
		t.Fatal("Acquire must never return nil")
	}
	release() // must not panic
}

// The release function must be safe to call more than once and from the normal
// path (env unset): only the first call takes effect, the rest are no-ops.
func TestReleaseIdempotent(t *testing.T) {
	t.Setenv("SONUT_ALLOW_SLEEP", "")
	release := Acquire("test")
	if release == nil {
		t.Fatal("Acquire must never return nil")
	}
	release()
	release() // second call must not panic or block
}
