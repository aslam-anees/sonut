//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"time"
)

// bashWaitDelay bounds how long Wait blocks for the I/O pipes to drain after the
// process has exited or the context's Cancel has run, so a backgrounded child
// holding the pipes cannot make Run() hang past the timeout. Var (not const) so
// tests can shorten it.
var bashWaitDelay = 2 * time.Second

// isBackgroundCommand and rewriteForBackground are no-ops on Windows: cmd.exe
// does not use Unix-style & backgrounding, and setsid is not available.
func isBackgroundCommand(_ string) bool              { return false }
func rewriteForBackground(t string) (string, string) { return t, "" }

// hardenProcessLifetime makes a Windows shell command killable as a process
// tree. cmd.exe starts helper commands as child processes, so killing only the
// shell can leave a long-running child alive and holding cwd/temp handles after
// @code exits.
func hardenProcessLifetime(command *exec.Cmd) {
	command.WaitDelay = bashWaitDelay
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		_ = exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(command.Process.Pid)).Run()
		return nil
	}
}
