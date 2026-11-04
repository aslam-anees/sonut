package llama

import (
	"testing"
	"time"
)

func TestMarkOwnedPortAndStopClearsOwnership(t *testing.T) {
	// Isolate from any prior test state.
	ownedMu.Lock()
	ownedPorts = map[int]struct{}{}
	ownedMu.Unlock()

	MarkOwnedPort(0) // no-op
	if got := OwnedPorts(); len(got) != 0 {
		t.Fatalf("MarkOwnedPort(0) should not own ports, got %v", got)
	}

	MarkOwnedPort(42611)
	MarkOwnedPort(42611) // idempotent
	ports := OwnedPorts()
	if len(ports) != 1 || ports[0] != 42611 {
		t.Fatalf("OwnedPorts = %v, want [42611]", ports)
	}

	// Verify dispatch without touching a real engine on this machine.
	var killed []int
	stopOwnedServers(func(port int, _ time.Duration) { killed = append(killed, port) })
	if len(killed) != 1 || killed[0] != 42611 {
		t.Fatalf("cleanup targeted %v; want only the explicitly owned port", killed)
	}
	if got := OwnedPorts(); len(got) != 0 {
		t.Fatalf("after StopOwnedServers ownership should be empty, got %v", got)
	}
}

func TestStopOwnedServersLeavesUnownedDefaultPortAlone(t *testing.T) {
	ownedMu.Lock()
	ownedPorts = map[int]struct{}{}
	ownedMu.Unlock()
	stopOwnedServers(func(port int, _ time.Duration) {
		t.Fatalf("cleanup attempted to terminate unowned port %d", port)
	})
}

func TestClearOwnedPort(t *testing.T) {
	ownedMu.Lock()
	ownedPorts = map[int]struct{}{}
	ownedMu.Unlock()

	MarkOwnedPort(9999)
	ClearOwnedPort(9999)
	if got := OwnedPorts(); len(got) != 0 {
		t.Fatalf("ClearOwnedPort should drop ownership, got %v", got)
	}
}

func TestKillLlamaOnPortWithTimeoutNoHang(t *testing.T) {
	// Empty port: must return promptly well under a second.
	start := time.Now()
	KillLlamaOnPortWithTimeout(1, 100*time.Millisecond) // port 1 is never a real llama
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("KillLlamaOnPortWithTimeout took %v, want < 2s on empty port", elapsed)
	}
}
