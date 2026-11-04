// Package sensor contains the opt-in, kernel-facing runtime sensor boundary.
//
// The package deliberately keeps the privileged helper's contract small. The
// TUI and agent never receive BPF file descriptors, arbitrary programs, or a
// writable control socket; they can only request the reviewed helper with a
// bounded policy after an explicit human action.
package sensor

import "time"

const statusVersion = 1

type State string

const (
	StateOff         State = "off"
	StateStarting    State = "starting"
	StateRunning     State = "running"
	StateUnavailable State = "unavailable"
	StateError       State = "error"
)

type Mode string

const (
	ModeOff         Mode = "off"
	ModeEnforce     Mode = "network-enforce"
	ModeDetectOnly  Mode = "detect-only"
	ModeUnavailable Mode = "unavailable"
	ModeNetworkOnly Mode = "network-only"
)

// Options is intentionally narrower than a general BPF policy. It accepts
// only bounded destination CIDRs and the process/cgroup scope selected by the
// parent. Hostname matching stays in Tool Guard/userspace.
type Options struct {
	Root        string
	TargetPID   int
	CgroupPath  string
	DeniedCIDRs []string
	DeniedPorts []int
	Enforce     bool
	HelperPath  string
}

type Capabilities struct {
	Platform      string `json:"platform"`
	CgroupV2      bool   `json:"cgroup_v2"`
	BTF           bool   `json:"btf"`
	BPFSyscall    bool   `json:"bpf_syscall"`
	RingBuffer    bool   `json:"ring_buffer"`
	CgroupConnect bool   `json:"cgroup_connect"`
	LSM           bool   `json:"ebpf_lsm"`
}

type Status struct {
	Version       int          `json:"version"`
	State         State        `json:"state"`
	Mode          Mode         `json:"mode"`
	Capabilities  Capabilities `json:"capabilities"`
	HelperPID     int          `json:"helper_pid,omitempty"`
	TargetPID     int          `json:"target_pid,omitempty"`
	OwnerUID      int          `json:"owner_uid,omitempty"`
	Cgroup        string       `json:"cgroup,omitempty"`
	AttachedHooks []string     `json:"attached_hooks,omitempty"`
	DegradedHooks []string     `json:"degraded_hooks,omitempty"`
	FailedHooks   []string     `json:"failed_hooks,omitempty"`
	Events        uint64       `json:"events"`
	Blocked       uint64       `json:"blocked"`
	Dropped       uint64       `json:"dropped"`
	LastError     string       `json:"last_error,omitempty"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

func unavailableStatus() Status {
	return Status{
		Version:      statusVersion,
		State:        StateUnavailable,
		Mode:         ModeUnavailable,
		Capabilities: probeCapabilities(),
		UpdatedAt:    time.Now().UTC(),
	}
}
