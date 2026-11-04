package security

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

// Graph is the session's exposure graph: which files' contents entered the
// agent's context, and whether that content later flowed into an outbound
// call. It answers questions like "did anything from .env ever leave?" with
// evidence, not just a host/port log.
type Graph struct {
	mu       sync.Mutex
	contents map[string]trackedFile // path -> bounded normalized fingerprints
	leaked   map[string][]Event
}

type trackedFile struct {
	fingerprints []string
	sessionID    string
	toolCallID   string
	readEventID  string
}

const maxTrackedFileBytes = 8 * 1024

// NewGraph builds an empty exposure graph.
func NewGraph() *Graph {
	return &Graph{
		contents: make(map[string]trackedFile),
		leaked:   make(map[string][]Event),
	}
}

// RecordFileRead notes that path's content entered the agent's context.
func (g *Graph) RecordFileRead(path, content string) {
	g.RecordFileReadWithMetadata(path, content, "", "")
}

// RecordFileReadWithMetadata records only bounded SHA-256 fingerprints of
// meaningful lines/fragments. Raw file content is intentionally not retained
// in the security graph.
func (g *Graph) RecordFileReadWithMetadata(path, content, sessionID, toolCallID string) {
	if path == "" {
		return
	}
	if len(content) > maxTrackedFileBytes {
		content = content[:maxTrackedFileBytes]
	}
	fingerprints := normalizedFingerprints(content)
	if len(fingerprints) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.contents[path] = trackedFile{
		fingerprints: fingerprints,
		sessionID:    sessionID,
		toolCallID:   toolCallID,
		readEventID:  "file-read:" + shortFingerprint(path+"\x00"+sessionID+"\x00"+toolCallID),
	}
}

// RecordEgress checks whether an outbound payload contains content read from
// a tracked file, linking the file to the event if so.
func (g *Graph) RecordEgress(event Event, payload string) {
	if payload == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	outboundFingerprints := normalizedFingerprints(payload)
	if len(outboundFingerprints) == 0 {
		return
	}
	for path, file := range g.contents {
		if len(file.fingerprints) == 0 {
			continue
		}
		if fingerprintsOverlap(file.fingerprints, outboundFingerprints) {
			linked := event
			linked.EvidenceRefs = append(append([]string(nil), event.EvidenceRefs...), file.readEventID)
			g.leaked[path] = append(g.leaked[path], linked)
		}
	}
}

func normalizedFingerprints(value string) []string {
	const maxFingerprints = 128
	seen := make(map[string]struct{}, maxFingerprints)
	result := make([]string, 0, maxFingerprints)
	add := func(fragment string) {
		if len(result) >= maxFingerprints {
			return
		}
		fragment = strings.Join(strings.Fields(strings.TrimSpace(fragment)), " ")
		if len(fragment) < 8 {
			return
		}
		sum := sha256.Sum256([]byte(fragment))
		fingerprint := hex.EncodeToString(sum[:])
		if _, ok := seen[fingerprint]; ok {
			return
		}
		seen[fingerprint] = struct{}{}
		result = append(result, fingerprint)
	}
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		add(line)
		// Outbound payloads often wrap a file assignment in a sentence. Hashing
		// individual meaningful fields preserves exact-match semantics without
		// retaining the field itself.
		for _, field := range strings.Fields(line) {
			add(field)
		}
	}
	return result
}

func shortFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func fingerprintsOverlap(left, right []string) bool {
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		set[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := set[value]; ok {
			return true
		}
	}
	return false
}

// DidLeave reports whether any tracked file whose path contains pathSubstr
// has ever had its content observed in an outbound payload, plus the
// evidencing events.
func (g *Graph) DidLeave(pathSubstr string) (bool, []Event) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var events []Event
	for path, evs := range g.leaked {
		if strings.Contains(path, pathSubstr) {
			events = append(events, evs...)
		}
	}
	return len(events) > 0, events
}
