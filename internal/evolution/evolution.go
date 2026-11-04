// Package evolution provides the small, durable context layer used by @code.
//
// It deliberately stores plain Markdown instead of adding a database or an
// embedding service. The files are human-readable, easy to back up, and cheap
// to inject into a prompt. The agent still treats the model's conversation as
// the source of truth; this package only keeps a bounded, resumable index of
// verified work.
package evolution

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aistatine/sonut/internal/redaction"
)

const (
	defaultContextTokens = 1200
	maxContextTokens     = 12000
	maxMemoryBytes       = 24 << 10
	maxCheckpointBytes   = 16 << 10
	maxNotesBytes        = 12 << 10
	maxRecentWorkLines   = 12
)

var fileMu sync.Mutex

// Paths identifies the durable files for one project and session.
type Paths struct {
	Root       string
	ProjectID  string
	ProjectDir string
	Memory     string
	SessionDir string
	Checkpoint string
	Notes      string
}

// ResolvePaths returns paths without creating anything. The default is
// project-local so the agent can inspect and evolve its own memory without
// needing access outside the workspace. SONUT_EVOLUTION_DIR can point at a
// shared/user-level root when desired.
func ResolvePaths(projectRoot, sessionID string) Paths {
	root := strings.TrimSpace(os.Getenv("SONUT_EVOLUTION_DIR"))
	if root == "" {
		root = filepath.Join(strings.TrimSpace(projectRoot), ".atcode", "memory")
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	projectID := ProjectID(projectRoot)
	projectDir := filepath.Join(root, "projects", projectID)
	sessionDir := filepath.Join(root, "sessions")
	if id := safeSessionID(sessionID); id != "" {
		sessionDir = filepath.Join(sessionDir, id)
	}
	return Paths{
		Root:       root,
		ProjectID:  projectID,
		ProjectDir: projectDir,
		Memory:     filepath.Join(projectDir, "MEMORY.md"),
		SessionDir: sessionDir,
		Checkpoint: filepath.Join(sessionDir, "checkpoint.md"),
		Notes:      filepath.Join(sessionDir, "notes.md"),
	}
}

// ProjectID is a stable, short identifier for a workspace path.
func ProjectID(projectRoot string) string {
	root := strings.TrimSpace(projectRoot)
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:])[:12]
}

// ContextOptions controls how much durable context enters a request.
type ContextOptions struct {
	BudgetTokens int
	IncludeNotes bool
}

// Context is the bounded prompt fragment and its accounting metadata.
type Context struct {
	Text      string
	Tokens    int
	Sources   []string
	Truncated bool
}

// LoadContext reads project memory and the current session checkpoint/notes,
// applying a strict token budget. Missing files are normal and return an empty
// context. Project memory gets the largest share because it survives sessions;
// notes are optional and receive the smallest share because they are noisy.
func LoadContext(projectRoot, sessionID string, options ContextOptions) (Context, error) {
	paths := ResolvePaths(projectRoot, sessionID)
	budget := options.BudgetTokens
	if budget <= 0 {
		budget = contextBudgetFromEnv()
	}
	if budget <= 0 {
		budget = defaultContextTokens
	}
	if budget > maxContextTokens {
		budget = maxContextTokens
	}

	sections := make([]contextSection, 0, 3)
	if text, err := readBounded(paths.Memory, maxMemoryBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Context{}, err
	} else if strings.TrimSpace(text) != "" {
		sections = append(sections, contextSection{name: "project memory", text: text, share: 0.60})
	}
	if text, err := readBounded(paths.Checkpoint, maxCheckpointBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Context{}, err
	} else if strings.TrimSpace(text) != "" {
		sections = append(sections, contextSection{name: "session checkpoint", text: text, share: 0.30})
	}
	if options.IncludeNotes {
		if text, err := readBounded(paths.Notes, maxNotesBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Context{}, err
		} else if strings.TrimSpace(text) != "" {
			sections = append(sections, contextSection{name: "recent notes", text: text, share: 0.10})
		}
	}
	if len(sections) == 0 {
		return Context{}, nil
	}

	// Re-normalize shares when notes are absent so unused budget is not wasted.
	shareTotal := 0.0
	for _, section := range sections {
		shareTotal += section.share
	}
	var out []string
	var sources []string
	truncated := false
	for _, section := range sections {
		sectionBudget := int(float64(budget) * section.share / shareTotal)
		if sectionBudget < 80 {
			sectionBudget = 80
		}
		text, cut := trimToTokens(section.text, sectionBudget)
		if cut {
			truncated = true
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, "## "+section.name+"\n"+text)
		sources = append(sources, section.name)
	}
	text := strings.TrimSpace(strings.Join(out, "\n\n"))
	if estimateTokens(text) > budget {
		var cut bool
		text, cut = trimToTokens(text, budget)
		truncated = truncated || cut
	}
	return Context{Text: text, Tokens: estimateTokens(text), Sources: sources, Truncated: truncated}, nil
}

