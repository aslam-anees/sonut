package llama

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// InternalLlamaPort is the loopback port the actual llama-server binds to.
// It is deliberately non-obvious (not 8000) so end users don't stumble onto
// the raw server; the public surface is the reverse proxy (default 2748),
// which masks the web UI. The server is additionally launched with --no-webui,
// so even a direct hit on this port serves no UI.
const InternalLlamaPort = 42611

// startedProxies tracks public ports we already bound a proxy on, so restart
// flows (e.g. /memory context resize re-running startLlamaCmd) don't fail
// with "address already in use" on the second StartWebUIProxy call.
var (
	proxyMu        sync.Mutex
	startedProxies = map[int]bool{}
)

// StartWebUIProxy starts a reverse proxy on the public port that:
// - Returns "atcode is serving" for UI paths (root, index.html, etc.)
// - Proxies API requests to the internal llama server on internalPort
// This masking prevents the llama-server web UI from being exposed while
// keeping all API endpoints fully functional.
func StartWebUIProxy(publicPort, internalPort int) error {
	proxyMu.Lock()
	if startedProxies[publicPort] {
		proxyMu.Unlock()
		return nil // already running in this process (e.g. llama restart)
	}
	proxyMu.Unlock()

	publicAddr := fmt.Sprintf("127.0.0.1:%d", publicPort)
	internalURL := fmt.Sprintf("http://127.0.0.1:%d", internalPort)

	// Create the reverse proxy
	target, err := url.Parse(internalURL)
	if err != nil {
		return fmt.Errorf("failed to parse target URL: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	// Warmup requests are deliberately canceled when a real user request
	// takes ownership of the single llama slot. The default reverse-proxy
	// handler logs those expected cancellations as "http: proxy error" to
	// stderr, which makes a healthy preemption look like an engine crash.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if isExpectedProxyCancellation(err) {
			return
		}
		http.Error(w, "atcode engine temporarily unavailable", http.StatusBadGateway)
	}

	// Create the HTTP handler
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Return "atcode is serving" for root and UI paths
		if isUIPath(r.URL.Path) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "atcode is serving")
			return
		}
		// Proxy API requests to the internal llama server
		proxy.ServeHTTP(w, r)
	})

	// Start the server in a goroutine
	listener, err := net.Listen("tcp", publicAddr)
	if err != nil {
		// Another process already serves this public port (interactive TUI while
		// at exec starts). If the existing listener is a healthy atcode proxy,
		// treat that as success so concurrent local sessions share the engine.
		// Do NOT mark startedProxies: we do not own that listener, so a later
		// call must re-check health and may bind after the other process exits.
		if publicProxyHealthy(context.Background(), publicPort) {
			return nil
		}
		return fmt.Errorf("failed to listen on %s: %w", publicAddr, err)
	}

	proxyMu.Lock()
	startedProxies[publicPort] = true
	proxyMu.Unlock()

	go func() {
		// Serve until process exit; errors here are non-fatal (best-effort mask).
		_ = http.Serve(listener, mux)
		proxyMu.Lock()
		delete(startedProxies, publicPort)
		proxyMu.Unlock()
	}()

	return nil
}

func isExpectedProxyCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "use of closed network connection")
}

// ReclaimPublicPort frees the public port when it is owned by a previous
// @code/llama-server instance. A new @code instance is the hand-off signal:
// it gracefully stops the old owner, waits for the port to drain, and lets the
// caller bind the new proxy. Unrecognized processes are never killed.
func ReclaimPublicPort(publicPort int) {
	proxyMu.Lock()
	ours := startedProxies[publicPort]
	proxyMu.Unlock()
	if ours {
		return
	}
	if cmd := cmdlineOnPort(publicPort); strings.Contains(cmd, AtcodeServerBaseName) ||
		strings.Contains(cmd, "llama-server") || isAtcodeProcessCommand(cmd) {
		KillLlamaOnPort(publicPort)
	}
}

// isAtcodeProcessCommand identifies the product executable without matching a
// shell, editor, or unrelated service that happens to use the public port.
// The source checkout is commonly launched as ./@; installed builds use
// atcode. Also accept the current executable's basename for renamed installs.
func isAtcodeProcessCommand(cmd string) bool {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return false
	}
	name := strings.Trim(filepath.Base(strings.Trim(fields[0], "\"'")), "\r\n")
	if name == "@" || strings.EqualFold(name, "atcode") {
		return true
	}
	if current, err := os.Executable(); err == nil {
		return name != "" && name == filepath.Base(current)
	}
	return false
}

// isUIPath determines if a request path is for the web UI (should be masked)
// or an API path (should be proxied through to llama-server).
// Returns true for paths that are part of the llama-server web UI.
func isUIPath(path string) bool {
	// Exact match for root. Root must NOT appear in the prefix list below —
	// "/" is a prefix of every path and would mask the API too.
	if path == "/" || path == "" {
		return true
	}

	// Path prefixes that are part of the llama-server web UI
	uiPaths := []string{
		"/index.html", // main UI file
		"/css/",       // stylesheets
		"/js/",        // scripts
		"/img/",       // images
		"/favicon",    // icon
		"/static/",    // static assets
		"/ui/",        // UI namespace (some versions)
		"/build/",     // build artifacts
	}

	// Check if path starts with any UI prefix
	for _, uiPath := range uiPaths {
		if strings.HasPrefix(path, uiPath) {
			return true
		}
	}

	// All other paths (v1, health, completions, etc.) are API endpoints
	return false
}
