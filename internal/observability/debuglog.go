package observability

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	debugLogMu   sync.Mutex
	debugLogFile *os.File
	debugLogOpen bool
)

// InitDebugLog opens ~/.atcode/debug.log for append when SONUT_DEBUG=1 (or any
// non-empty value) is set. All subsequent DebugLog calls write to it.
// Safe to call multiple times; opens only once.
func InitDebugLog() {
	if os.Getenv("SONUT_DEBUG") == "" {
		return
	}
	debugLogMu.Lock()
	defer debugLogMu.Unlock()
	if debugLogOpen {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".atcode")
	_ = os.MkdirAll(dir, 0700)
	f, err := os.OpenFile(filepath.Join(dir, "debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	debugLogFile = f
	debugLogOpen = true
}

// DebugLog writes a timestamped line to ~/.atcode/debug.log when SONUT_DEBUG is
// set. It is a no-op when debug logging is not initialised.
func DebugLog(format string, args ...any) {
	debugLogMu.Lock()
	f := debugLogFile
	debugLogMu.Unlock()
	if f == nil {
		return
	}
	line := fmt.Sprintf("[%s] "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
	debugLogMu.Lock()
	_, _ = f.WriteString(line)
	debugLogMu.Unlock()
}

// CloseDebugLog flushes and closes the debug log file. Safe to call even when
// debug logging is not active.
func CloseDebugLog() {
	debugLogMu.Lock()
	defer debugLogMu.Unlock()
	if debugLogFile != nil {
		_ = debugLogFile.Sync()
		_ = debugLogFile.Close()
		debugLogFile = nil
		debugLogOpen = false
	}
}
