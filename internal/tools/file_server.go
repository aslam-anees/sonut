package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// localFileServer tracks HTTP servers started to serve local directories for
// the browser automation flow. file:// URLs are blocked by Playwright MCP, so
// we spin up a minimal per-directory HTTP server and redirect the model to the
// localhost URL instead. Servers persist for the process lifetime; the memory
// footprint is minimal (one goroutine + listener per unique directory).
var (
	fileServerMu    sync.Mutex
	fileServerPorts = map[string]int{} // abs-dir → assigned port
)

// serveLocalFile starts (or reuses) an HTTP file server rooted at the
// directory containing filePath, and returns the http://localhost:<port>/<name>
// URL for the file. Returns an error if the file does not exist.
func serveLocalFile(filePath string) (string, error) {
	// Normalise: resolve absolute path.
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path %q: %w", filePath, err)
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		return "", fmt.Errorf("file not found: %s", abs)
	}

	dir := filepath.Dir(abs)
	base := filepath.Base(abs)

	fileServerMu.Lock()
	defer fileServerMu.Unlock()

	if port, ok := fileServerPorts[dir]; ok {
		return fmt.Sprintf("http://localhost:%d/%s", port, base), nil
	}

	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		return "", fmt.Errorf("could not start local file server: %w", listenErr)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	fileServerPorts[dir] = port

	server := &http.Server{Handler: http.FileServer(http.Dir(dir))}
	go func() {
		_ = server.Serve(listener)
	}()
	// Server intentionally kept alive for the process lifetime (or until
	// shutdown via the context attached to the server below). For graceful
	// cleanup in tests we expose a shutdownFileServers helper.
	registerFileServer(server)

	return fmt.Sprintf("http://localhost:%d/%s", port, base), nil
}

// fileServerList keeps weak references so tests can shut them all down.
var (
	fileServerListMu sync.Mutex
	fileServerList   []*http.Server
)

func registerFileServer(s *http.Server) {
	fileServerListMu.Lock()
	fileServerList = append(fileServerList, s)
	fileServerListMu.Unlock()
}

// shutdownFileServers stops all running local file servers. Used in tests.
func shutdownFileServers() {
	fileServerListMu.Lock()
	servers := append([]*http.Server(nil), fileServerList...)
	fileServerList = nil
	fileServerListMu.Unlock()

	fileServerMu.Lock()
	fileServerPorts = map[string]int{}
	fileServerMu.Unlock()

	for _, s := range servers {
		_ = s.Shutdown(context.Background())
	}
}

// parseFileURL extracts a filesystem path from a file:// URL or a bare
// absolute path that looks like an HTML file. Returns ("", false) if s is
// not a local file reference.
func parseFileURL(s string) (path string, ok bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "file://") {
		// file:///abs/path or file://localhost/abs/path
		p := strings.TrimPrefix(s, "file://")
		p = strings.TrimPrefix(p, "localhost")
		if p == "" {
			return "", false
		}
		return p, true
	}
	// Bare absolute path to an HTML file.
	if filepath.IsAbs(s) && (strings.HasSuffix(strings.ToLower(s), ".html") || strings.HasSuffix(strings.ToLower(s), ".htm")) {
		return s, true
	}
	return "", false
}
