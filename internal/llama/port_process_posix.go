//go:build !windows

package llama

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func pidsOnPort(port int) []int {
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port)).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, pidText := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(strings.TrimSpace(pidText))
		if err == nil && pid > 0 {
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
	out, err := exec.Command("ps", "-p", strconv.Itoa(pids[0]), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
