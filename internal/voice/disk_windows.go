//go:build windows

package voice

import (
	"fmt"
	"golang.org/x/sys/windows"
)

func freeDisk(root string, required int64) error {
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return fmt.Errorf("voice disk-space check: %w", err)
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, &total, &free); err != nil {
		return fmt.Errorf("voice disk-space check: %w", err)
	}
	if available < uint64(required) {
		return fmt.Errorf("voice pack needs %d bytes but only %d bytes are available", required, available)
	}
	return nil
}
