package sensor

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aistatine/sonut/internal/security"
)

// Event is the sanitized userspace projection of a kernel observation. It has
// no packet payload or file content. ToolCallID is empty when the process
// could not be correlated to an in-process tool invocation.
type Event struct {
	ID          string    `json:"id"`
	Timestamp   time.Time `json:"timestamp"`
	PID         uint32    `json:"pid,omitempty"`
	ParentPID   uint32    `json:"parent_pid,omitempty"`
	Destination string    `json:"destination,omitempty"`
	Port        uint16    `json:"port,omitempty"`
	Protocol    string    `json:"protocol,omitempty"`
	Verdict     string    `json:"verdict"`
	ToolCallID  string    `json:"tool_call_id,omitempty"`
	Evidence    string    `json:"evidence,omitempty"`
}

// ToSecurityEvent converts a sanitized kernel observation to the shared
// runtime event schema. The conversion carries provenance and verdict only;
// there is intentionally no payload field to copy.
func (event Event) ToSecurityEvent(sessionID string) security.Event {
	verdict := security.VerdictAllowed
	if event.Verdict == "blocked" {
		verdict = security.VerdictBlocked
	}
	return security.Event{
		ID: event.ID, Timestamp: event.Timestamp, Direction: security.DirectionOutbound,
		Source: "ebpf", Coverage: security.CoverageObserved, SessionID: sessionID,
		ToolCallID: event.ToolCallID, PID: int(event.PID), ParentPID: int(event.ParentPID),
		Tool: "runtime-sensor", Host: event.Destination, Port: int(event.Port),
		Protocol: event.Protocol, Classification: security.Clean, Verdict: verdict,
		Reason: event.Evidence, EvidenceRefs: []string{event.Evidence},
	}
}

func parseKernelEvent(data []byte) (Event, error) {
	if len(data) < 16 {
		return Event{}, fmt.Errorf("sensor event is truncated")
	}
	ip := net.IPv4(data[0], data[1], data[2], data[3]).String()
	port := binary.BigEndian.Uint16(data[4:6])
	pid := binary.LittleEndian.Uint32(data[8:12])
	verdict := "allowed"
	if data[12] != 0 {
		verdict = "blocked"
	}
	now := time.Now().UTC()
	return Event{ID: fmt.Sprintf("sensor-%d-%d", pid, now.UnixNano()), Timestamp: now, PID: pid, Destination: ip, Port: port, Protocol: "tcp", Verdict: verdict, Evidence: "ebpf:cgroup/connect4"}, nil
}

type Counters struct {
	events  atomic.Uint64
	blocked atomic.Uint64
	dropped atomic.Uint64
}

func (c *Counters) Add(event Event) {
	c.events.Add(1)
	if event.Verdict == "blocked" {
		c.blocked.Add(1)
	}
}

func (c *Counters) Drop() { c.dropped.Add(1) }

func (c *Counters) Snapshot() (events, blocked, dropped uint64) {
	return c.events.Load(), c.blocked.Load(), c.dropped.Load()
}

// Lineage tracks a bounded PID-to-tool relationship. It is deliberately not
// a process inventory: only explicitly registered descendants are retained.
type Lineage struct {
	mu      sync.Mutex
	entries map[uint32]string
	max     int
}

func NewLineage(max int) *Lineage {
	if max < 1 {
		max = 256
	}
	return &Lineage{entries: make(map[uint32]string, max), max: max}
}

func (l *Lineage) Register(pid uint32, toolCallID string) {
	if pid == 0 || toolCallID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) >= l.max {
		for oldPID := range l.entries {
			delete(l.entries, oldPID)
			break
		}
	}
	l.entries[pid] = toolCallID
}

func (l *Lineage) Resolve(pid, parentPID uint32) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if value := l.entries[pid]; value != "" {
		return value
	}
	return l.entries[parentPID]
}

func (l *Lineage) Remove(pid uint32) {
	l.mu.Lock()
	delete(l.entries, pid)
	l.mu.Unlock()
}
