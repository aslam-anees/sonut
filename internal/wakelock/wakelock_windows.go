//go:build windows

package wakelock

import (
	"runtime"

	"golang.org/x/sys/windows"
)

// SetThreadExecutionState flags. ES_CONTINUOUS makes the state persist until
// the next call (rather than resetting after one idle check); ES_SYSTEM_REQUIRED
// forces the system to stay awake.
const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

var (
	modkernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState = modkernel32.NewProc("SetThreadExecutionState")
)

func setThreadExecutionState(flags uint32) {
	_, _, _ = procSetThreadExecutionState.Call(uintptr(flags))
}

// acquire calls SetThreadExecutionState with ES_CONTINUOUS|ES_SYSTEM_REQUIRED
// so Windows does not sleep while atcode runs. The execution-state flag is
// per-thread and is cleared when that thread exits, so a dedicated goroutine
// locks its OS thread and stays alive until release, then clears the state with
// a bare ES_CONTINUOUS. This is process-lifetime safe: if the goroutine's thread
// were allowed to migrate or exit, Windows would silently drop the request.
func acquire(reason string) func() {
	_ = reason
	done := make(chan struct{})
	released := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		setThreadExecutionState(esContinuous | esSystemRequired)
		<-done
		setThreadExecutionState(esContinuous) // clear the request
		close(released)
	}()
	return func() {
		close(done)
		<-released
	}
}
