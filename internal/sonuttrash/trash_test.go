package sonuttrash

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPutAndList(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(src, []byte("sensitive"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Put(root, src); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Source must be gone.
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source file still exists after Put")
	}

	entries := List(root)
	if len(entries) != 1 {
		t.Fatalf("List returned %d entries, want 1", len(entries))
	}
	if entries[0].Name != "secret.txt" {
		t.Errorf("entry name = %q, want secret.txt", entries[0].Name)
	}
	data, err := os.ReadFile(entries[0].SlotPath)
	if err != nil {
		t.Fatalf("read trashed file: %v", err)
	}
	if string(data) != "sensitive" {
		t.Errorf("trashed content = %q, want sensitive", data)
	}
}

func TestSlotCollisionNanoseconds(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		src := filepath.Join(root, "file.txt")
		if err := os.WriteFile(src, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := Put(root, src); err != nil {
			t.Fatalf("Put iteration %d: %v", i, err)
		}
	}
	entries := List(root)
	if len(entries) != 3 {
		t.Errorf("List returned %d entries, want 3 (slot collision?)", len(entries))
	}
}

func TestSweepRemovesExpired(t *testing.T) {
	root := t.TempDir()
	// Manually create an old slot.
	oldSlot := time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02T15-04-05.000000000")
	oldPath := filepath.Join(root, binDir, oldSlot, "old.txt")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	Sweep(root)

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("expired entry was not swept")
	}
}

func TestSweepKeepsRecent(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(src, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Put(root, src); err != nil {
		t.Fatal(err)
	}

	Sweep(root)

	if len(List(root)) != 1 {
		t.Error("recent entry was incorrectly swept")
	}
}

func TestParseSlotTimeLegacy(t *testing.T) {
	// Legacy millisecond format must still parse.
	_, err := parseSlotTime("2024-01-15T10-30-45.123")
	if err != nil {
		t.Errorf("legacy format parse failed: %v", err)
	}
}
