package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileTrackerRecordsAndReadsBackVersion(t *testing.T) {
	tracker := NewFileTracker()
	path := filepath.Join(t.TempDir(), "a.txt")
	content := []byte("hello world")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	tracker.Record(path, content, info)
	version, ok := tracker.Version(path)
	if !ok {
		t.Fatal("expected a recorded version")
	}
	if version.Hash != HashContent(content) {
		t.Fatalf("hash = %q, want %q", version.Hash, HashContent(content))
	}
	if version.Size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", version.Size, len(content))
	}
	if version.MTime.IsZero() {
		t.Fatal("expected mtime to be recorded from stat info")
	}
}

func TestCheckConflictAllowsUntrackedPath(t *testing.T) {
	tracker := NewFileTracker()
	// No Record call: a first-touch write has no baseline to conflict against.
	if err := tracker.CheckConflict("/nowhere/x.txt", []byte("anything")); err != nil {
		t.Fatalf("untracked path should not conflict, got %v", err)
	}
}

func TestCheckConflictAllowsMatchingContent(t *testing.T) {
	tracker := NewFileTracker()
	content := []byte("stable content")
	tracker.Record("/repo/x.txt", content, nil)
	if err := tracker.CheckConflict("/repo/x.txt", content); err != nil {
		t.Fatalf("unchanged content should not conflict, got %v", err)
	}
}

func TestCheckConflictBlocksDriftedContent(t *testing.T) {
	tracker := NewFileTracker()
	tracker.Record("/repo/x.txt", []byte("version one"), nil)
	if err := tracker.CheckConflict("/repo/x.txt", []byte("version two — changed underneath us")); err != ErrFileChangedOnDisk {
		t.Fatalf("drifted content should report ErrFileChangedOnDisk, got %v", err)
	}
}

func TestForgetClearsBaselineSoNextWriteIsAllowed(t *testing.T) {
	tracker := NewFileTracker()
	tracker.Record("/repo/x.txt", []byte("version one"), nil)
	tracker.Forget("/repo/x.txt")
	if _, ok := tracker.Version("/repo/x.txt"); ok {
		t.Fatal("Forget should drop the recorded version")
	}
	if err := tracker.CheckConflict("/repo/x.txt", []byte("version two")); err != nil {
		t.Fatalf("forgotten path should behave as untracked, got %v", err)
	}
}

func TestUnchangedSinceReadMatchesSizeMtimeAndRange(t *testing.T) {
	tracker := NewFileTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	content := []byte("hello unchanged")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tracker.RecordRead(path, content, info, 1, 0, 120)
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !tracker.UnchangedSinceRead(path, info2, 1, 0, 120) {
		t.Fatal("same size/mtime and range should be unchanged")
	}
	if tracker.UnchangedSinceRead(path, info2, 1, 0, 200) {
		t.Fatal("different max_lines must not short-circuit")
	}
	if tracker.UnchangedSinceRead(path, info2, 2, 0, 120) {
		t.Fatal("different start_line must not short-circuit")
	}
}

func TestUnchangedSinceReadClearedByWriteRecord(t *testing.T) {
	tracker := NewFileTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "b.txt")
	content := []byte("v1")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	tracker.RecordRead(path, content, info, 1, 0, 120)
	// Simulate edit/write_file Record — clears cacheable flag.
	tracker.Record(path, []byte("v2"), info)
	if tracker.UnchangedSinceRead(path, info, 1, 0, 120) {
		t.Fatal("write Record must invalidate read short-circuit")
	}
}

func TestUnchangedSinceReadDetectsMtimeDrift(t *testing.T) {
	tracker := NewFileTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	tracker.RecordRead(path, []byte("v1"), info, 1, 0, 120)
	// Touch file so mtime moves.
	if err := os.WriteFile(path, []byte("v1-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(path)
	if tracker.UnchangedSinceRead(path, info2, 1, 0, 120) {
		t.Fatal("mtime/size drift must not short-circuit")
	}
}

func TestRecordOverwritesBaselineWithNewContent(t *testing.T) {
	tracker := NewFileTracker()
	tracker.Record("/repo/x.txt", []byte("old"), nil)
	tracker.Record("/repo/x.txt", []byte("new"), nil)
	// After re-recording, the new content is the baseline and matches.
	if err := tracker.CheckConflict("/repo/x.txt", []byte("new")); err != nil {
		t.Fatalf("re-recorded content should be the new baseline, got %v", err)
	}
	if err := tracker.CheckConflict("/repo/x.txt", []byte("old")); err != ErrFileChangedOnDisk {
		t.Fatal("the superseded content should now conflict")
	}
}

func TestNilFileTrackerIsANoop(t *testing.T) {
	var tracker *FileTracker
	tracker.Record("/repo/x.txt", []byte("x"), nil) // must not panic
	tracker.Forget("/repo/x.txt")
	if _, ok := tracker.Version("/repo/x.txt"); ok {
		t.Fatal("nil tracker should report no version")
	}
	if err := tracker.CheckConflict("/repo/x.txt", []byte("x")); err != nil {
		t.Fatalf("nil tracker should never conflict, got %v", err)
	}
}

func TestHashContentIsStableAndDistinguishing(t *testing.T) {
	// Store results of separate calls before comparing so the stability check is
	// not a same-expression comparison (staticcheck SA4000).
	first := HashContent([]byte("a"))
	second := HashContent([]byte("a"))
	if first != second {
		t.Fatal("hash must be stable for identical content")
	}
	if first == HashContent([]byte("b")) {
		t.Fatal("hash must differ for different content")
	}
}
