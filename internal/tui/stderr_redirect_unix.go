//go:build !windows

package tui

import (
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// redirectStderrToLog swaps the process's fd 2 to ~/.atcode/tui-stderr.log for
// the TUI's lifetime. While Bubble Tea owns the alt-screen, ANY write to the
// inherited stderr — the Go log package, library diagnostics, macOS malloc
// warnings from spawned helpers (`MallocStackLogging: ...`), a grandchild that
// inherited fd 2 — lands raw in the middle of the rendered frame and corrupts
// the display. Redirecting the fd itself (not just os.Stderr) closes the whole
// class, including writes from child processes.
//
// It returns the real terminal's stderr as a writer (for the notifier's
// bell/OSC-9 sequences and post-exit messages) and a restore func that puts
// fd 2 back. Both are safe non-nil no-op-ish fallbacks when the redirect
// cannot be set up — the TUI then runs exactly as before.
func redirectStderrToLog() (terminal *os.File, restore func()) {
	terminal, restore = os.Stderr, func() {}

	home, err := os.UserHomeDir()
	if err != nil {
		return terminal, restore
	}
	logPath := filepath.Join(home, ".atcode", "tui-stderr.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return terminal, restore
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return terminal, restore
	}

	savedFd, err := unix.Dup(int(os.Stderr.Fd()))
	if err != nil {
		logFile.Close()
		return terminal, restore
	}
	unix.CloseOnExec(savedFd)
	if err := unix.Dup2(int(logFile.Fd()), int(os.Stderr.Fd())); err != nil {
		unix.Close(savedFd)
		logFile.Close()
		return terminal, restore
	}

	terminal = os.NewFile(uintptr(savedFd), "/dev/stderr")
	// The log package writes through os.Stderr, which now points at the file;
	// set it explicitly anyway so a future stderr reassignment can't re-corrupt.
	log.SetOutput(logFile)
	restore = func() {
		_ = unix.Dup2(savedFd, int(os.Stderr.Fd()))
		log.SetOutput(os.Stderr)
		logFile.Close()
	}
	return terminal, restore
}
