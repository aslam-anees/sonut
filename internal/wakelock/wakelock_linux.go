//go:build linux

package wakelock

import (
	"os/exec"
	"syscall"
)

// acquire holds an idle/sleep inhibitor for the session. systemd-logind is the
// primary Linux mechanism; GNOME's session inhibitor covers desktop systems
// without systemd-inhibit. Both helpers own a `sleep infinity` child and are
// terminated as one process group on release.
func acquire(reason string) func() {
	if reason == "" {
		reason = "atcode is running"
	}
	if path, err := exec.LookPath("systemd-inhibit"); err == nil {
		if release := startInhibitor(path,
			"--what=idle:sleep",
			"--who=atcode",
			"--why="+reason,
			"--mode=block",
			"sleep", "infinity",
		); release != nil {
			return release
		}
	}
	if path, err := exec.LookPath("gnome-session-inhibit"); err == nil {
		return startInhibitor(path,
			"--app-id=atcode",
			"--reason="+reason,
			"--inhibit=idle:suspend",
			"sleep", "infinity",
		)
	}
	return nil
}

func startInhibitor(path string, args ...string) func() {
	cmd := exec.Command(path, args...)
	// New process group so release can signal the helper AND its sleep child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil
	}
	return func() {
		if cmd.Process != nil {
			// Negative pid targets the whole group; closing systemd-inhibit
			// releases the logind lock.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		_ = cmd.Wait()
	}
}
