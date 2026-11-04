package engineclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

func TestStreamCompletionTranslatesEngineFrames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/inference" {
			t.Fatal("unexpected path", r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["lane"] != "coding" {
			t.Fatalf("lane = %#v", request["lane"])
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte("{\"type\":\"accepted\"}\n" +
			"{\"type\":\"delta\",\"payload\":{\"text\":\"hello\"}}\n" +
			"{\"type\":\"tool-call-start\",\"payload\":{\"id\":\"call-1\",\"name\":\"read_file\"}}\n" +
			"{\"type\":\"tool-call-delta\",\"payload\":{\"id\":\"call-1\",\"arguments\":\"{}\"}}\n" +
			"{\"type\":\"completed\"}\n"))
	}))
	defer server.Close()

	client := NewWithHTTPClient(server.URL, server.Client())
	stream, err := client.StreamCompletion(context.Background(), sonutruntime.CompletionRequest{
		Messages: []sonutruntime.Message{{Role: sonutruntime.MessageRoleUser, Content: "hi"}},
		Tools:    []sonutruntime.ToolDefinition{{Name: "read_file", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []sonutruntime.StreamEvent
	for event := range stream {
		got = append(got, event)
	}
	if len(got) != 4 || got[0].Type != sonutruntime.StreamEventText || got[0].Content != "hello" {
		t.Fatalf("events = %#v", got)
	}
	if got[1].Type != sonutruntime.StreamEventToolCallStart || got[2].ArgumentsFragment != "{}" || got[3].Type != sonutruntime.StreamEventDone {
		t.Fatalf("tool/done events = %#v", got)
	}
}

func TestReadyReturnsActionableError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"retryable_unavailable"}`))
	}))
	defer server.Close()
	err := NewWithHTTPClient(server.URL, server.Client()).Ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("error = %v", err)
	}
}
