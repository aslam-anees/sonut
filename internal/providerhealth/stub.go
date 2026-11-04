package providerhealth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/providercatalog"
)

type Status int

const (
	StatusPass Status = iota
	StatusFail
	StatusWarn
)

func (s Status) String() string {
	switch s {
	case StatusPass:
		return "pass"
	case StatusFail:
		return "fail"
	case StatusWarn:
		return "warn"
	default:
		return "unknown"
	}
}

type Options struct {
	Profile      config.ProviderProfile
	Connectivity bool
	UserAgent    string
	// HTTPClient is injectable for tests and embedders. A nil client uses a
	// bounded default client; callers should not share a client with a request
	// body policy they do not control.
	HTTPClient *http.Client
	Timeout    time.Duration
	MaxBody    int64
}

type Category int

const (
	CategoryNone Category = iota
	CategoryRateLimit
	CategoryAuth
)

type Check struct {
	ID       string
	Status   Status
	Message  string
	Details  interface{}
	Target   string
	Category Category
}

type Result struct {
	Status Status
	Checks []Check
}

// PrimaryCheck returns the connectivity check, if any, so callers can surface
// its status/message without needing to know the check ID.
func (r *Result) PrimaryCheck() *Check {
	for i := range r.Checks {
		if r.Checks[i].ID == "provider.connectivity" {
			return &r.Checks[i]
		}
	}
	return nil
}

func Probe(ctx context.Context, opts Options) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	profile := opts.Profile
	descriptor, hasDescriptor := descriptorForProfile(profile)
	baseURL := strings.TrimSpace(profile.BaseURL)
	if baseURL == "" && hasDescriptor {
		baseURL = strings.TrimSpace(descriptor.DefaultBaseURL)
	}
	if !opts.Connectivity {
		if baseURL == "" {
			return Result{Status: StatusFail, Checks: []Check{{
				ID:       "provider.configuration",
				Status:   StatusFail,
				Message:  "provider base URL is not configured",
				Category: CategoryNone,
			}}}
		}
		if _, err := parseBaseURL(baseURL); err != nil {
			return Result{Status: StatusFail, Checks: []Check{{
				ID:       "provider.configuration",
				Status:   StatusFail,
				Message:  err.Error(),
				Target:   safeTarget(baseURL),
				Category: CategoryNone,
			}}}
		}
		return Result{Status: StatusPass, Checks: []Check{{
			ID:      "provider.configuration",
			Status:  StatusPass,
			Message: "provider configuration is valid",
			Target:  safeTarget(baseURL),
		}}}
	}

	parsed, err := parseBaseURL(baseURL)
	if err != nil {
		return Result{Status: StatusFail, Checks: []Check{{
			ID:       "provider.connectivity",
			Status:   StatusFail,
			Message:  err.Error(),
			Target:   safeTarget(baseURL),
			Category: CategoryNone,
		}}}
	}

	endpoint := endpointURL(parsed, profileKind(profile, descriptor))
	requestCtx := ctx
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if deadline, ok := requestCtx.Deadline(); !ok || time.Until(deadline) > timeout {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(requestCtx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Result{Status: StatusFail, Checks: []Check{{
			ID:       "provider.connectivity",
			Status:   StatusFail,
			Message:  "could not create provider health request",
			Target:   safeTarget(endpoint.String()),
			Category: CategoryNone,
		}}}
	}
	applyHeaders(req, profile, profileKind(profile, descriptor))
	if agent := strings.TrimSpace(opts.UserAgent); agent != "" {
		req.Header.Set("User-Agent", agent)
	} else {
		req.Header.Set("User-Agent", "atcode/provider-health")
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	response, err := client.Do(req)
	if err != nil {
		message := "provider endpoint could not be reached"
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			message = "provider endpoint timed out"
		}
		return Result{Status: StatusFail, Checks: []Check{{
			ID:       "provider.connectivity",
			Status:   StatusFail,
			Message:  message,
			Details:  map[string]string{"error": classifyNetworkError(err)},
			Target:   safeTarget(endpoint.String()),
			Category: CategoryNone,
		}}}
	}
	defer response.Body.Close()
	maxBody := opts.MaxBody
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBody))

	check := checkForStatus(response.StatusCode, response.Status)
	check.ID = "provider.connectivity"
	check.Target = safeTarget(endpoint.String())
	return Result{Status: check.Status, Checks: []Check{check}}
}

