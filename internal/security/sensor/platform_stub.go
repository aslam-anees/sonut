//go:build !linux

package sensor

import (
	"errors"
	"fmt"
	"os"
)

func isLinux() bool { return false }

func probeCapabilities() Capabilities {
	return Capabilities{Platform: "unsupported"}
}

func validateTargetPID(pid int) error {
	if pid <= 0 {
		return errors.New("sensor target PID must be positive")
	}
	return nil
}

func processAlive(pid int) bool {
	// There is no portable, non-invasive PID liveness probe in the standard
	// library. The status file is still bounded and the next explicit status
	// read will refresh it on Linux; unsupported platforms never start a helper.
	return pid > 0
}

func signalHelperProcess(process *os.Process) error {
	return process.Kill()
}

func RunHelper([]string) error {
	return fmt.Errorf("Linux eBPF sensor is unsupported on this platform")
}