type contextSection struct {
	name  string
	text  string
	share float64
}

// Ensure creates the memory tree and a stable, editable project-memory file.
func Ensure(projectRoot string) (Paths, error) {
	paths := ResolvePaths(projectRoot, "")
	if err := os.MkdirAll(paths.ProjectDir, 0o700); err != nil {
		return Paths{}, fmt.Errorf("evolution: create project memory: %w", err)
	}
	if _, err := os.Stat(paths.Memory); errors.Is(err, os.ErrNotExist) {
		if err := atomicWrite(paths.Memory, []byte(memoryTemplate()), 0o600); err != nil {
			return Paths{}, err
		}
	} else if err != nil {
		return Paths{}, fmt.Errorf("evolution: inspect project memory: %w", err)
	}
	return paths, nil
}

// RecordRun persists a compact, redacted record of a completed run. It updates
// the current session checkpoint and a short project-history section, keeping
// the files bounded so this never becomes a new source of context bloat.
func RecordRun(projectRoot, sessionID, prompt, outcome string) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	paths, err := Ensure(projectRoot)
	if err != nil {
		return err
	}
	paths = ResolvePaths(projectRoot, sessionID)
	if err := os.MkdirAll(paths.SessionDir, 0o700); err != nil {
		return fmt.Errorf("evolution: create session memory: %w", err)
	}

	prompt = cleanLine(prompt, 260)
	outcome = cleanLine(outcome, 360)
	stamp := time.Now().UTC().Format("2006-01-02 15:04 UTC")
	note := fmt.Sprintf("- %s — %s\n  ↳ %s\n", stamp, prompt, outcome)
	if err := appendBounded(paths.Notes, "# Session notes\n\n", note, maxNotesBytes); err != nil {
		return err
	}
	checkpoint := fmt.Sprintf("# Session checkpoint\n\nLast updated: %s\n\n## Active intent\n%s\n\n## Current state\n%s\n\n## Next action\nContinue from the current session history; verify the workspace before changing files.\n", stamp, prompt, outcome)
	if err := atomicWrite(paths.Checkpoint, []byte(checkpoint), 0o600); err != nil {
		return err
	}
	return appendRecentProjectWork(paths.Memory, stamp, prompt, outcome)
}

// MemoryPath returns the project MEMORY.md path, creating the memory tree when
// needed. It is useful to explicit /dream or /distill workflows.
func MemoryPath(projectRoot string) (string, error) {
	paths, err := Ensure(projectRoot)
	if err != nil {
		return "", err
	}
	return paths.Memory, nil
}

func memoryTemplate() string {
	return "# Project memory\n\n" +
		"## Rules\n\n" +
		"(Add only explicit, durable project rules.)\n\n" +
		"## Architecture decisions\n\n" +
		"(Add decisions with a short reason.)\n\n" +
		"## Discovered durable knowledge\n\n" +
		"(Add verified facts that future sessions need.)\n\n" +
		"## Recent verified work\n\n"
}