func descriptorForProfile(profile config.ProviderProfile) (providercatalog.Descriptor, bool) {
	for _, id := range []string{profile.CatalogID, profile.Provider, string(profile.ProviderKind)} {
		if descriptor, err := providercatalog.Require(id); err == nil {
			return descriptor, true
		}
	}
	return providercatalog.Descriptor{}, false
}

func profileKind(profile config.ProviderProfile, descriptor providercatalog.Descriptor) config.ProviderKind {
	if kind := config.ProviderKind(strings.ToLower(strings.TrimSpace(string(profile.ProviderKind)))); kind != "" {
		return kind
	}
	if descriptor.Transport != "" {
		return config.ProviderKind(descriptor.Transport)
	}
	return config.ProviderKind(strings.ToLower(strings.TrimSpace(profile.Provider)))
}

func parseBaseURL(raw string) (*url.URL, error) {
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

func endpointURL(base *url.URL, kind config.ProviderKind) *url.URL {
	endpoint := *base
	suffix := "models"
	switch kind {
	case config.ProviderKindAnthropic, config.ProviderKindAnthropicCompat:
		if !strings.HasSuffix(strings.TrimSuffix(endpoint.Path, "/"), "/v1") {
			suffix = "v1/models"
		} else {
			suffix = "models"
		}
	case config.ProviderKindGoogle:
		suffix = "v1beta/models"
	}
	endpoint.Path = path.Join(endpoint.Path, suffix)
	endpoint.RawPath = ""
	return &endpoint
}

func applyHeaders(req *http.Request, profile config.ProviderProfile, kind config.ProviderKind) {
	for key, value := range profile.CustomHeaders {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	key := strings.TrimSpace(profile.APIKey)
	if key == "" && strings.TrimSpace(profile.APIKeyEnv) != "" {
		key = strings.TrimSpace(os.Getenv(strings.TrimSpace(profile.APIKeyEnv)))
	}
	header := strings.TrimSpace(profile.AuthHeader)
	value := strings.TrimSpace(profile.AuthHeaderValue)
	if strings.HasPrefix(value, "env:") {
		value = strings.TrimSpace(os.Getenv(strings.TrimSpace(strings.TrimPrefix(value, "env:"))))
	}
	if value != "" {
		if header == "" {
			header = "Authorization"
		}
		req.Header.Set(header, value)
		return
	}
	if key == "" {
		return
	}
	switch kind {
	case config.ProviderKindAnthropic, config.ProviderKindAnthropicCompat:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case config.ProviderKindGoogle:
		req.Header.Set("x-goog-api-key", key)
	default:
		if header == "" {
			header = "Authorization"
		}
		if strings.EqualFold(strings.TrimSpace(profile.AuthScheme), "raw") || strings.EqualFold(strings.TrimSpace(profile.AuthScheme), "none") {
			req.Header.Set(header, key)
		} else if strings.TrimSpace(profile.AuthScheme) != "" {
			req.Header.Set(header, strings.TrimSpace(profile.AuthScheme)+" "+key)
		} else {
			req.Header.Set(header, "Bearer "+key)
		}
	}
}

func checkForStatus(code int, status string) Check {
	check := Check{Status: StatusFail, Message: "provider endpoint returned " + status}
	switch {
	case code >= 200 && code < 300:
		check.Status = StatusPass
		check.Message = "provider endpoint reachable"
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		check.Category = CategoryAuth
		check.Message = "provider endpoint rejected the configured credentials"
	case code == http.StatusTooManyRequests:
		check.Status = StatusWarn
		check.Category = CategoryRateLimit
		check.Message = "provider endpoint is rate limited"
	case code >= 500:
		check.Message = "provider endpoint is unavailable"
	}
	check.Details = map[string]any{"status": code}
	return check
}

func classifyNetworkError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

func safeTarget(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
