//go:build !windows

package engineclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
)

func defaultEndpoint(home string) string { return filepath.Join(home, "run", "engine.sock") }

func newTransport(endpoint string) (http.RoundTripper, error) {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return http.DefaultTransport, nil
	}
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("@engine endpoint is empty")
	}
	dialer := &net.Dialer{}
	return &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", endpoint)
	}, DisableKeepAlives: false}, nil
}
