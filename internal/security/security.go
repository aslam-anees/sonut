// Package security implements @code's egress-visibility layer: every
// network-capable tool routes its outbound intent through Guard.CheckOutbound
// before bytes leave the machine. It classifies payloads (clean / sensitive /
// blocked), enforces a small local policy (default-block non-80/443 ports
// except localhost, plus an explicit host allowlist), keeps a risk-ranked
// in-memory event log for the TUI panel, and appends every event to an
// append-only JSONL audit log under .atcode/audit/.
package security

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Classification is the outcome of scanning an outbound payload.
type Classification string

const (
	Clean     Classification = "clean"
	Sensitive Classification = "sensitive"
	Blocked   Classification = "blocked"
)

// Verdict is the policy decision applied to the event.
type Verdict string

const (
	VerdictAllowed     Verdict = "allowed"
	VerdictAllowedOnce Verdict = "allowed_once"
	VerdictBlocked     Verdict = "blocked"
)

// Direction identifies the direction of a runtime observation. Outbound
// events are policy checkpoints; inbound events are descriptive observations
// and must never be sent through outbound blocking logic.
type Direction string

const (
	DirectionOutbound Direction = "outbound"
	DirectionInbound  Direction = "inbound"
)

// SurfaceCoverage describes how much enforcement the security layer has for a
// network-capable surface. It is deliberately more precise than a boolean
// "protected" label.
type SurfaceCoverage string

const (
	CoverageEnforced   SurfaceCoverage = "enforced"
	CoverageObserved   SurfaceCoverage = "observed"
	CoverageBestEffort SurfaceCoverage = "best-effort"
	CoverageNotCovered SurfaceCoverage = "not-covered"
)

// OutboundMetadata carries bounded correlation metadata from a tool run. It
// never contains a request payload or secret value.
type OutboundMetadata struct {
	SessionID  string
	ToolCallID string
	PID        int
	ParentPID  int
	Process    string
	Cgroup     string
	Evidence   []string
}

