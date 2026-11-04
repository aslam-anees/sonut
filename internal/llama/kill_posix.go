//go:build !windows

package llama

import (
	"os"
	"syscall"
)

// signalPIDBestEffort sends sig to pid, preferring the whole process group when
// pid is its own group leader (ConfigureChildProcessGroup children). That way
// forked descendants die with llama-server instead of being orphaned on exit.
func signalPIDBestEffort(pid int, sig os.Signal) {
	if pid <= 1 {
		return
	}
	sysSig, ok := sig.(syscall.Signal)
	if !ok {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(sig)
		}
		return
	}
	target := pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		target = -pid // whole group
	}
	_ = syscall.Kill(target, sysSig)
}
