package llama

import (
	"sync"
	"time"
)

// Session-owned atcode engine processes. When this process launches (or adopts)
// llama-server on InternalLlamaPort for the interactive shell, we mark the port
// so a clean exit reaps it instead of leaving a multi-GB orphan.
//
// Ownership is process-local: a second atcode instance does not inherit this map.
// Only ports we actually used for the engine this session are stopped on exit —
// never an arbitrary system service on a random port.
var (
	ownedMu    sync.Mutex
	ownedPorts = map[int]struct{}{}
)

// MarkOwnedPort records that this process is responsible for the llama-server
// listening on port (typically InternalLlamaPort). Safe to call repeatedly.
func MarkOwnedPort(port int) {
	if port <= 0 {
		return
	}
	ownedMu.Lock()
	ownedPorts[port] = struct{}{}
	ownedMu.Unlock()
}

// ClearOwnedPort drops ownership without killing (e.g. after a deliberate
// hand-off). Rare; most paths use StopOwnedServers on exit.
func ClearOwnedPort(port int) {
	ownedMu.Lock()
	delete(ownedPorts, port)
	ownedMu.Unlock()
}

// OwnedPorts returns a snapshot of ports this process currently owns.
func OwnedPorts() []int {
	ownedMu.Lock()
	defer ownedMu.Unlock()
	out := make([]int, 0, len(ownedPorts))
	for p := range ownedPorts {
		out = append(out, p)
	}
	return out
}

// StopOwnedServers terminates every llama-server this process marked as owned.
// Uses a short grace period so TUI exit stays snappy; still SIGINT then SIGKILL.
// Idempotent and safe if nothing is owned or already dead.
//
// Unowned ports belong to other sessions. A best-effort cleanup must never
// terminate an unrelated live engine merely because it uses the default port.
func StopOwnedServers() {
	stopOwnedServers(KillLlamaOnPortWithTimeout)
}

func stopOwnedServers(kill func(int, time.Duration)) {
	ownedMu.Lock()
	ports := make([]int, 0, len(ownedPorts))
	for p := range ownedPorts {
		if p <= 0 {
			continue
		}
		ports = append(ports, p)
	}
	// Clear first so concurrent re-entries don't double-kill forever.
	ownedPorts = map[int]struct{}{}
	ownedMu.Unlock()

	for _, port := range ports {
		kill(port, 1500*time.Millisecond)
	}
}
