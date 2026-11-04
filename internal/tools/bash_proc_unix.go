//go:build !windows

package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// bashWaitDelay bounds how long Wait blocks for the I/O pipes to drain after the
// process has exited or the context's Cancel has run. Without it, a backgrounded
// child that inherited the shell's stdout/stderr keeps those pipes open and
// Run() hangs long past the timeout waiting for EOF. Var (not const) so tests
// can shorten it.
var bashWaitDelay = 2 * time.Second

// isBackgroundCommand reports whether commandText ends with an unquoted & that
// would background the process spawned by the shell.
func isBackgroundCommand(commandText string) bool {
	return strings.HasSuffix(strings.TrimRight(commandText, " \t\n\r"), "&")
}

// rewriteForBackground rewrites a command ending with & so the background
// process is decoupled from the shell wrapper's I/O pipes and process group.
//
// Without this, the background process inherits the shell's stdout/stderr
// (the pipes Go set up for the Cmd), so those pipes stay open after the shell
// exits. Go's cmd.Wait() then blocks on pipe-EOF; after WaitDelay it kills
// the entire process group — including the background process — and returns
// exec.ErrWaitDelay, which surfaces as exit -1.
//
// The fix:
//  1. Run the command via setsid, giving it a new session/process group. Our
//     hardenProcessLifetime group-kill targets the outer shell's group and
//     cannot reach the setsid process.
//  2. Redirect all stdio away from the shell's pipes (stdin→/dev/null,
//     stdout/stderr→a log file), so the shell's pipes close immediately when
//     the shell exits and WaitDelay never fires.
//  3. Print the background PID so the tool result tells the model the process
//     started and where to find its output.
func rewriteForBackground(commandText string) (rewritten, logPath string) {
	base := strings.TrimRight(
		strings.TrimSuffix(strings.TrimRight(commandText, " \t\n\r"), "&"),
		" \t\n\r",
	)
	logPath = filepath.Join(os.TempDir(), fmt.Sprintf("sonut-bg-%d.log", time.Now().UnixNano()))
	// Single-quote-escape the base command. This safely embeds arbitrary
	// commands (including those with double-quotes, $, backticks, etc.) by
	// closing the current quote, inserting a literal single quote, and
	// reopening, which is the only quoting that never conflicts with its content.
	escaped := "'" + strings.ReplaceAll(base, "'", `'\''`) + "'"
	rewritten = fmt.Sprintf(
		"setsid sh -c %s </dev/null >>%s 2>&1 & _BG_PID=$!"+
			"; printf 'Background process started\\npid: %%d\\nlog: %s\\n' \"$_BG_PID\"",
		escaped, logPath, logPath,
	)
	return rewritten, logPath
}

// hardenProcessLifetime makes a bash command killable as a single unit so a
// timeout cannot leak backgrounded children.
//
// Setpgid puts the shell into a new process group; since `sh -c` runs without
// job control, every process it forks (including `&` jobs) inherits that group.
// On context cancellation we signal the whole group (negative pid) instead of
// just the shell, so orphaned children die with it. WaitDelay is the backstop:
// if a child still holds the I/O pipes after the group is killed, Wait gives up
// rather than blocking forever.
func hardenProcessLifetime(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	command.WaitDelay = bashWaitDelay
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		// Negative pid targets the process group led by the shell.
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
}
