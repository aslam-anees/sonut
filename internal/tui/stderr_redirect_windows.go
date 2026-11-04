//go:build windows

package tui

import "os"

// redirectStderrToLog is a no-op on Windows: the console host is not corrupted
// by interleaved stderr writes the way a raw-mode Unix terminal is, and fd
// duplication semantics differ. The Unix implementation carries the rationale.
func redirectStderrToLog() (terminal *os.File, restore func()) {
	return os.Stderr, func() {}
}
