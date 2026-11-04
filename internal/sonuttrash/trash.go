// Package sonuttrash implements a soft-delete TTL bin stored at
// <workspace>/.atcode/ttl/. Files moved there by Put() are kept for 24 hours,
// then removed by Sweep(). The bin is per-workspace so each project has its own
// short-lived undo buffer.
package sonuttrash

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aistatine/sonut/internal/observability"
)

const (
	binDir = ".atcode/ttl"
	ttl    = 24 * time.Hour
)

// Put moves src (an absolute path inside the workspace) into the TTL bin. The
// file is stored as <workspace>/.atcode/ttl/<RFC3339-timestamp>/<basename> so
// multiple deletes of the same filename don't collide.
func Put(workspaceRoot, src string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("trash: stat %s: %w", src, err)
	}
	slot := time.Now().UTC().Format("2006-01-02T15-04-05.000000000")
	dest := filepath.Join(workspaceRoot, binDir, slot, info.Name())
	// 0700: only the owner can read the bin — deleted files may contain secrets.
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return fmt.Errorf("trash: mkdir: %w", err)
	}
	if err := os.Rename(src, dest); err != nil {
		// Cross-device: copy then remove. Remove the destination on copy
		// failure so we don't leave a partial file in the bin.
		if copyErr := copyFile(src, dest); copyErr != nil {
			_ = os.Remove(dest)
			return fmt.Errorf("trash: copy %s: %w", src, copyErr)
		}
		if removeErr := os.Remove(src); removeErr != nil {
			// Undo the copy so the file isn't duplicated.
			_ = os.Remove(dest)
			observability.DebugLog("trash: remove %s after copy failed: %v", src, removeErr)
			return fmt.Errorf("trash: remove %s after copy: %w", src, removeErr)
		}
	}
	return nil
}

// Sweep removes all entries in the TTL bin that are older than 24 hours. It is
// safe to call at any time; errors are silently ignored so a full bin never
// blocks the agent.
func Sweep(workspaceRoot string) {
	root := filepath.Join(workspaceRoot, binDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().UTC().Add(-ttl)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := parseSlotTime(e.Name())
		if err != nil || t.After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// List returns all files currently in the TTL bin with their expiry time.
func List(workspaceRoot string) []Entry {
	root := filepath.Join(workspaceRoot, binDir)
	slots, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, slot := range slots {
		if !slot.IsDir() {
			continue
		}
		t, err := parseSlotTime(slot.Name())
		if err != nil {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, slot.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			out = append(out, Entry{
				Name:      f.Name(),
				SlotPath:  filepath.Join(root, slot.Name(), f.Name()),
				DeletedAt: t,
				ExpiresAt: t.Add(ttl),
			})
		}
	}
	return out
}

// Entry describes one item in the TTL bin.
type Entry struct {
	Name      string
	SlotPath  string
	DeletedAt time.Time
	ExpiresAt time.Time
}

func parseSlotTime(name string) (time.Time, error) {
	t, err := time.Parse("2006-01-02T15-04-05.000000000", name)
	if err != nil {
		// Accept legacy millisecond-precision slots written by older versions.
		t, err = time.Parse("2006-01-02T15-04-05.000", name)
	}
	return t, err
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode())
}
