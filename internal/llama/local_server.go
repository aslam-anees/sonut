package llama

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/aistatine/sonut/internal/background"
)

// EnsureLocalServer makes the local engine usable by non-TUI callers. A
// healthy server is reused; a server started here is returned with a stop
// function so the caller cannot accidentally kill an engine it did not own.
// The public proxy is started only when the caller needs it and is already
// running in this process otherwise.
func EnsureLocalServer(ctx context.Context, publicPort int) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if publicPort <= 0 {
		return func() {}, fmt.Errorf("invalid local server port %d", publicPort)
	}
	if err := PrepareEngineAuth(); err != nil {
		return func() {}, fmt.Errorf("prepare local engine authentication: %w", err)
	}

	if serverHealthy(ctx, InternalLlamaPort) {
		// A healthy endpoint is not enough: an older llama-server can keep
		// accepting requests with the wrong context, batch size, slot count, or
		// projector settings. Reusing it leaves the caller waiting behind stale
		// prefill behavior indefinitely. Validate before adopting it; the TUI uses
		// the same gate during startup.
		if !ServerProfileIsCurrent(InternalLlamaPort) {
			KillLlamaOnPortWithTimeout(InternalLlamaPort, 1500*time.Millisecond)
			ClearOwnedPort(InternalLlamaPort)
		}
	}
	if serverHealthy(ctx, InternalLlamaPort) {
		// When the configured public port is already the engine's internal
		// port, the engine itself is the endpoint. Starting a proxy on the same
		// address causes the real CLI to fail with "address already in use" even
		// though llama-server is healthy.
		if publicPort != InternalLlamaPort {
			// Another @code process (interactive TUI) may already own the public
			// proxy. Reuse it when healthy instead of failing with EADDRINUSE —
			// live supermario probe: at exec while `at`/TUI holds :2748.
			if publicProxyHealthy(ctx, publicPort) {
				return func() {}, nil
			}
			if err := StartWebUIProxy(publicPort, InternalLlamaPort); err != nil {
				// Race: proxy came up between the health check and bind. Retry once.
				if publicProxyHealthy(ctx, publicPort) {
					return func() {}, nil
				}
				return func() {}, err
			}
		}
		return func() {}, nil
	}
	// No usable engine remains, so a stale @code proxy is a safe hand-off
	// candidate. ReclaimPublicPort only targets our executable or llama-server;
	// unrelated services on the configured port are left untouched.
	ReclaimPublicPort(publicPort)

	serverBin, ok := FindAtcodeServer()
	if !ok {
		return func() {}, fmt.Errorf("atcode engine not found; rerun the @code installer")
	}
	modelPath := os.Getenv("ATCODE_MODEL_PATH")
	if modelPath == "" {
		modelPath = atcodeEnv("MODEL_PATH")
	}
	var mmProjPath string
	if modelPath == "" {
		modelPath, mmProjPath, ok = CheckModelsPresent()
		if !ok {
			return func() {}, fmt.Errorf("no local model found; set ATCODE_MODEL_PATH or run setup")
		}
	}

	if publicPort != InternalLlamaPort {
		if err := StartWebUIProxy(publicPort, InternalLlamaPort); err != nil {
			return func() {}, err
		}
	}
	args := OptimalLlamaArgs(modelPath, mmProjPath, InternalLlamaPort)
	cmd := exec.Command(serverBin, args...)
	background.ConfigureChildProcessGroup(cmd)
	var logFile *os.File
	if opened, openErr := os.OpenFile(AtcodeServerLogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); openErr == nil {
		logFile = opened
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return func() {}, fmt.Errorf("failed to start atcode engine: %w", err)
	}
	if logFile != nil {
		_ = logFile.Close()
	}
	MarkOwnedPort(InternalLlamaPort)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	if err := waitForServerProcess(ctx, InternalLlamaPort, exited); err != nil {
		KillLlamaOnPortWithTimeout(InternalLlamaPort, 1500*time.Millisecond)
		ClearOwnedPort(InternalLlamaPort)
		return func() {}, fmt.Errorf("atcode engine did not become ready: %w", err)
	}
	return func() { KillLlamaOnPortWithTimeout(InternalLlamaPort, 1500*time.Millisecond) }, nil
}

func serverHealthy(ctx context.Context, port int) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// publicProxyHealthy reports whether the public reverse-proxy port is already
// serving (owned by this process or another @code instance). /health is
// proxied to the engine; a 200 means exec can reuse the endpoint without
// binding again.
func publicProxyHealthy(ctx context.Context, publicPort int) bool {
	if publicPort <= 0 {
		return false
	}
	return serverHealthy(ctx, publicPort)
}

func waitForServer(ctx context.Context, port int) error {
	return waitForServerProcess(ctx, port, nil)
}

func waitForServerProcess(ctx context.Context, port int, exited <-chan error) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if serverHealthy(ctx, port) {
			return nil
		}
		select {
		case err := <-exited:
			if err == nil {
				return fmt.Errorf("local engine exited before becoming ready (see %s)", AtcodeServerLogPath())
			}
			return fmt.Errorf("local engine exited before becoming ready: %w (see %s)", err, AtcodeServerLogPath())
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
