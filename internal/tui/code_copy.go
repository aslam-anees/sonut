package tui

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
)

// codeBlockIDPrefix / Suffix is a private OSC sequence used to embed a code-block
// registry key in the rendered header line without affecting layout: terminals
// ignore unknown OSC codes, and ansiSequenceEnd treats OSC sequences as zero-width
// (it scans until BEL or ST), so lipgloss width math stays correct.
// We use OSC (ESC ]) rather than CSI (ESC [) because CSI terminates at the first
// byte in 0x40–0x7E — which includes all letters — so a hex hash would split the
// sequence mid-way and leak literal text into the display.
const codeBlockIDPrefix = "\x1b]50;CPCOPY="
const codeBlockIDSuffix = "\a"

var codeBlockRegistryMu sync.RWMutex
var codeBlockRegistry = map[string]string{} // id → raw code content

// registerCodeBlock stores code content and returns a short hex ID.
func registerCodeBlock(lines []string) string {
	content := strings.Join(lines, "\n")
	sum := sha256.Sum256([]byte(content))
	id := fmt.Sprintf("%x", sum[:4]) // 8 hex chars, low collision risk per session
	codeBlockRegistryMu.Lock()
	codeBlockRegistry[id] = content
	codeBlockRegistryMu.Unlock()
	return id
}

// lookupCodeBlock retrieves registered code by ID.
func lookupCodeBlock(id string) (string, bool) {
	codeBlockRegistryMu.RLock()
	v, ok := codeBlockRegistry[id]
	codeBlockRegistryMu.RUnlock()
	return v, ok
}

// codeBlockIDTag returns the zero-width hidden marker embedding id.
func codeBlockIDTag(id string) string {
	return codeBlockIDPrefix + id + codeBlockIDSuffix
}

// extractCodeBlockID scans a rendered line for the hidden copy-button ID marker
// and returns the id string, or "" if none is found.
func extractCodeBlockID(line string) string {
	idx := strings.Index(line, codeBlockIDPrefix)
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(codeBlockIDPrefix):]
	// Suffix is BEL (\a = 0x07); search for it directly.
	end := strings.IndexByte(rest, '\a')
	if end < 0 {
		return ""
	}
	id := rest[:end]
	if len(id) == 0 || len(id) > 16 {
		return ""
	}
	// Validate: only lowercase hex chars
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return ""
		}
	}
	return id
}
