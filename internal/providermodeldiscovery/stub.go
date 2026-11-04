package providermodeldiscovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/providercatalog"
)

type Service struct{}

type Options struct {
	HTTPClient *http.Client
	Timeout    time.Duration
	UserAgent  string
	MaxBody    int64
}

type Model struct {
	ID              string
	Name            string
	Description     string
	InputModalities []string
	ContextWindow   int
	ToolCall        bool
	Reasoning       bool
	InputCost       float64
	OutputCost      float64
	Tags            []string
	Source          string
}

func New() *Service {
	return &Service{}
}

func DiscoverCatalog(ctx context.Context, descriptor providercatalog.Descriptor, profile config.ProviderProfile, opts Options) ([]Model, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	baseURL := strings.TrimSpace(profile.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(descriptor.DefaultBaseURL)
	}
	base, err := parseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(descriptor.ID), "ollama") {
		return discoverOllama(ctx, base, profile, opts)
	}
	endpoint := modelsEndpoint(base, descriptor, profile)
	body, status, err := request(ctx, http.MethodGet, endpoint, nil, profile, opts, discoveryKind(descriptor, profile))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("provider models endpoint returned HTTP %d", status)
	}
	models, err := parseModels(body, descriptor, profile)
	if err != nil {
		return nil, fmt.Errorf("decode provider models response: %w", err)
	}
	sort.SliceStable(models, func(left, right int) bool {
		return strings.ToLower(models[left].ID) < strings.ToLower(models[right].ID)
	})
	return models, nil
}

