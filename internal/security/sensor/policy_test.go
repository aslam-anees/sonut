package sensor

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestValidatePolicyAndDecision(t *testing.T) {
	policy, err := ValidatePolicy([]string{"10.0.0.0/8", "192.0.2.7/32"}, []int{22})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Denies(net.ParseIP("10.12.0.4"), 443) {
		t.Fatal("CIDR policy should deny an address inside the configured network")
	}
	if !policy.Denies(net.ParseIP("203.0.113.8"), 22) {
		t.Fatal("port policy should deny the configured port")
	}
	if policy.Denies(net.ParseIP("203.0.113.8"), 443) {
		t.Fatal("unlisted address/port should remain allowed")
	}
}

func TestValidatePolicyBoundsAndIPv4Scope(t *testing.T) {
	if _, err := ValidatePolicy([]string{"2001:db8::/32"}, nil); err == nil {
		t.Fatal("IPv6 policy should be rejected by the current connect4-only helper")
	}
	tooMany := make([]string, maxDeniedCIDRs+1)
	for index := range tooMany {
		tooMany[index] = "10.0.0.0/8"
	}
	if _, err := ValidatePolicy(tooMany, nil); err == nil {
		t.Fatal("policy bound should be enforced")
	}
}

func TestLineageLifecycleIsBounded(t *testing.T) {
	lineage := NewLineage(1)
	lineage.Register(100, "tool-a")
	if got := lineage.Resolve(100, 0); got != "tool-a" {
		t.Fatalf("Resolve(pid) = %q", got)
	}
	lineage.Register(101, "tool-b")
	if got := lineage.Resolve(100, 0); got != "" {
		t.Fatalf("bounded lineage retained an evicted pid: %q", got)
	}
	if got := lineage.Resolve(102, 101); got != "tool-b" {
		t.Fatalf("Resolve(parent) = %q", got)
	}
	lineage.Remove(101)
	if got := lineage.Resolve(101, 0); got != "" {
		t.Fatalf("Remove did not clear lineage: %q", got)
	}
}

func TestKernelEventConversionIsSanitized(t *testing.T) {
	data := make([]byte, 16)
	copy(data[:4], []byte{192, 0, 2, 7})
	binary.BigEndian.PutUint16(data[4:6], 443)
	binary.LittleEndian.PutUint32(data[8:12], 42)
	event, err := parseKernelEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	converted := event.ToSecurityEvent("session-1")
	if converted.Source != "ebpf" || converted.Host != "192.0.2.7" || converted.PID != 42 || converted.SessionID != "session-1" {
		t.Fatalf("unexpected normalized event: %#v", converted)
	}
	if converted.Reason != "ebpf:cgroup/connect4" || len(converted.EvidenceRefs) != 1 {
		t.Fatalf("missing provenance: %#v", converted)
	}
}
