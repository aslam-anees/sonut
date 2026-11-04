package security

import (
	"context"
	"testing"

	"github.com/aistatine/sonut/internal/enterprise/audit"
)

func TestAttachEnterpriseRecordsAuditTrail(t *testing.T) {
	guard := NewGuard(DefaultPolicy(), "")
	layer := NewEnterpriseLayer(EnterpriseOptions{})
	guard.AttachEnterprise(layer)

	if _, err := guard.CheckOutbound("web_fetch", "example.com", 443, "https", "hello", false); err != nil {
		t.Fatalf("unexpected block: %v", err)
	}

	logs, total, err := layer.Audit.List(context.Background(), 1, 20, audit.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("expected the CheckOutbound call to produce one audit record, got total=%d len=%d", total, len(logs))
	}
	if logs[0].Action != "egress.allowed" || logs[0].Resource != "network" {
		t.Fatalf("unexpected audit record: %+v", logs[0])
	}
}

func TestGuardWithoutEnterpriseLayerUnaffected(t *testing.T) {
	guard := NewGuard(DefaultPolicy(), "")
	if _, err := guard.CheckOutbound("web_fetch", "example.com", 443, "https", "hello", false); err != nil {
		t.Fatalf("guard without enterprise layer should behave exactly as before: %v", err)
	}
}
