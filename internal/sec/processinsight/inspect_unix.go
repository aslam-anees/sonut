//go:build !windows

package processinsight

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func inspect(ctx context.Context, target Target) (Result, error) {
	result := Result{Target: target, ObservedAt: time.Now().UTC()}
	switch target.Kind {
	case TargetPID:
		pid, _ := strconv.Atoi(target.Value)
		process, err := readProcess(ctx, pid)
		if err != nil {
			result.NotFound = os.IsNotExist(err)
			result.Warnings = append(result.Warnings, safeWarning(err))
			return result, nil
		}
		result.Process = &process
		result.Source = processSource()
		result.Ancestry = ancestry(ctx, process.PPID)
		result.Children = children(ctx, process.PID)
	case TargetPort:
		pid, socket, err := processForPort(ctx, target.Value)
		if err != nil {
			result.Warnings = append(result.Warnings, safeWarning(err))
			return result, nil
		}
		if pid == 0 {
			result.NotFound = true
			result.Warnings = append(result.Warnings, "no local process owns this port")
			return result, nil
		}
		process, err := readProcess(ctx, pid)
		if err != nil {
			result.Warnings = append(result.Warnings, safeWarning(err))
			return result, nil
		}
		process.Sockets = append(process.Sockets, socket)
		result.Process = &process
		result.Source = "lsof"
	case TargetName:
		pid, err := processForName(ctx, target.Value)
		if err != nil {
			result.Warnings = append(result.Warnings, safeWarning(err))
			return result, nil
		}
		if pid == 0 {
			result.NotFound = true
			return result, nil
		}
		process, err := readProcess(ctx, pid)
		if err != nil {
			result.Warnings = append(result.Warnings, safeWarning(err))
			return result, nil
		}
		result.Process = &process
		result.Source = "ps"
	case TargetFile:
		path, err := filepath.Abs(target.Value)
		if err != nil {
			return result, err
		}
		if _, err := os.Stat(path); err != nil {
			result.NotFound = os.IsNotExist(err)
			result.Warnings = append(result.Warnings, safeWarning(err))
			return result, nil
		}
		pid, err := processForFile(ctx, path)
		if err != nil {
			result.Warnings = append(result.Warnings, safeWarning(err))
			return result, nil
		}
		if pid == 0 {
			result.NotFound = true
			result.Warnings = append(result.Warnings, "no local process has this file open")
			return result, nil
		}
		process, err := readProcess(ctx, pid)
		if err != nil {
			result.Warnings = append(result.Warnings, safeWarning(err))
			return result, nil
		}
		result.Process = &process
		result.Source = "lsof"
	}
	return result, nil
}

func readProcess(ctx context.Context, pid int) (Process, error) {
	if pid <= 0 {
		return Process{}, fmt.Errorf("invalid pid %d", pid)
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
		return readProcLinux(pid)
	}
	output, err := boundedCommand(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,user=,comm=,args=")
	if err != nil {
		return Process{}, err
	}
	line := strings.TrimSpace(output)
	if line == "" {
		return Process{}, os.ErrNotExist
	}
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return Process{}, fmt.Errorf("unreadable process metadata for pid %d", pid)
	}
	ppid, _ := strconv.Atoi(fields[1])
	return Process{PID: pid, PPID: ppid, User: sanitize(fields[2]), Command: sanitize(fields[3]), Arguments: redactArguments(strings.Join(fields[3:], " ")), Executable: sanitize(fields[3])}, nil
}

func readProcLinux(pid int) (Process, error) {
	root := filepath.Join("/proc", strconv.Itoa(pid))
	status, err := os.ReadFile(filepath.Join(root, "status"))
	if err != nil {
		return Process{}, err
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(status))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) == 2 {
			values[parts[0]] = strings.TrimSpace(parts[1])
		}
	}
	ppid := 0
	if fields := strings.Fields(values["PPid"]); len(fields) > 0 {
		ppid, _ = strconv.Atoi(fields[0])
	}
	command, _ := readBoundedFile(filepath.Join(root, "comm"))
	args, _ := readBoundedFile(filepath.Join(root, "cmdline"))
	argsText := strings.TrimRight(strings.ReplaceAll(string(args), "\x00", " "), " ")
	executable, _ := os.Readlink(filepath.Join(root, "exe"))
	working, _ := os.Readlink(filepath.Join(root, "cwd"))
	return Process{PID: pid, PPID: ppid, Command: sanitize(string(command)), Arguments: redactArguments(argsText), Executable: sanitize(executable), WorkingDir: sanitize(working)}, nil
}

func ancestry(ctx context.Context, ppid int) []Process {
	result := make([]Process, 0, 8)
	seen := map[int]bool{}
	for ppid > 0 && len(result) < MaxAncestry && !seen[ppid] {
		seen[ppid] = true
		process, err := readProcess(ctx, ppid)
		if err != nil {
			break
		}
		result = append(result, process)
		ppid = process.PPID
	}
	return result
}

func children(ctx context.Context, parent int) []Process {
	output, err := boundedCommand(ctx, "ps", "-axo", "pid=,ppid=,user=,comm=,args=")
	if err != nil {
		return nil
	}
	result := make([]Process, 0, 8)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		ppid, _ := strconv.Atoi(fields[1])
		if ppid != parent || pid <= 0 {
			continue
		}
		result = append(result, Process{PID: pid, PPID: ppid, User: sanitize(fields[2]), Command: sanitize(fields[3]), Arguments: redactArguments(strings.Join(fields[3:], " "))})
		if len(result) >= MaxChildren {
			break
		}
	}
	return result
}

func processForName(ctx context.Context, name string) (int, error) {
	output, err := boundedCommand(ctx, "ps", "-axo", "pid=,comm=,args=")
	if err != nil {
		return 0, err
	}
	needle := strings.ToLower(name)
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		if pid > 0 && pid != os.Getpid() {
			return pid, nil
		}
	}
	return 0, nil
}

func processForPort(ctx context.Context, port string) (int, Socket, error) {
	output, err := boundedCommand(ctx, "lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fpctn")
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return 0, Socket{}, nil
		}
		return 0, Socket{}, err
	}
	var pid int
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "p") {
			pid, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "p")))
		}
	}
	portNumber, _ := strconv.Atoi(port)
	return pid, Socket{Protocol: "tcp", Port: portNumber, State: "LISTEN"}, nil
}

func processForFile(ctx context.Context, path string) (int, error) {
	output, err := boundedCommand(ctx, "lsof", "-t", "--", path)
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return 0, nil
		}
		return 0, err
	}
	for _, line := range strings.Split(output, "\n") {
		pid, _ := strconv.Atoi(strings.TrimSpace(line))
		if pid > 0 {
			return pid, nil
		}
	}
	return 0, nil
}

func boundedCommand(ctx context.Context, command string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, command, args...)
	output, err := cmd.Output()
	if err != nil {
		if commandCtx.Err() != nil {
			return "", commandCtx.Err()
		}
		return "", err
	}
	if len(output) > MaxOutput {
		output = output[:MaxOutput]
	}
	return sanitize(string(output)), nil
}

func readBoundedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, MaxOutput))
}

func processSource() string {
	if _, err := os.Stat("/proc"); err == nil {
		return "procfs"
	}
	return "ps"
}

func sanitize(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f) {
			return r
		}
		return ' '
	}, value)
	return strings.TrimSpace(value)
}

func safeWarning(err error) string {
	if err == nil {
		return ""
	}
	return sanitize(err.Error())
}
