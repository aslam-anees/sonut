package observability

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Always-on structured performance log at ~/.atcode/perf.log.
//
// Purpose: measure end-to-end agent latency (launch, prefill, TTFT, decode,
// tools) so we can optimize without guessing. Opt-out with SONUT_PERF=0.
// Every line is one event: key=value fields, machine-parseable.

var (
	perfLogMu   sync.Mutex
	perfLogFile *os.File
	perfLogInit sync.Once
	perfLogOff  bool
)

// InitPerfLog opens ~/.atcode/perf.log for append. Safe to call multiple times.
// Disabled when SONUT_PERF=0 (or "false" / "off" / "no").
func InitPerfLog() {
	perfLogInit.Do(func() {
		v := strings.ToLower(strings.TrimSpace(os.Getenv("SONUT_PERF")))
		if v == "0" || v == "false" || v == "off" || v == "no" {
			perfLogOff = true
			return
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir := filepath.Join(home, ".atcode")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		f, err := os.OpenFile(filepath.Join(dir, "perf.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		perfLogFile = f
		// Session banner so successive runs are easy to split.
		_, _ = f.WriteString(fmt.Sprintf(
			"%s event=session_start pid=%d\n",
			time.Now().UTC().Format(time.RFC3339Nano),
			os.Getpid(),
		))
	})
}

// PerfLog writes one structured event line. keysvals must be alternating
// key, value (values are fmt.Sprint'd). No-op when disabled or not init'd.
//
// Example:
//
//	PerfLog("inference", "ttft_ms", 1200, "prompt_tok", 4500, "cache_tok", 4200)
func PerfLog(event string, keysvals ...any) {
	if perfLogOff {
		return
	}
	InitPerfLog()
	perfLogMu.Lock()
	f := perfLogFile
	perfLogMu.Unlock()
	if f == nil {
		return
	}
	var b strings.Builder
	b.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
	b.WriteString(" event=")
	b.WriteString(sanitizePerfField(event))
	for i := 0; i+1 < len(keysvals); i += 2 {
		key := fmt.Sprint(keysvals[i])
		val := fmt.Sprint(keysvals[i+1])
		b.WriteByte(' ')
		b.WriteString(sanitizePerfField(key))
		b.WriteByte('=')
		b.WriteString(sanitizePerfValue(val))
	}
	b.WriteByte('\n')
	perfLogMu.Lock()
	_, _ = f.WriteString(b.String())
	perfLogMu.Unlock()
}

// ClosePerfLog flushes and closes the perf log. Safe when not open.
func ClosePerfLog() {
	perfLogMu.Lock()
	defer perfLogMu.Unlock()
	if perfLogFile != nil {
		_ = perfLogFile.Sync()
		_ = perfLogFile.Close()
		perfLogFile = nil
	}
}

func sanitizePerfField(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		if r == '=' || r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return '_'
		}
		return r
	}, s)
}

func sanitizePerfValue(s string) string {
	// Keep single-token values; quote only if whitespace/special present.
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\r=\"'") {
		s = strings.ReplaceAll(s, `"`, `'`)
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, "\r", "")
		return `"` + s + `"`
	}
	return s
}
