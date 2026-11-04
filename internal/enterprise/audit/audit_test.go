package audit

import (
	"context"
	"testing"
)

func TestWriteAndListRoundTrip(t *testing.T) {
	svc := NewService(nil, nil)
	ctx := context.Background()

	if err := svc.Write(ctx, "req-1", 7, "user.login", "session", "sess-1", "1.2.3.4", "curl/8", map[string]string{"method": "password"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := svc.Write(ctx, "req-2", 7, "user.logout", "session", "sess-1", "1.2.3.4", "curl/8", nil); err != nil {
		t.Fatalf("write: %v", err)
	}

	logs, total, err := svc.List(ctx, 1, 20, ListFilter{ActorUserID: 7})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(logs) != 2 {
		t.Fatalf("expected 2 logs, got total=%d len=%d", total, len(logs))
	}
	// Newest first.
	if logs[0].Action != "user.logout" || logs[1].Action != "user.login" {
		t.Fatalf("unexpected order: %+v", logs)
	}
	if logs[1].DetailJSON == "" {
		t.Fatalf("expected detail JSON to be recorded")
	}
}

func TestListFiltersByAction(t *testing.T) {
	svc := NewService(nil, nil)
	ctx := context.Background()
	_ = svc.Write(ctx, "r1", 1, "policy.deny", "resource", "id-1", "", "", nil)
	_ = svc.Write(ctx, "r2", 1, "policy.allow", "resource", "id-2", "", "", nil)

	logs, total, err := svc.List(ctx, 1, 20, ListFilter{Action: "policy.deny"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(logs) != 1 || logs[0].Action != "policy.deny" {
		t.Fatalf("unexpected filtered result: total=%d logs=%+v", total, logs)
	}
}

func TestSinkReceivesEveryWrite(t *testing.T) {
	var received []Log
	svc := NewService(nil, func(l Log) { received = append(received, l) })
	ctx := context.Background()
	if err := svc.Write(ctx, "r1", 1, "audit.test", "resource", "id", "", "", nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(received) != 1 || received[0].Action != "audit.test" {
		t.Fatalf("expected sink to observe the write, got %+v", received)
	}
}
