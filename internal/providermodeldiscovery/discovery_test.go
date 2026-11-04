package providermodeldiscovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/providercatalog"
)

func TestDiscoverCatalogParsesOpenAICompatibleModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer discovery-secret" {
			t.Fatalf("missing discovery authorization header")
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "zeta", "context_window": 32768, "supports_tools": true},
				{"id": "alpha", "description": "fast", "reasoning": true},
				{"id": "zeta"},
			},
		})
	}))
	defer server.Close()

	models, err := DiscoverCatalog(context.Background(), providercatalog.Descriptor{
		ID:        "custom",
		Transport: providercatalog.TransportOpenAICompatible,
	}, config.ProviderProfile{
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      server.URL + "/v1",
		APIKey:       "discovery-secret",
	}, Options{})
	if err != nil {
		t.Fatalf("DiscoverCatalog() error = %v", err)
	}
	if len(models) != 2 || models[0].ID != "alpha" || models[1].ID != "zeta" {
		t.Fatalf("models = %#v, want sorted unique models", models)
	}
	if !models[1].ToolCall || models[1].ContextWindow != 32768 || !models[0].Reasoning {
		t.Fatalf("model metadata was not parsed: %#v", models)
	}
}

func TestDiscoverOllamaModelsAndContextWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(writer).Encode(map[string]any{"models": []map[string]any{
				{"name": "qwen2.5-coder:7b", "details": map[string]any{"family": "qwen2"}},
			}})
		case "/api/show":
			if request.Method != http.MethodPost {
				t.Fatalf("Ollama show method = %s, want POST", request.Method)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"model_info": map[string]any{"general.context_length": 32768},
			})
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	models, err := DiscoverCatalog(context.Background(), providercatalog.Descriptor{ID: "ollama"}, config.ProviderProfile{BaseURL: server.URL + "/v1"}, Options{})
	if err != nil {
		t.Fatalf("DiscoverCatalog() error = %v", err)
	}
	if len(models) != 1 || models[0].ID != "qwen2.5-coder:7b" || models[0].Source != "ollama" {
		t.Fatalf("Ollama models = %#v", models)
	}
	window, err := DiscoverOllamaContextWindow(context.Background(), server.URL+"/v1", models[0].ID, Options{})
	if err != nil || window != 32768 {
		t.Fatalf("Ollama context window = %d, error=%v; want 32768", window, err)
	}
}
