// Portions derived from DEEIX-Chat, Copyright 2026 DEEIX-AI, Apache-2.0.
// Modified for @code; see WIRING.md and third_party/licenses/DEEIX-Chat-LICENSE.txt.
// Package security provides SSRF-hardened outbound HTTP helpers and
// sensitive-header redaction. Vendored from DEEIX-Chat/backend's
// internal/shared/security package.
package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const redactedHeaderValue = "********"
const defaultOutboundConnectTimeout = 10 * time.Second

// ErrUnsafeOutboundURL indicates an outbound URL fails the production safety
// boundary (SSRF protection).
var ErrUnsafeOutboundURL = errors.New("unsafe outbound url")

// RedactHeadersJSON redacts sensitive header values in a JSON object of
// custom request headers, so API responses don't widen a secret's exposure.
func RedactHeadersJSON(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return value
	}
	payload := map[string]interface{}{}
	if err := json.Unmarshal([]byte(value), &payload); err != nil || payload == nil {
		return "{}"
	}
	result := make(map[string]interface{}, len(payload))
	for key, item := range payload {
		if IsSensitiveHeaderName(key) {
			result[key] = redactedHeaderValue
			continue
		}
		result[key] = item
	}
	normalized, err := json.Marshal(result)
	if err != nil {
		return "{}"
	}
	return string(normalized)
}

// IsSensitiveHeaderName reports whether a header name may carry a secret,
// token, or cookie.
func IsSensitiveHeaderName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	if normalized == "" {
		return false
	}
	if normalized == "cookie" || normalized == "set-cookie" {
		return true
	}
	for _, marker := range []string{"authorization", "api-key", "apikey", "token", "secret"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// ValidateOutboundHTTPURL validates an admin-configured outbound HTTP URL.
// When SSRF protection is enabled it additionally blocks loopback, private,
// link-local, and metadata addresses.
func ValidateOutboundHTTPURL(raw string, env string, ssrfProtectionEnabled bool) error {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%w: invalid url", ErrUnsafeOutboundURL)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: user info is not allowed", ErrUnsafeOutboundURL)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: unsupported scheme", ErrUnsafeOutboundURL)
	}
	if !shouldEnforceSSRFProtection(env, ssrfProtectionEnabled) {
		return nil
	}
	host := normalizeURLHostname(parsed.Hostname())
	if host == "" || isUnsafeHostname(host) {
		return fmt.Errorf("%w: unsafe host", ErrUnsafeOutboundURL)
	}
	if ip := net.ParseIP(host); ip != nil && isUnsafeIP(ip) {
		return fmt.Errorf("%w: unsafe ip", ErrUnsafeOutboundURL)
	}
	return nil
}

// NewOutboundHTTPClient creates an HTTP client with optional SSRF protection.
func NewOutboundHTTPClient(env string, ssrfProtectionEnabled bool, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: NewOutboundHTTPTransport(env, ssrfProtectionEnabled, defaultOutboundConnectTimeout),
	}
}

// NewOutboundHTTPTransport creates an HTTP transport with optional SSRF
// protection.
func NewOutboundHTTPTransport(env string, ssrfProtectionEnabled bool, connectTimeout time.Duration) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	if shouldEnforceSSRFProtection(env, ssrfProtectionEnabled) {
		transport.Proxy = nil
	}
	transport.DialContext = NewOutboundDialContext(env, ssrfProtectionEnabled, connectTimeout, 30*time.Second)
	return transport
}

// NewOutboundDialContext creates an optionally hardened dialer. When SSRF
// protection is enabled, it resolves the target hostname first, rejects
// loopback/private/link-local/metadata IPs, then dials the validated IP
// directly (preventing TOCTOU DNS rebinding).
func NewOutboundDialContext(env string, ssrfProtectionEnabled bool, timeout time.Duration, keepAlive time.Duration) func(context.Context, string, string) (net.Conn, error) {
	if timeout <= 0 {
		timeout = defaultOutboundConnectTimeout
	}
	if keepAlive == 0 {
		keepAlive = 30 * time.Second
	}
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: keepAlive,
	}
	return newOutboundDialContext(env, ssrfProtectionEnabled, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}

type lookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)
type dialContextFunc func(context.Context, string, string) (net.Conn, error)

func newOutboundDialContext(env string, ssrfProtectionEnabled bool, lookupIPAddr lookupIPAddrFunc, dial dialContextFunc) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		if !shouldEnforceSSRFProtection(env, ssrfProtectionEnabled) {
			return dial(ctx, network, address)
		}
		addresses, err := resolveSafeDialAddresses(ctx, network, address, lookupIPAddr)
		if err != nil {
			return nil, err
		}
		var firstErr error
		for _, dialAddress := range addresses {
			conn, err := dial(ctx, network, dialAddress)
			if err == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("%w: no dial address", ErrUnsafeOutboundURL)
	}
}

func resolveSafeDialAddresses(ctx context.Context, network string, address string, lookupIPAddr lookupIPAddrFunc) ([]string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid dial address", ErrUnsafeOutboundURL)
	}
	host = normalizeURLHostname(host)
	if host == "" || isUnsafeHostname(host) {
		return nil, fmt.Errorf("%w: unsafe host", ErrUnsafeOutboundURL)
	}
	if ip := net.ParseIP(host); ip != nil {
		if isUnsafeIP(ip) {
			return nil, fmt.Errorf("%w: unsafe ip", ErrUnsafeOutboundURL)
		}
		if !ipMatchesNetwork(ip, network) {
			return nil, fmt.Errorf("%w: no address for network", ErrUnsafeOutboundURL)
		}
		return []string{net.JoinHostPort(ip.String(), port)}, nil
	}
	records, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve host: %v", ErrUnsafeOutboundURL, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: no resolved ip", ErrUnsafeOutboundURL)
	}
	addresses := make([]string, 0, len(records))
	for _, record := range records {
		ip := record.IP
		if ip == nil {
			continue
		}
		if isUnsafeIP(ip) {
			return nil, fmt.Errorf("%w: unsafe resolved ip", ErrUnsafeOutboundURL)
		}
		if ipMatchesNetwork(ip, network) {
			addresses = append(addresses, net.JoinHostPort(ip.String(), port))
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: no address for network", ErrUnsafeOutboundURL)
	}
	return addresses, nil
}

func ipMatchesNetwork(ip net.IP, network string) bool {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "tcp4":
		return ip.To4() != nil
	case "tcp6":
		return ip.To4() == nil
	default:
		return true
	}
}

func isProductionEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "prod", "production":
		return true
	default:
		return false
	}
}

func shouldEnforceSSRFProtection(env string, enabled bool) bool {
	return enabled && isProductionEnv(env)
}

func normalizeURLHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func isUnsafeHostname(host string) bool {
	switch host {
	case "localhost", "localhost.localdomain", "ip6-localhost", "metadata.google.internal":
		return true
	default:
		return strings.HasSuffix(host, ".localhost")
	}
}

func isUnsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	normalized := ip
	if v4 := ip.To4(); v4 != nil {
		normalized = v4
	}
	if normalized.IsLoopback() ||
		normalized.IsPrivate() ||
		normalized.IsLinkLocalUnicast() ||
		normalized.IsLinkLocalMulticast() ||
		normalized.IsUnspecified() ||
		normalized.IsMulticast() {
		return true
	}
	// 100.100.100.200 is Alibaba Cloud's metadata endpoint.
	return normalized.Equal(net.ParseIP("100.100.100.200"))
}
