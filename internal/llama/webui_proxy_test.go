package llama

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestWebUIProxyMasksUIAndProxiesAPI verifies the public port returns
// "atcode is serving" for UI paths while API paths reach the backend.
func TestWebUIProxyMasksUIAndProxiesAPI(t *testing.T) {
	// Fake llama-server backend on an ephemeral port.
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendLn.Close()
	backendPort := backendLn.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	})
	go http.Serve(backendLn, mux)

	// Public proxy on another ephemeral port.
	pubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	publicPort := pubLn.Addr().(*net.TCPAddr).Port
	pubLn.Close() // free it for StartWebUIProxy
	if err := StartWebUIProxy(publicPort, backendPort); err != nil {
		t.Fatalf("StartWebUIProxy: %v", err)
	}
	// Idempotency: a second start (restart flow) must not error.
	if err := StartWebUIProxy(publicPort, backendPort); err != nil {
		t.Fatalf("second StartWebUIProxy: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	get := func(path string) string {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", publicPort, path))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	for _, uiPath := range []string{"/", "/index.html"} {
		if got := get(uiPath); !strings.Contains(got, "atcode is serving") {
			t.Errorf("GET %s = %q, want 'atcode is serving'", uiPath, got)
		}
	}
	if got := get("/health"); !strings.Contains(got, "ok") {
		t.Errorf("GET /health = %q, want proxied backend response", got)
	}
	if got := get("/v1/models"); !strings.Contains(got, "data") {
		t.Errorf("GET /v1/models = %q, want proxied backend response", got)
	}
}

func TestIsAtcodeProcessCommand(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want bool
	}{
		{cmd: "./@", want: true},
		{cmd: "/usr/local/bin/atcode --resume", want: true},
		{cmd: "llama-server -m model.gguf", want: false},
		{cmd: "python -m http.server 2748", want: false},
	} {
		if got := isAtcodeProcessCommand(tc.cmd); got != tc.want {
			t.Errorf("isAtcodeProcessCommand(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestProxyErrorHandlerDoesNotExposeExpectedCancellation(t *testing.T) {
	// The handler is installed by StartWebUIProxy; this test exercises the
	// same cancellation classification used for warmup preemption.
	if !isExpectedProxyCancellation(context.Canceled) {
		t.Fatal("context cancellation should be treated as expected")
	}
}

// Concurrent local sessions: TUI already holds the public proxy; a second
// StartWebUIProxy (at exec) must reuse the healthy listener instead of EADDRINUSE.
func TestStartWebUIProxyReusesHealthyExternalListener(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendLn.Close()
	backendPort := backendLn.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	go http.Serve(backendLn, mux)

	// Simulate the other process's public proxy.
	pubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	publicPort := pubLn.Addr().(*net.TCPAddr).Port
	go http.Serve(pubLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"status":"ok"}`)
			return
		}
		http.NotFound(w, r)
	}))
	time.Sleep(30 * time.Millisecond)

	if err := StartWebUIProxy(publicPort, backendPort); err != nil {
		t.Fatalf("reuse external healthy proxy: %v", err)
	}
}
