//go:build windows

package engineclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/Microsoft/go-winio"
)

func defaultEndpoint(_ string) string {
	digest := sha256.Sum256([]byte(os.Getenv("USERNAME")))
	return `\\.\pipe\aistatine-atengine-` + hex.EncodeToString(digest[:8])
}

func newTransport(endpoint string) (http.RoundTripper, error) {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return http.DefaultTransport, nil
	}
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("@engine endpoint is empty")
	}
	return &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return winio.DialPipeContext(ctx, endpoint)
	}}, nil
}