func appendRecentProjectWork(path, stamp, prompt, outcome string) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("evolution: read project memory: %w", err)
	}
	text := string(raw)
	if text == "" {
		text = memoryTemplate()
	}
	marker := "## Recent verified work"
	idx := strings.Index(text, marker)
	if idx < 0 {
		text = strings.TrimRight(text, "\n") + "\n\n" + marker + "\n\n"
		idx = strings.Index(text, marker)
	}
	headerEnd := idx + len(marker)
	prefix := text[:headerEnd]
	body := text[headerEnd:]
	body = strings.TrimSpace(body)
	lines := []string{}
	if body != "" {
		lines = strings.Split(body, "\n")
	}
	entry := fmt.Sprintf("- %s — %s ↳ %s", stamp, prompt, outcome)
	lines = append(lines, entry)
	if len(lines) > maxRecentWorkLines {
		lines = lines[len(lines)-maxRecentWorkLines:]
	}
	updated := strings.TrimRight(prefix, "\n") + "\n\n" + strings.Join(lines, "\n") + "\n"
	if len(updated) > maxMemoryBytes {
		updated = trimBytesAtLineBoundary(updated, maxMemoryBytes)
	}
	return atomicWriteLocked(path, []byte(updated), 0o600)
}

func appendBounded(path, header, entry string, maxBytes int) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("evolution: read notes: %w", err)
	}
	text := string(raw)
	if text == "" {
		text = header
	}
	text += entry
	if len(text) > maxBytes {
		// Keep the header and the newest complete lines.
		head := header
		body := strings.TrimPrefix(text, header)
		body = newestLines(body, maxBytes-len(head))
		text = head + body
	}
	return atomicWriteLocked(path, []byte(text), 0o600)
}

func readBounded(path string, maxBytes int) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(raw) > maxBytes {
		raw = []byte(trimBytesAtLineBoundary(string(raw), maxBytes))
	}
	return string(raw), nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	return atomicWriteLocked(path, data, mode)
}

func atomicWriteLocked(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("evolution: create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".evolution-*")
	if err != nil {
		return fmt.Errorf("evolution: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("evolution: chmod temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("evolution: write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("evolution: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("evolution: close temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("evolution: replace %s: %w", path, err)
	}
	return nil
}

func safeSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\\`) {
		return ""
	}
	return id
}

func cleanLine(value string, limit int) string {
	value = redaction.RedactString(strings.Join(strings.Fields(value), " "), redaction.Options{})
	if value == "" {
		return "(none)"
	}
	return cutRunes(value, limit)
}

func cutRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func trimBytesAtLineBoundary(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	cut := value[:limit]
	if idx := strings.LastIndexByte(cut, '\n'); idx > 0 {
		return cut[:idx] + "\n"
	}
	return cut
}

func trimToTokens(value string, budget int) (string, bool) {
	if budget <= 0 || estimateTokens(value) <= budget {
		return value, false
	}
	// Markdown context is line-oriented. Binary search for the largest complete
	// line prefix that fits after adding a compact truncation marker.
	marker := "\n[truncated; read the memory file for the rest]"
	if estimateTokens(marker) > budget {
		limit := budget * 4
		if limit > len(marker) {
			limit = len(marker)
		}
		return marker[:limit], true
	}
	best := marker
	low, high := 0, len(value)
	for low <= high {
		mid := low + (high-low)/2
		prefix := strings.TrimRight(trimBytesAtLineBoundary(value, mid), "\n")
		candidate := prefix + marker
		if estimateTokens(candidate) <= budget {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best, true
}

func newestLines(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	kept := make([]string, 0, len(lines))
	used := 0
	for i := len(lines) - 1; i >= 0; i-- {
		extra := len(lines[i]) + 1 // keep the terminating newline in the bound
		if used+extra > limit {
			break
		}
		kept = append(kept, lines[i])
		used += extra
	}
	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		kept[left], kept[right] = kept[right], kept[left]
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, "\n") + "\n"
}

func estimateTokens(value string) int {
	count := 0
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case ' ', '\t', '\n', '\r', '\f', '\v':
		default:
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return (count + 3) / 4
}

func contextBudgetFromEnv() int {
	value := strings.TrimSpace(os.Getenv("SONUT_EVOLUTION_CONTEXT_TOKENS"))
	if value == "" {
		return defaultContextTokens
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return defaultContextTokens
	}
	return n
}
