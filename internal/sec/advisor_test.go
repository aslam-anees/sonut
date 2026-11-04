package sec

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

type advisorTestProvider struct {
	mu      sync.Mutex
	request sonutruntime.CompletionRequest
}

func (provider *advisorTestProvider) StreamCompletion(_ context.Context, request sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	provider.mu.Lock()
	provider.request = request
	provider.mu.Unlock()
	events := make(chan sonutruntime.StreamEvent, 2)
	events <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "Rotate the exposed credential first [finding-1]."}
	events <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
	close(events)
	return events, nil
}

func TestAdvisorIsInProcessGroundedAndRedacted(t *testing.T) {
	provider := &advisorTestProvider{}
	report := Report{
		Version: ReportVersion,
		Score:   61,
		Grade:   "D",
		Findings: []Finding{{
			ID:           "finding-1",
			Title:        "Credential exposed",
			Description:  "A credential is committed.",
			Severity:     SeverityCritical,
			RiskScore:    95,
			Path:         ".env",
			Evidence:     "TOKEN=[REDACTED:token]",
			Confidence:   "high",
			EvidenceKind: "workspace-file",
			Remediation:  "Rotate it.",
		}},
	}
	advisor, err := NewAdvisor(report, provider)
	if err != nil {
		t.Fatalf("NewAdvisor: %v", err)
	}
	answer, err := advisor.AskWithContext(context.Background(), "What should I fix first?", "signals=none observed\ntool_calls=2")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !strings.Contains(answer, "[finding-1]") {
		t.Fatalf("answer did not cite finding: %q", answer)
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	serialized, _ := json.Marshal(provider.request.Messages)
	if strings.Contains(string(serialized), "TOKEN=real-secret") {
		t.Fatal("advisor request leaked a raw credential")
	}
	for _, want := range []string{"[REDACTED:token]", "finding-1", `confidence`, `high`} {
		if !strings.Contains(string(serialized), want) {
			t.Fatalf("advisor was not grounded in %q: %s", want, serialized)
		}
	}
	if !strings.Contains(string(serialized), "SESSION TRIAGE") || !strings.Contains(string(serialized), "tool_calls=2") {
		t.Fatalf("advisor did not receive the bounded triage context: %s", serialized)
	}
}

func TestAdvisorValidatesQuestionsAndRequiresProvider(t *testing.T) {
	if _, err := NewAdvisor(Report{}, nil); err == nil {
		t.Fatal("NewAdvisor should require a provider")
	}
	advisor, err := NewAdvisor(Report{}, &advisorTestProvider{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advisor.Ask(context.Background(), "  "); err == nil {
		t.Fatal("empty question should be rejected")
	}
}
