// Package processinsight provides a bounded, read-only explanation of local
// processes, ports, and executable files for /sec. It intentionally exposes
// summaries rather than full environments or arbitrary process control.
package processinsight

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	MaxAncestry = 32
	MaxChildren = 128
	MaxOutput   = 64 * 1024
)

type TargetKind string

const (
	TargetPID  TargetKind = "pid"
	TargetPort TargetKind = "port"
	TargetName TargetKind = "name"
	TargetFile TargetKind = "file"
)

type Target struct {
	Kind  TargetKind `json:"kind"`
	Value string     `json:"value"`
}

func PID(value string) Target  { return Target{Kind: TargetPID, Value: value} }
func Port(value string) Target { return Target{Kind: TargetPort, Value: value} }
func Name(value string) Target { return Target{Kind: TargetName, Value: value} }
func File(value string) Target { return Target{Kind: TargetFile, Value: value} }

type Process struct {
	PID         int      `json:"pid"`
	PPID        int      `json:"ppid,omitempty"`
	Command     string   `json:"command,omitempty"`
	Arguments   string   `json:"arguments,omitempty"`
	Executable  string   `json:"executable,omitempty"`
	User        string   `json:"user,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	ContainerID string   `json:"container_id,omitempty"`
	Sockets     []Socket `json:"sockets,omitempty"`
}

type Socket struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port,omitempty"`
	State    string `json:"state,omitempty"`
}

type Result struct {
	Target      Target    `json:"target"`
	Process     *Process  `json:"process,omitempty"`
	Ancestry    []Process `json:"ancestry,omitempty"`
	Children    []Process `json:"children,omitempty"`
	Source      string    `json:"source,omitempty"`
	Warnings    []string  `json:"warnings,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
	Truncated   bool      `json:"truncated,omitempty"`
	NotFound    bool      `json:"not_found,omitempty"`
	Unsupported bool      `json:"unsupported,omitempty"`
}

var ErrInvalidTarget = errors.New("invalid process-insight target")

func Inspect(ctx context.Context, target Target) (Result, error) {
	target.Value = strings.TrimSpace(target.Value)
	if target.Value == "" {
		return Result{}, ErrInvalidTarget
	}
	switch target.Kind {
	case TargetPID:
		value, err := strconv.Atoi(target.Value)
		if err != nil || value <= 0 {
			return Result{}, fmt.Errorf("target pid must be a positive integer")
		}
	case TargetPort:
		value, err := strconv.Atoi(target.Value)
		if err != nil || value < 1 || value > 65535 {
			return Result{}, fmt.Errorf("target port must be an integer from 1 to 65535")
		}
	case TargetName, TargetFile:
	default:
		return Result{}, fmt.Errorf("unsupported target kind %q", target.Kind)
	}
	return inspect(ctx, target)
}
