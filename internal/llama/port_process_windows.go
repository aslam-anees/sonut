//go:build windows

package llama

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func pidsOnPort(port int) []int {
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return nil
	}
	wantSuffix := ":" + strconv.Itoa(port)
	seen := map[int]bool{}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		localAddress := strings.Trim(fields[1], "[]")
		if !strings.HasSuffix(localAddress, wantSuffix) ||
			!strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		pid, parseErr := strconv.Atoi(fields[len(fields)-1])
		if parseErr == nil && pid > 0 && !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	return pids
}

func cmdlineOnPort(port int) string {
	pids := pidsOnPort(port)
	if len(pids) == 0 {
		return ""
	}
	script := fmt.Sprintf(
		"(Get-CimInstance Win32_Process -Filter 'ProcessId = %d').CommandLine",
		pids[0],
	)
	out, err := exec.Command(
		"powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		script,
	).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
