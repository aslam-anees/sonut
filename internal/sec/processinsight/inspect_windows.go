//go:build windows

package processinsight

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func inspect(_ context.Context, target Target) (Result, error) {
	result := Result{Target: target, ObservedAt: time.Now().UTC(), Source: "windows-process-api"}
	if target.Kind != TargetPID {
		result.Unsupported = true
		result.Warnings = []string{"this collector currently supports PID targets on Windows; port/name/file targets require a native collector"}
		return result, nil
	}
	pid, _ := strconv.Atoi(target.Value)
	if pid <= 0 || pid == os.Getpid() {
		result.Warnings = []string{fmt.Sprintf("PID %d is not an inspectable child target", pid)}
		return result, nil
	}
	result.Unsupported = true
	result.Warnings = []string{"Windows process metadata is intentionally unavailable until the native collector is added; no elevation was attempted"}
	result.Warnings = append(result.Warnings, strings.TrimSpace("target PID "+strconv.Itoa(pid)))
	return result, nil
}
