package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteFile commits a complete file body through a sibling temporary
// file. A failed write therefore leaves the previous target intact; callers
// can safely retry with the same edit context.
func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sonut-write-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temporary file into place: %w", err)
	}
	removeTemp = false
	// Persist the directory entry as well as the file bytes. Without this final
	// sync, a sudden process or machine failure immediately after Rename can
	// leave the old name restored even though the generated body was fsynced.
	// Some platforms do not allow opening directories; the rename has already
	// succeeded there, so keep the successful write result and rely on the OS.
	_ = syncDirectory(dir)
	return nil
}

func syncDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