func DiscoverOllamaContextWindow(ctx context.Context, baseURL string, modelID string, opts Options) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	base, err := parseURL(baseURL)
	if err != nil {
		return 0, err
	}
	endpoint := *base
	endpoint.Path = path.Join(stripV1Path(endpoint.Path), "api", "show")
	endpoint.RawPath = ""
	payload, err := json.Marshal(map[string]string{"model": strings.TrimSpace(modelID)})
	if err != nil {
		return 0, fmt.Errorf("encode Ollama model request: %w", err)
	}
	body, status, err := request(ctx, http.MethodPost, &endpoint, bytes.NewReader(payload), config.ProviderProfile{}, opts, config.ProviderKindOpenAICompatible)
	if err != nil {
		return 0, err
	}
	if status < 200 || status >= 300 {
		return 0, fmt.Errorf("ollama model details endpoint returned HTTP %d", status)
	}
	var response struct {
		ModelInfo  map[string]json.RawMessage `json:"model_info"`
		Parameters string                     `json:"parameters"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return 0, fmt.Errorf("decode ollama model details response: %w", err)
	}
	if window := contextFromFields(response.ModelInfo); window > 0 {
		return window, nil
	}
	if window := parseContextFromParameters(response.Parameters); window > 0 {
		return window, nil
	}
	return 0, fmt.Errorf("ollama model details did not include a context window")
}

func discoverOllama(ctx context.Context, base *url.URL, profile config.ProviderProfile, opts Options) ([]Model, error) {
	endpoint := *base
	endpoint.Path = path.Join(stripV1Path(endpoint.Path), "api", "tags")
	endpoint.RawPath = ""
	body, status, err := request(ctx, http.MethodGet, &endpoint, nil, profile, opts, config.ProviderKindOpenAICompatible)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("ollama models endpoint returned HTTP %d", status)
	}
	var response struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode Ollama models response: %w", err)
	}
	models := make([]Model, 0, len(response.Models))
	for _, raw := range response.Models {
		model := modelFromRaw(raw, "ollama")
		if model.ID == "" {
			continue
		}
		model.Source = "ollama"
		models = append(models, model)
	}
	return models, nil
}

func modelsEndpoint(base *url.URL, descriptor providercatalog.Descriptor, profile config.ProviderProfile) *url.URL {
	endpoint := *base
	kind := strings.ToLower(strings.TrimSpace(string(profile.ProviderKind)))
	if kind == "" {
		kind = strings.ToLower(strings.TrimSpace(string(descriptor.Transport)))
	}
	suffix := "models"
	if kind == string(providercatalog.TransportAnthropic) || kind == string(providercatalog.TransportAnthropicCompatible) {
		if !strings.HasSuffix(strings.TrimSuffix(endpoint.Path, "/"), "/v1") {
			suffix = "v1/models"
		}
	} else if kind == string(providercatalog.TransportGoogle) {
		suffix = "v1beta/models"
	}
	endpoint.Path = path.Join(endpoint.Path, suffix)
	endpoint.RawPath = ""
	return &endpoint
}

func parseModels(body []byte, descriptor providercatalog.Descriptor, profile config.ProviderProfile) ([]Model, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	key := "data"
	if strings.EqualFold(string(descriptor.Transport), string(providercatalog.TransportGoogle)) ||
		strings.EqualFold(string(profile.ProviderKind), string(config.ProviderKindGoogle)) {
		key = "models"
	}
	var entries []map[string]json.RawMessage
	if raw := envelope[key]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
	}
	models := make([]Model, 0, len(entries))
	for _, entry := range entries {
		model := modelFromRaw(entry, sourceForDescriptor(descriptor))
		if model.ID == "" {
			continue
		}
		models = append(models, model)
	}
	return deduplicateModels(models), nil
}

func modelFromRaw(raw map[string]json.RawMessage, source string) Model {
	model := Model{
		ID:              firstString(raw, "id", "model", "name"),
		Name:            firstString(raw, "display_name", "displayName", "name", "id", "model"),
		Description:     firstString(raw, "description", "desc"),
		InputModalities: stringSlice(raw, "input_modalities", "inputModalities"),
		ContextWindow:   firstPositiveInt(raw, "context_window", "contextWindow", "context_length", "contextLength", "max_context_length", "max_model_len", "inputTokenLimit", "input_token_limit"),
		ToolCall:        firstBool(raw, "tool_call", "toolCall", "tool_calls", "supports_tools", "supportsToolCall"),
		Reasoning:       firstBool(raw, "reasoning", "thinking", "supports_reasoning", "supportsThinking"),
		InputCost:       firstFloat(raw, "input_cost", "inputCost", "input_price"),
		OutputCost:      firstFloat(raw, "output_cost", "outputCost", "output_price"),
		Tags:            stringSlice(raw, "tags", "capabilities", "supportedGenerationMethods"),
		Source:          source,
	}
	model.ID = strings.TrimPrefix(strings.TrimSpace(model.ID), "models/")
	if model.Name == "" {
		model.Name = model.ID
	}
	for _, tag := range model.Tags {
		lower := strings.ToLower(tag)
		if strings.Contains(lower, "reason") || strings.Contains(lower, "think") {
			model.Reasoning = true
		}
		if strings.Contains(lower, "tool") || strings.Contains(lower, "function") {
			model.ToolCall = true
		}
	}
	return model
}

func deduplicateModels(models []Model) []Model {
	seen := make(map[string]bool, len(models))
	result := make([]Model, 0, len(models))
	for _, model := range models {
		key := strings.ToLower(strings.TrimSpace(model.ID))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, model)
	}
	return result
}

func sourceForDescriptor(descriptor providercatalog.Descriptor) string {
	if id := strings.TrimSpace(descriptor.ID); id != "" {
		return id
	}
	return "live"
}

func request(ctx context.Context, method string, endpoint *url.URL, body io.Reader, profile config.ProviderProfile, opts Options, kind config.ProviderKind) ([]byte, int, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	requestCtx := ctx
	if deadline, ok := requestCtx.Deadline(); !ok || time.Until(deadline) > timeout {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(requestCtx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint.String(), body)
	if err != nil {
		return nil, 0, fmt.Errorf("create provider models request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	applyAuth(req, profile, kind)
	if agent := strings.TrimSpace(opts.UserAgent); agent != "" {
		req.Header.Set("User-Agent", agent)
	} else {
		req.Header.Set("User-Agent", "atcode/model-discovery")
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("provider models request failed: %w", err)
	}
	defer response.Body.Close()
	maxBody := opts.MaxBody
	if maxBody <= 0 {
		maxBody = 4 << 20
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, response.StatusCode, fmt.Errorf("read provider models response: %w", err)
	}
	if int64(len(data)) > maxBody {
		return nil, response.StatusCode, fmt.Errorf("provider models response exceeds %d bytes", maxBody)
	}
	return data, response.StatusCode, nil
}

func applyAuth(req *http.Request, profile config.ProviderProfile, kind config.ProviderKind) {
	for key, value := range profile.CustomHeaders {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	value := strings.TrimSpace(profile.AuthHeaderValue)
	header := strings.TrimSpace(profile.AuthHeader)
	if value != "" {
		if header == "" {
			header = "Authorization"
		}
		req.Header.Set(header, value)
		return
	}
	key := strings.TrimSpace(profile.APIKey)
	if key == "" && strings.TrimSpace(profile.APIKeyEnv) != "" {
		key = strings.TrimSpace(os.Getenv(strings.TrimSpace(profile.APIKeyEnv)))
	}
	if key == "" || header != "" && strings.EqualFold(header, "none") {
		return
	}
	if header == "" {
		switch kind {
		case config.ProviderKindAnthropic, config.ProviderKindAnthropicCompat:
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
			return
		case config.ProviderKindGoogle:
			req.Header.Set("x-goog-api-key", key)
			return
		}
	}
	if header == "" {
		header = "Authorization"
	}
	if strings.EqualFold(strings.TrimSpace(profile.AuthScheme), "raw") {
		req.Header.Set(header, key)
	} else if strings.TrimSpace(profile.AuthScheme) != "" {
		req.Header.Set(header, strings.TrimSpace(profile.AuthScheme)+" "+key)
	} else {
		req.Header.Set(header, "Bearer "+key)
	}
}

func discoveryKind(descriptor providercatalog.Descriptor, profile config.ProviderProfile) config.ProviderKind {
	if kind := config.ProviderKind(strings.ToLower(strings.TrimSpace(string(profile.ProviderKind)))); kind != "" {
		return kind
	}
	return config.ProviderKind(strings.ToLower(strings.TrimSpace(string(descriptor.Transport))))
}

func parseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("provider base URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("provider base URL must use http or https")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("provider base URL must not contain credentials")
	}
	return parsed, nil
}

func stripV1Path(value string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(value), "/")
	if strings.HasSuffix(trimmed, "/v1") {
		return strings.TrimSuffix(trimmed, "/v1")
	}
	return trimmed
}

func firstString(raw map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if err := json.Unmarshal(raw[key], &value); err == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositiveInt(raw map[string]json.RawMessage, keys ...string) int {
	for _, key := range keys {
		var number float64
		if err := json.Unmarshal(raw[key], &number); err == nil && number > 0 {
			return int(number)
		}
	}
	return 0
}

func firstFloat(raw map[string]json.RawMessage, keys ...string) float64 {
	for _, key := range keys {
		var number float64
		if err := json.Unmarshal(raw[key], &number); err == nil && number >= 0 {
			return number
		}
	}
	return 0
}

func firstBool(raw map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		var value bool
		if err := json.Unmarshal(raw[key], &value); err == nil && value {
			return true
		}
	}
	return false
}

func stringSlice(raw map[string]json.RawMessage, keys ...string) []string {
	for _, key := range keys {
		var values []string
		if err := json.Unmarshal(raw[key], &values); err == nil && len(values) > 0 {
			result := make([]string, 0, len(values))
			for _, value := range values {
				if value = strings.TrimSpace(value); value != "" {
					result = append(result, value)
				}
			}
			return result
		}
	}
	return nil
}

func contextFromFields(fields map[string]json.RawMessage) int {
	if len(fields) == 0 {
		return 0
	}
	for key, value := range fields {
		lower := strings.ToLower(key)
		if !strings.Contains(lower, "context") && !strings.Contains(lower, "num_ctx") {
			continue
		}
		var number float64
		if err := json.Unmarshal(value, &number); err == nil && number > 0 {
			return int(number)
		}
	}
	return 0
}

var contextParameterPattern = regexp.MustCompile(`(?im)(?:num_ctx|context_length|context_window)\s*[:=]?\s+(\d+)`)

func parseContextFromParameters(parameters string) int {
	matches := contextParameterPattern.FindStringSubmatch(parameters)
	if len(matches) != 2 {
		return 0
	}
	value, _ := strconv.Atoi(matches[1])
	return value
}
