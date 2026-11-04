package agent

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Intent layer: nested AGENTS.md discovery during a run.
//
// buildSystemPrompt injects the guideline chain from the git root to the CWD,
// but a run that later touches files in an unrelated sub-tree (internal/tui/,
// pkg/auth/, …) never sees that sub-tree's AGENTS.md — the intent, constraints,
// and pitfalls its authors wrote down. The intent layer watches the paths each
// tool call touches and, the first time a directory with an unseen guideline
// file is entered, surfaces that file to the model as a one-shot note.

// intentLayerMaxFileBytes caps a single surfaced guideline file.
const intentLayerMaxFileBytes = 2 << 10 // 2 KiB

// intentLayerMaxRunBytes caps the total guideline bytes surfaced per run so a
// repo salted with AGENTS.md files can't flood the context.
const intentLayerMaxRunBytes = 6 << 10 // 6 KiB

// intentPathArgKeys are the tool-argument names that carry filesystem paths.
var intentPathArgKeys = []string{
	"path", "file_path", "filepath", "file", "filename", "dir", "directory",
	"target_file", "workdir", "cwd",
}

// intentLayerState tracks which directories were already checked this run and
// how much guideline budget remains. One instance per Run.
type intentLayerState struct {
	root    string          // absolute workspace root ("" disables the layer)
	seen    map[string]bool // directories already checked (hit or miss)
	remains int             // remaining byte budget for this run
}

// newIntentLayerState builds the per-run state. The directories whose
// guidelines buildSystemPrompt already injected (git root → cwd chain) are
// pre-marked seen so they are never surfaced twice.
func newIntentLayerState(cwd string) *intentLayerState {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return &intentLayerState{}
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return &intentLayerState{}
	}
	state := &intentLayerState{
		root:    abs,
		seen:    make(map[string]bool),
		remains: intentLayerMaxRunBytes,
	}
	for _, dir := range projectGuidelineDirs(abs, findProjectGitRoot(abs)) {
		state.seen[dir] = true
	}
	return state
}

// observeToolCall extracts every path-like argument from a tool call, walks
// each path's directory chain from the workspace root down to the path's own
// directory, and returns a note containing any unseen guideline files found
// along the way. Returns "" when nothing new was discovered (the common case,
// which keeps the conversation byte-identical to pre-intent-layer behavior).
func (s *intentLayerState) observeToolCall(args map[string]any) string {
	if s == nil || s.root == "" || s.remains <= 0 || len(args) == 0 {
		return ""
	}
	var b strings.Builder
	for _, key := range intentPathArgKeys {
		raw, ok := args[key].(string)
		if !ok {
			continue
		}
		for _, dir := range s.dirsToCheck(raw) {
			if s.remains <= 0 {
				break
			}
			s.seen[dir] = true
			match := findProjectContextFile(dir)
			if match == "" {
				continue
			}
			data, err := os.ReadFile(match)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			limit := intentLayerMaxFileBytes
			if s.remains < limit {
				limit = s.remains
			}
			if len(content) > limit {
				cut := limit
				for cut > 0 && !utf8.RuneStart(content[cut]) {
					cut--
				}
				content = content[:cut] + "\n… (truncated; read the file for the rest)"
			}
			s.remains -= len(content)
			label := match
			if rel, err := filepath.Rel(s.root, match); err == nil && !strings.HasPrefix(rel, "..") {
				label = rel
			}
			b.WriteString("## Directory guidelines (" + label + ")\n\n" + content + "\n\n")
		}
	}
	note := strings.TrimSpace(b.String())
	if note == "" {
		return ""
	}
	return "You are now working in a directory with its own guidelines. Follow them for work under that directory:\n\n" + note
}

// dirsToCheck resolves a raw path argument to the unseen directories between
// the workspace root (exclusive — the root chain is pre-seen) and the path's
// directory (inclusive), in root-to-leaf order. Paths outside the workspace
// resolve to nil, as do already-fully-seen chains.
func (s *intentLayerState) dirsToCheck(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	p := raw
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.root, p)
	}
	p = filepath.Clean(p)
	// Treat the path as a file and start from its parent; if it is actually a
	// directory, include it too. A stat keeps this exact without guessing.
	dir := filepath.Dir(p)
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		dir = p
	}
	rel, err := filepath.Rel(s.root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil
	}
	var dirs []string
	cur := s.root
	if rel != "." {
		for _, seg := range strings.Split(rel, string(filepath.Separator)) {
			if seg == "" || seg == "." {
				continue
			}
			cur = filepath.Join(cur, seg)
			if !s.seen[cur] {
				dirs = append(dirs, cur)
			}
		}
	}
	return dirs
}
