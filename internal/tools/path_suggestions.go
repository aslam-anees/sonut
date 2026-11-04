package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxPathSuggestionEntries = 2048

// pathNotFoundSuggestion returns one existing in-workspace path that is likely
// to be what the model meant. It runs only after a failed read. The direct
// joined-component check handles the common screenshot-task mistake:
//
//	assets/features/bg.svg -> assets/features bg.svg
//
// A bounded workspace scan then handles dropped folders and close basenames.
// This is adapted from Grok Build's path-not-found enrichment: return a useful
// correction in the first error so the agent does not spend another model turn
// listing and retrying the same invented path.
func pathNotFoundSuggestion(workspaceRoot, requestedPath string) string {
	root := normalizeWorkspaceRoot(workspaceRoot)
	if root == "" || strings.TrimSpace(requestedPath) == "" {
		return ""
	}
	requested := filepath.Clean(filepath.FromSlash(requestedPath))
	if filepath.IsAbs(requested) {
		if rel, err := filepath.Rel(root, requested); err == nil {
			requested = rel
		}
	}

	parts := splitCleanPath(requested)
	for joinAt := len(parts) - 2; joinAt >= 0; joinAt-- {
		candidateParts := append([]string{}, parts[:joinAt]...)
		candidateParts = append(candidateParts, strings.Join(parts[joinAt:], " "))
		candidate := filepath.Join(append([]string{root}, candidateParts...)...)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return relativeSuggestion(root, candidate)
		}
	}

	queryBase := strings.ToLower(filepath.Base(requested))
	queryStem := strings.ToLower(strings.TrimSuffix(queryBase, filepath.Ext(queryBase)))
	queryPath := comparablePath(requested)
	type match struct {
		path  string
		score int
	}
	matches := make([]match, 0, 4)
	seen := 0
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && shouldSkipDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		seen++
		if seen > maxPathSuggestionEntries {
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		base := strings.ToLower(entry.Name())
		stem := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
		score := 100
		switch {
		case base == queryBase:
			score = 0
		case queryStem != "" && (strings.Contains(stem, queryStem) || strings.Contains(queryStem, stem)):
			score = 10
		case queryPath != "" && strings.Contains(comparablePath(rel), queryPath):
			score = 20
		default:
			return nil
		}
		matches = append(matches, match{path: rel, score: score + len(filepath.ToSlash(rel))/80})
		return nil
	})
	if len(matches) == 0 {
		return ""
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		return len(matches[i].path) < len(matches[j].path)
	})
	return filepath.ToSlash(matches[0].path)
}

func splitCleanPath(path string) []string {
	path = filepath.ToSlash(filepath.Clean(path))
	path = strings.TrimPrefix(path, "./")
	raw := strings.Split(path, "/")
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		if part != "" && part != "." && part != ".." {
			out = append(out, part)
		}
	}
	return out
}

func comparablePath(path string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.ToSlash(path)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func relativeSuggestion(root, candidate string) string {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return filepath.ToSlash(candidate)
	}
	return filepath.ToSlash(rel)
}
