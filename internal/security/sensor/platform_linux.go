//go:build linux

package sensor

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func isLinux() bool { return true }

func probeCapabilities() Capabilities {
	return linuxCapabilities()
}

func cgroupV2Mounted() bool {
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 3 && fields[2] == "cgroup2" {
			return true
		}
	}
	return false
}

func validateTargetPID(pid int) error {
	if pid <= 0 {
		return errors.New("sensor target PID must be positive")
	}
	if os.Geteuid() == 0 {
		return nil
	}
	info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	if err != nil {
		return fmt.Errorf("sensor target PID %d is unavailable: %w", pid, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && int(stat.Uid) != os.Getuid() {
		return errors.New("sensor target PID must belong to the invoking user")
	}
	return nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func signalHelperProcess(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}
