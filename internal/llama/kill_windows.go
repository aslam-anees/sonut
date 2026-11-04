//go:build windows

package llama

import "os"

// signalPIDBestEffort sends sig to pid. Windows has no process-group kill
// equivalent used by our launcher; Kill the individual process (Interrupt is
// not implemented on Windows, so both graceful and force paths use Kill).
func signalPIDBestEffort(pid int, sig os.Signal) {
	if pid <= 1 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	// Prefer Kill: os.Interrupt is not implemented on Windows.
	_ = p.Kill()
	_ = sig // silence unused in graceful+force dual calls
}
