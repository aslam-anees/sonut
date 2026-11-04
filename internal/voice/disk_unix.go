//go:build !windows

package voice

import (
	"fmt"
	"os"
	"syscall"
)

func freeDisk(root string, required int64) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return fmt.Errorf("voice disk-space check: %w", err)
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	if available < uint64(required) {
		return fmt.Errorf("voice pack needs %d bytes but only %d bytes are available", required, available)
	}
	return nil
}
