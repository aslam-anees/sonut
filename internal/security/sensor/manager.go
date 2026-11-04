package sensor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const statusFile = ".atcode/sec/sensor-status.json"

type Manager struct {
	root string
}

func NewManager(root string) *Manager {
	if root == "" {
		root = "."
	}
	return &Manager{root: root}
}

func (m *Manager) Status() Status {
	status := unavailableStatus()
	if data, err := os.ReadFile(filepath.Join(m.root, filepath.FromSlash(statusFile))); err == nil {
		var stored Status
		if json.Unmarshal(data, &stored) == nil && stored.Version == statusVersion {
			status = stored
			status.Capabilities = probeCapabilities()
			if status.State == StateRunning && !processAlive(status.HelperPID) {
				status.State = StateOff
				status.Mode = ModeOff
				status.LastError = "sensor helper is no longer running"
				status.UpdatedAt = time.Now().UTC()
			}
		}
	}
	return status
}

func (m *Manager) Start(options Options) error {
	if !isLinux() {
		return fmt.Errorf("Linux eBPF sensor is unsupported on %s", probeCapabilities().Platform)
	}
	if current := m.Status(); current.State == StateRunning {
		return fmt.Errorf("sensor helper is already running (pid %d)", current.HelperPID)
	}
	if options.Root == "" {
		options.Root = m.root
	}
	if options.TargetPID == 0 {
		options.TargetPID = os.Getpid()
	}
	if err := validateTargetPID(options.TargetPID); err != nil {
		return err
	}
	if err := ValidateHelperOptions(options); err != nil {
		return err
	}
	helper, err := findHelper(options.HelperPath)
	if err != nil {
		return err
	}
	ownerUID := os.Getuid()
	status := Status{Version: statusVersion, State: StateStarting, Mode: ModeOff, Capabilities: probeCapabilities(), TargetPID: options.TargetPID, OwnerUID: ownerUID, Cgroup: options.CgroupPath, UpdatedAt: time.Now().UTC()}
	if err := writeStatus(options.Root, status, ownerUID); err != nil {
		return err
	}
	args := []string{helper, "--root", options.Root, "--pid", strconv.Itoa(options.TargetPID), "--owner-uid", strconv.Itoa(ownerUID)}
	if options.CgroupPath != "" {
		args = append(args, "--cgroup", options.CgroupPath)
	}
	for _, cidr := range options.DeniedCIDRs {
		args = append(args, "--deny-cidr", cidr)
	}
	for _, port := range options.DeniedPorts {
		args = append(args, "--deny-port", strconv.Itoa(port))
	}
	if options.Enforce {
		args = append(args, "--enforce")
	}
	command := exec.Command(args[0], args[1:]...)
	command.Stdin = nil
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	if os.Geteuid() != 0 {
		args = append([]string{"sudo", "--"}, args...)
		command = exec.Command(args[0], args[1:]...)
		command.Stdin = nil
		command.Stdout = os.Stderr
		command.Stderr = os.Stderr
	}
	if err := command.Start(); err != nil {
		status.State = StateError
		status.Mode = ModeUnavailable
		status.LastError = boundedError(err)
		_ = writeStatus(options.Root, status, ownerUID)
		return fmt.Errorf("start sensor helper: %w", err)
	}
	_ = command.Process.Release()
	return nil
}

func (m *Manager) Stop() error {
	status := m.Status()
	if status.State != StateRunning && status.State != StateStarting {
		return nil
	}
	if status.HelperPID <= 0 {
		return errors.New("sensor status has no helper pid")
	}
	process, err := os.FindProcess(status.HelperPID)
	if err != nil {
		return err
	}
	if err := signalHelperProcess(process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop sensor helper: %w", err)
	}
	status.State = StateOff
	status.Mode = ModeOff
	status.UpdatedAt = time.Now().UTC()
	return writeStatus(m.root, status, os.Getuid())
}

func findHelper(explicit string) (string, error) {
	if explicit == "" {
		explicit = os.Getenv("ATCODE_SENSOR_HELPER")
	}
	if explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", errors.New("ATCODE_SENSOR_HELPER must be an absolute path")
		}
		if info, err := os.Stat(explicit); err != nil || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("sensor helper is not executable: %s", explicit)
		}
		return explicit, nil
	}
	path, err := exec.LookPath("atcode-sensor")
	if err != nil {
		return "", errors.New("atcode-sensor helper not found; install the reviewed helper or set ATCODE_SENSOR_HELPER")
	}
	return path, nil
}

func ValidateHelperOptions(options Options) error {
	if options.Root == "" {
		return errors.New("sensor root is required")
	}
	if _, err := ValidatePolicy(options.DeniedCIDRs, options.DeniedPorts); err != nil {
		return err
	}
	if options.CgroupPath != "" {
		clean := filepath.Clean(options.CgroupPath)
		if !strings.HasPrefix(clean, "/sys/fs/cgroup/") && clean != "/sys/fs/cgroup" {
			return errors.New("sensor cgroup must be under /sys/fs/cgroup")
		}
	}
	return nil
}

func writeStatus(root string, status Status, ownerUID int) error {
	path := filepath.Join(root, filepath.FromSlash(statusFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sensor-status-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if ownerUID >= 0 && os.Geteuid() == 0 {
		_ = tmp.Chown(ownerUID, -1)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 180 {
		return value[:180] + "…"
	}
	return value
}
