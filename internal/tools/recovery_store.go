package tools

// The in-memory FileTracker is intentionally fast, but it is not a durability
// boundary: a rejected multi-minute generation must survive a process restart,
// a provider reconnect, and a tool instance being rebuilt. This small workspace
// local journal is written only on mutation failure and is swept after 24 hours.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	generatedRecoveryDir = ".atcode/recovery"
	generatedRecoveryTTL = 24 * time.Hour
)

type generatedRecoveryArtifact struct {
	Kind          string    `json:"kind"`
	RequestedPath string    `json:"requested_path,omitempty"`
	TargetPath    string    `json:"target_path,omitempty"`
	Content       string    `json:"content"`
	CreatedAt     time.Time `json:"created_at"`
}

func generatedRecoveryDirectory(workspaceRoot string) (string, error) {
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", err
	}
	atcodeDir := filepath.Join(root, ".atcode")
	dir := filepath.Join(atcodeDir, "recovery")
	for _, part := range []string{atcodeDir, dir} {
		if info, statErr := os.Lstat(part); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("recovery directory must not traverse symlink %s", part)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, part := range []string{atcodeDir, dir} {
		info, statErr := os.Lstat(part)
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("recovery directory must not traverse symlink %s", part)
		}
	}
	return dir, nil
}

func normalizeRecoveryPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	return strings.ToLower(filepath.ToSlash(filepath.Clean(value)))
}

func saveGeneratedRecovery(workspaceRoot, kind, requestedPath, targetPath, content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", nil
	}
	dir, err := generatedRecoveryDirectory(workspaceRoot)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	seed := fmt.Sprintf("%s\x00%s\x00%s\x00%d", kind, requestedPath, content, now.UnixNano())
	sum := sha256.Sum256([]byte(seed))
	name := fmt.Sprintf("%d-%s.json", now.UnixNano(), hex.EncodeToString(sum[:])[:16])
	path := filepath.Join(dir, name)
	artifact := generatedRecoveryArtifact{
		Kind:          kind,
		RequestedPath: requestedPath,
		TargetPath:    targetPath,
		Content:       content,
		CreatedAt:     now,
	}
	body, err := json.Marshal(artifact)
	if err != nil {
		return "", err
	}
	if err := atomicWriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	go sweepGeneratedRecovery(workspaceRoot)
	return path, nil
}

func loadLatestGeneratedRecovery(workspaceRoot, kind, requestedPath, targetPath string) (generatedRecoveryArtifact, string, bool) {
	dir, err := generatedRecoveryDirectory(workspaceRoot)
	if err != nil {
		return generatedRecoveryArtifact{}, "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return generatedRecoveryArtifact{}, "", false
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	wantRequested := normalizeRecoveryPath(requestedPath)
	wantTarget := normalizeRecoveryPath(targetPath)
	for _, path := range paths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var artifact generatedRecoveryArtifact
		if json.Unmarshal(body, &artifact) != nil || artifact.Kind != kind || artifact.Content == "" {
			continue
		}
		artifactRequested := normalizeRecoveryPath(artifact.RequestedPath)
		artifactTarget := normalizeRecoveryPath(artifact.TargetPath)
		if wantTarget != "" && artifactTarget == wantTarget {
			return artifact, path, true
		}
		if wantRequested != "" && artifactRequested == wantRequested {
			return artifact, path, true
		}
		// A path rejected before resolution has no trustworthy target. It is
		// recoverable after the model supplies a corrected workspace path.
		if artifact.TargetPath == "" && (wantTarget != "" || wantRequested == "") {
			return artifact, path, true
		}
	}
	return generatedRecoveryArtifact{}, "", false
}

func removeGeneratedRecovery(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

func removeGeneratedRecoveryArtifacts(workspaceRoot, kind, requestedPath, targetPath string) {
	dir, err := generatedRecoveryDirectory(workspaceRoot)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	wantRequested := normalizeRecoveryPath(requestedPath)
	wantTarget := normalizeRecoveryPath(targetPath)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var artifact generatedRecoveryArtifact
		if json.Unmarshal(body, &artifact) != nil || artifact.Kind != kind {
			continue
		}
		if (wantTarget != "" && normalizeRecoveryPath(artifact.TargetPath) == wantTarget) ||
			(wantRequested != "" && normalizeRecoveryPath(artifact.RequestedPath) == wantRequested) ||
			(wantTarget != "" && artifact.TargetPath == "") {
			_ = os.Remove(path)
		}
	}
}

func sweepGeneratedRecovery(workspaceRoot string) {
	dir, err := generatedRecoveryDirectory(workspaceRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-generatedRecoveryTTL)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, statErr := entry.Info()
		if statErr == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
