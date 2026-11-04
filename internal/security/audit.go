package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditLog is an append-only JSONL writer for egress events, rotated daily
// under dir (e.g. .atcode/audit/2026-07-05.jsonl), surviving across sessions.
type AuditLog struct {
	mu  sync.Mutex
	dir string
}

// NewAuditLog builds an AuditLog rooted at dir. The directory is created
// lazily on the first Append call.
func NewAuditLog(dir string) *AuditLog {
	return &AuditLog{dir: dir}
}

func (a *AuditLog) pathFor(t time.Time) string {
	return filepath.Join(a.dir, t.Format("2006-01-02")+".jsonl")
}

// Append writes one event as a JSON line to today's audit file.
func (a *AuditLog) Append(event Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(a.pathFor(event.Timestamp), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}
