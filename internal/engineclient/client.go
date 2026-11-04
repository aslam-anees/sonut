// Package engineclient is the small, dependency-light @code client for the
// local @engine control plane. It deliberately owns only wire translation and
// transport; tool execution, approvals, and sandbox policy remain in @code.
package engineclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

const defaultBaseURL = "http://atengine.local"

type Client struct {
	baseURL string
	http    *http.Client
	seq     atomic.Uint64
}

// New uses ATENGINE_URL when supplied, otherwise the owner-only socket in
// ATENGINE_HOME (or ~/.atengine). URL overrides are useful for integration
// tests and explicitly managed enterprise deployments.
func New() (*Client, error) {
	endpoint := strings.TrimSpace(os.Getenv("ATENGINE_URL"))
	if endpoint == "" {
		home := strings.TrimSpace(os.Getenv("ATENGINE_HOME"))
		if home == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("resolve @engine home: %w", err)
			}
			home = userHome + string(os.PathSeparator) + ".atengine"
		}
		endpoint = defaultEndpoint(home)
	}
	transport, err := newTransport(endpoint)
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: defaultBaseURL, http: &http.Client{Transport: transport, Timeout: 0}}, nil
}

// NewWithHTTPClient is intentionally exported for deterministic contract tests
// and local harnesses. Production callers should use New.
func NewWithHTTPClient(baseURL string, client *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: client}
}

func (c *Client) Ready(ctx context.Context) error {
	if c == nil || c.http == nil {
		return errors.New("@engine client is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/health/ready", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("connect to @engine: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		return fmt.Errorf("@engine is not ready (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

type wireRequest struct {
	Context  wireContext   `json:"context"`
	Lane     string        `json:"lane"`
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
}

type wireContext struct {
	ProtocolVersion string    `json:"protocol_version"`
	RequestID       string    `json:"request_id"`
	Caller          string    `json:"caller"`
	Deadline        time.Time `json:"deadline"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type wireFrame struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

func (c *Client) StreamCompletion(ctx context.Context, request sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	if c == nil || c.http == nil {
		return nil, errors.New("@engine client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id := fmt.Sprintf("code-%d-%d", time.Now().UnixNano(), c.seq.Add(1))
	model := strings.TrimSpace(os.Getenv("ATCODE_MODEL"))
	if model == "" {
		model = strings.TrimSpace(os.Getenv("SONUT_MODEL"))
	}
	if model == "" {
		model = "local"
	}
	msgs := make([]wireMessage, 0, len(request.Messages))
	for _, msg := range request.Messages {
		converted := wireMessage{Role: string(msg.Role), Content: msg.Content, ToolCallID: msg.ToolCallID}
		for _, call := range msg.ToolCalls {
			converted.ToolCalls = append(converted.ToolCalls, wireToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		msgs = append(msgs, converted)
	}
	tools := make([]wireTool, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, wireTool{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(5 * time.Minute)
	}
	body, err := json.Marshal(wireRequest{Context: wireContext{ProtocolVersion: "1.0", RequestID: id, Caller: "code", Deadline: deadline}, Lane: "coding", Model: model, Messages: msgs, Tools: tools, Stream: true})
	if err != nil {
		return nil, fmt.Errorf("encode @engine request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/inference", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("@engine inference request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("@engine inference rejected (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	out := make(chan sonutruntime.StreamEvent, 32)
	go c.readFrames(ctx, resp.Body, out)
	return out, nil
}

func (c *Client) readFrames(ctx context.Context, body io.ReadCloser, out chan<- sonutruntime.StreamEvent) {
	defer close(out)
	defer body.Close()
	scanner := bufio.NewScanner(io.LimitReader(body, 16<<20))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var frame wireFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			send(ctx, out, sonutruntime.StreamEvent{Type: sonutruntime.StreamEventError, Error: fmt.Sprintf("decode @engine stream: %v", err)})
			return
		}
		event, ok := translate(frame)
		if !ok {
			continue
		}
		if !send(ctx, out, event) {
			return
		}
		if event.Type == sonutruntime.StreamEventDone || event.Type == sonutruntime.StreamEventError {
			return
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		_ = send(ctx, out, sonutruntime.StreamEvent{Type: sonutruntime.StreamEventError, Error: fmt.Sprintf("read @engine stream: %v", err)})
	}
}

func translate(frame wireFrame) (sonutruntime.StreamEvent, bool) {
	getString := func(key string) string { value, _ := frame.Payload[key].(string); return value }
	switch frame.Type {
	case "delta":
		return sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: getString("text")}, true
	case "reasoning":
		return sonutruntime.StreamEvent{Type: sonutruntime.StreamEventReasoning, Content: getString("text")}, true
	case "tool-call-start":
		return sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: getString("id"), ToolName: getString("name")}, true
	case "tool-call-delta":
		return sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: getString("id"), ToolName: getString("name"), ArgumentsFragment: getString("arguments")}, true
	case "tool-call-end":
		return sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: getString("id"), ToolName: getString("name"), ArgumentsFragment: getString("arguments")}, true
	case "completed":
		return sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}, true
	case "cancelled":
		return sonutruntime.StreamEvent{Type: sonutruntime.StreamEventError, Error: "@engine request cancelled"}, true
	case "error":
		detail := getString("detail")
		if detail == "" {
			detail = getString("error")
		}
		return sonutruntime.StreamEvent{Type: sonutruntime.StreamEventError, Error: detail}, true
	default:
		return sonutruntime.StreamEvent{}, false
	}
}

func send(ctx context.Context, out chan<- sonutruntime.StreamEvent, event sonutruntime.StreamEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
