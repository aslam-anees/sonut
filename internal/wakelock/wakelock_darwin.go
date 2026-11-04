//go:build darwin

package wakelock

import (
	"os"
	"os/exec"
	"strconv"
)

// acquire runs `caffeinate -i` so macOS does not idle-sleep while atcode is
// running. caffeinate ships with macOS; if it is somehow missing, acquisition
// is a no-op. -i prevents idle system sleep (the display may still sleep, which
// keeps battery drain low). -w <pid> binds caffeinate to our process so it
// exits on its own if atcode dies without releasing, avoiding a leaked helper.
func acquire(reason string) func() {
	_ = reason
	path, err := exec.LookPath("caffeinate")
	if err != nil {
		return nil
	}
	cmd := exec.Command(path, "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil
	}
	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}
}
