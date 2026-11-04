package provideronboarding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/providerhealth"
)

func TestDetectLocalRuntimesProbesInParallelButReturnsCandidateOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", request.URL.Path)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": []map[string]string{{"id": "local-coder"}}})
	}))
	defer server.Close()

	got := DetectLocalRuntimes(context.Background(), LocalDetectOptions{
		Candidates: []LocalRuntimeCandidate{
			{CatalogID: "lmstudio", Name: "second", BaseURL: server.URL + "/v1"},
			{CatalogID: "llamacpp", Name: "first", BaseURL: server.URL + "/v1"},
		},
	})
	if len(got) != 2 || got[0].Name != "second" || got[1].Name != "first" {
		t.Fatalf("detected runtimes = %#v, want candidate order", got)
	}
	if got[0].Models[0] != "local-coder" {
		t.Fatalf("detected models = %#v", got[0].Models)
	}
	if got[0].SetupAction().Command == "" {
		t.Fatal("SetupAction returned an empty adopt command")
	}
}

func TestProviderStateActionsExposeSafeNextSteps(t *testing.T) {
	actions := (ProviderState{Profile: config.ProviderProfile{
		Name:         "work",
		CatalogID:    "openai",
		ProviderKind: config.ProviderKindOpenAI,
		APIKeyEnv:    "OPENAI_API_KEY",
	}}).Actions()
	if len(actions) != 3 {
		t.Fatalf("actions = %#v, want use/set/check", actions)
	}
	if actions[0].Label != "Use provider" || actions[1].Label != "Set API key" || actions[2].Label != "Check provider" {
		t.Fatalf("actions = %#v", actions)
	}
	if actions[1].Command != "" || actions[1].Detail == "" {
		t.Fatalf("set-key action should carry guidance without a secret: %#v", actions[1])
	}
}

func TestClassifySetupProbePreservesActionableCategory(t *testing.T) {
	err, failed := ClassifySetupProbe(providerhealth.Result{
		Status: providerhealth.StatusFail,
		Checks: []providerhealth.Check{{
			ID:       "provider.connectivity",
			Status:   providerhealth.StatusFail,
			Category: providerhealth.CategoryAuth,
			Message:  "credentials rejected",
		}},
	})
	if !failed || err == nil || err.Class != "authentication failed" {
		t.Fatalf("classification = (%v, %v), want authentication failure", err, failed)
	}
}