// Event is one outbound network intent observed by the checkpoint.
type Event struct {
	ID             string          `json:"id"`
	Timestamp      time.Time       `json:"timestamp"`
	Direction      Direction       `json:"direction,omitempty"`
	Source         string          `json:"source,omitempty"`
	Coverage       SurfaceCoverage `json:"coverage,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	PID            int             `json:"pid,omitempty"`
	ParentPID      int             `json:"parent_pid,omitempty"`
	Process        string          `json:"process,omitempty"`
	Cgroup         string          `json:"cgroup,omitempty"`
	Tool           string          `json:"tool"`
	Host           string          `json:"host"`
	Port           int             `json:"port"`
	Protocol       string          `json:"protocol"`
	Bytes          int             `json:"bytes"`
	Classification Classification  `json:"classification"`
	Verdict        Verdict         `json:"verdict"`
	Reason         string          `json:"reason,omitempty"`
	BestEffort     bool            `json:"best_effort,omitempty"`
	EvidenceRefs   []string        `json:"evidence_refs,omitempty"`
}

// Policy is the local egress policy, loadable from .atcode/security.json.
type Policy struct {
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
	AllowedPorts []int    `json:"allowed_ports,omitempty"`
}

// DefaultPolicy blocks every port except 80/443 and localhost.
func DefaultPolicy() Policy {
	return Policy{}
}

func (p Policy) hostAllowlisted(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range p.AllowedHosts {
		if strings.ToLower(strings.TrimSuffix(h, ".")) == host {
			return true
		}
	}
	return false
}

func (p Policy) portAllowed(host string, port int) bool {
	if port == 0 || port == 80 || port == 443 {
		return true
	}
	if isLoopbackHost(host) {
		return true
	}
	for _, ap := range p.AllowedPorts {
		if ap == port {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Guard is the single policy checkpoint every network-capable tool routes
// through. It is safe for concurrent use.
type Guard struct {
	mu          sync.Mutex
	policy      Policy
	events      []Event
	allowedOnce map[string]bool // host -> one successful policy override
	audit       *AuditLog
	graph       *Graph
	maxEvents   int
	enterprise  *EnterpriseLayer
	sequence    atomic.Uint64
}

// NewGuard builds a Guard with the given policy and an audit log rooted at
// auditDir (pass "" to disable on-disk auditing, e.g. in unit tests).
func NewGuard(policy Policy, auditDir string) *Guard {
	var audit *AuditLog
	if auditDir != "" {
		audit = NewAuditLog(auditDir)
	}
	return &Guard{
		policy:      policy,
		allowedOnce: make(map[string]bool),
		audit:       audit,
		graph:       NewGraph(),
		maxEvents:   500,
	}
}

var (
	defaultGuard *Guard
	defaultOnce  sync.Once
)

// Default returns the process-wide Guard, initialized on first use against
// .atcode/audit relative to the current directory.
func Default() *Guard {
	defaultOnce.Do(func() {
		defaultGuard = NewGuard(DefaultPolicy(), ".atcode/audit")
	})
	return defaultGuard
}

// SetDefault overrides the process-wide Guard (used by tests and by the CLI
// entrypoint once it has loaded a real .atcode/security.json policy).
func SetDefault(g *Guard) {
	defaultGuard = g
	defaultOnce.Do(func() {})
}

// Graph exposes the guard's exposure graph.
func (g *Guard) Graph() *Graph { return g.graph }

// AllowOnce permits one successful outbound check for host after the user
// approves it from the TUI. It only overrides destination policy; a sensitive
// payload remains blocked by the data-loss boundary.
func (g *Guard) AllowOnce(host string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host != "" {
		g.allowedOnce[host] = true
	}
}

// CheckOutbound is the single policy checkpoint. tool identifies the caller
// (e.g. "web_fetch", "exec(curl)"), host/port/protocol describe the
// destination, and payload is the request body/URL/command line to scan for
// secrets. bestEffort marks events derived from parsing a shell command line
// rather than an in-process network call. It always records an Event; a
// non-nil error means the call must not proceed.
func (g *Guard) CheckOutbound(tool, host string, port int, protocol, payload string, bestEffort bool) (Event, error) {
	return g.CheckOutboundWithMetadata(tool, host, port, protocol, payload, bestEffort, OutboundMetadata{})
}

// CheckOutboundWithMetadata is the correlated form of CheckOutbound. The
// payload is classified and then discarded; only redacted classification
// metadata is persisted in the event/audit record.
func (g *Guard) CheckOutboundWithMetadata(tool, host string, port int, protocol, payload string, bestEffort bool, metadata OutboundMetadata) (Event, error) {
	classification, reason := classifyPayload(payload)
	if metadata.PID == 0 {
		metadata.PID = os.Getpid()
	}
	if metadata.ParentPID == 0 {
		metadata.ParentPID = os.Getppid()
	}
	if metadata.Process == "" && len(os.Args) > 0 {
		metadata.Process = filepath.Base(os.Args[0])
	}

	hostKey := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	g.mu.Lock()
	allowedOnce := g.allowedOnce[hostKey]
	policy := g.policy
	// A one-shot approval is consumed only by a successful check. In particular,
	// a secret-bearing payload must not spend the approval or become allowed.
	if allowedOnce && classification != Blocked {
		delete(g.allowedOnce, hostKey)
	}
	g.mu.Unlock()

	verdict := VerdictAllowed
	var err error
	switch {
	case classification == Blocked:
		verdict = VerdictBlocked
		err = &BlockedError{Host: host, Port: port, Reason: reason}
	case !policy.portAllowed(host, port) && !policy.hostAllowlisted(host) && !allowedOnce:
		verdict = VerdictBlocked
		if reason == "" {
			reason = "non-standard port blocked by policy"
		}
		err = &BlockedError{Host: host, Port: port, Reason: reason}
	case allowedOnce:
		verdict = VerdictAllowedOnce
	}

	event := Event{
		ID:             g.nextEventID(),
		Timestamp:      time.Now().UTC(),
		Direction:      DirectionOutbound,
		Source:         "tool-guard",
		Coverage:       CoverageEnforced,
		SessionID:      metadata.SessionID,
		ToolCallID:     metadata.ToolCallID,
		PID:            metadata.PID,
		ParentPID:      metadata.ParentPID,
		Process:        metadata.Process,
		Cgroup:         metadata.Cgroup,
		Tool:           tool,
		Host:           host,
		Port:           port,
		Protocol:       protocol,
		Bytes:          len(payload),
		Classification: classification,
		Verdict:        verdict,
		Reason:         reason,
		BestEffort:     bestEffort,
		EvidenceRefs:   append([]string(nil), metadata.Evidence...),
	}
	if bestEffort {
		event.Coverage = CoverageBestEffort
	}

	g.record(event, payload)
	return event, err
}

func (g *Guard) nextEventID() string {
	sequence := g.sequence.Add(1)
	return "egress-" + time.Now().UTC().Format("20060102T150405.000000000Z07:00") + "-" + formatSequence(sequence)
}

func formatSequence(value uint64) string {
	// Avoid strconv in the event hot path while keeping IDs bounded and stable.
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if value == 0 {
		return "0"
	}
	var buf [13]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = digits[value%36]
		value /= 36
	}
	return string(buf[i:])
}

// RecordInboundObservation appends descriptive inbound metadata without
// evaluating it as an outbound request. Callers should prefer this only when
// response metadata adds value; response bodies are never accepted here.
func (g *Guard) RecordInboundObservation(tool, host string, port int, protocol string, bytes int, reason string, metadata OutboundMetadata) Event {
	event := Event{
		ID:             g.nextEventID(),
		Timestamp:      time.Now().UTC(),
		Direction:      DirectionInbound,
		Source:         "tool-guard",
		Coverage:       CoverageObserved,
		SessionID:      metadata.SessionID,
		ToolCallID:     metadata.ToolCallID,
		PID:            metadata.PID,
		ParentPID:      metadata.ParentPID,
		Process:        metadata.Process,
		Cgroup:         metadata.Cgroup,
		Tool:           tool,
		Host:           host,
		Port:           port,
		Protocol:       protocol,
		Bytes:          clampEventBytes(bytes),
		Classification: Clean,
		Verdict:        VerdictAllowed,
		Reason:         reason,
		EvidenceRefs:   append([]string(nil), metadata.Evidence...),
	}
	g.record(event, "")
	return event
}

func clampEventBytes(value int) int {
	if value < 0 {
		return 0
	}
	if value > 1<<31-1 {
		return 1<<31 - 1
	}
	return value
}

func (g *Guard) record(event Event, payload string) {
	g.mu.Lock()
	g.events = append(g.events, event)
	if len(g.events) > g.maxEvents {
		g.events = g.events[len(g.events)-g.maxEvents:]
	}
	audit := g.audit
	g.mu.Unlock()

	g.graph.RecordEgress(event, payload)
	if audit != nil {
		_ = audit.Append(event)
	}
	g.recordEnterprise(event)
}

// Events returns a snapshot of the recorded events, most recent first.
func (g *Guard) Events() []Event {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Event, len(g.events))
	for i, e := range g.events {
		out[len(g.events)-1-i] = e
	}
	return out
}

// Counters summarizes the session's egress for the panel header.
type Counters struct {
	RequestsOut int
	BytesOut    int
	Hosts       int
	Blocked     int
}

// Summary computes the current counters.
func (g *Guard) Summary() Counters {
	g.mu.Lock()
	defer g.mu.Unlock()
	hosts := map[string]bool{}
	var c Counters
	for _, e := range g.events {
		if e.Direction == DirectionInbound {
			continue
		}
		c.RequestsOut++
		c.BytesOut += e.Bytes
		hosts[strings.ToLower(e.Host)] = true
		if e.Verdict == VerdictBlocked {
			c.Blocked++
		}
	}
	c.Hosts = len(hosts)
	return c
}

// BlockedError is returned by CheckOutbound when the call must not proceed.
type BlockedError struct {
	Host   string
	Port   int
	Reason string
}

func (e *BlockedError) Error() string {
	return "blocked: " + e.Reason
}
