package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

// defaultConnectTimeout bounds how long startup waits for ONE MCP server to
// connect and list its tools. A server that exceeds it is abandoned and skipped
// so a slow or unreachable server (e.g. a hosted endpoint blocked by the local
// network) cannot delay the first model response. Servers connect concurrently,
// so total startup cost is the slowest reachable server, not the sum.
const defaultConnectTimeout = 8 * time.Second

// playwrightBootstrapTimeout is deliberately longer than the general MCP
// startup budget. The default Playwright server is launched with npx and its
// first run may need to download both the package and browser support. Giving
// it the same short budget as a remote MCP makes browser tools disappear from
// the planner exactly when frontend validation needs them most.
const playwrightBootstrapTimeout = 60 * time.Second

// defaultCallTimeout bounds the complete MCP tool invocation, including the one
// safe retry allowed for idempotent/read-only calls. Without it, a hung
// server (e.g. a browser navigation that never settles, or a remote endpoint
// that accepts the request and never answers) stalls the whole agent turn:
// registryTool.Run inherits the loop's context, which has no per-tool deadline.
const defaultCallTimeout = 60 * time.Second

// defaultMCPAttemptTimeout returns control after a single unresponsive attempt
// while leaving enough of the total budget for one safe retry.
const defaultMCPAttemptTimeout = 30 * time.Second

const maxMCPCallAttempts = 2

type RegisterOptions struct {
	PermissionStore *PermissionStore
	Autonomy        PermissionAutonomy
	ClientFactory   func(context.Context, Server) (ToolClient, error)
	// ConnectTimeout bounds the per-server connect+list at startup. @code uses
	// defaultConnectTimeout.
	ConnectTimeout time.Duration
}

// SkippedServer records an MCP server that was not registered because it could
// not be reached or its tools could not be validated. Registration is
// best-effort per server: one unreachable server is skipped (and reported here)
// rather than aborting startup or disabling the others.
type SkippedServer struct {
	Name string
	Err  error
}

type Runtime struct {
	clients []ToolClient
	// cancels releases the per-server connect contexts of the clients we KEPT.
	// A stdio server's subprocess is tied to its context, so the context must
	// stay live for the session and be cancelled only at Close (after the client
	// is closed). Same length/order as clients is not required.
	cancels []context.CancelFunc
	skipped []SkippedServer
	once    sync.Once
	err     error
}

// Skipped returns the servers that were skipped during registration (unreachable
// or invalid), so the caller can warn the user without failing the launch.
func (runtime *Runtime) Skipped() []SkippedServer {
	if runtime == nil {
		return nil
	}
	return runtime.skipped
}

var unsafeToolNameChars = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func RegisterTools(ctx context.Context, registry *tools.Registry, cfg config.MCPConfig, options RegisterOptions) (*Runtime, error) {
	if registry == nil {
		return nil, fmt.Errorf("MCP tool registry is required")
	}
	servers, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{}
	if len(servers) == 0 {
		return runtime, nil
	}

	factory := options.ClientFactory
	if factory == nil {
		factory = func(ctx context.Context, server Server) (ToolClient, error) {
			return Connect(ctx, server)
		}
	}

	// Connect every server CONCURRENTLY: connect + list-tools is network/process
	// I/O, so connecting serially makes startup wait for the SUM of all servers —
	// one slow or unreachable server would block every other server AND the first
	// model response. Each server gets its own cancelable context bounded by the
	// startup timeout; a server that does not connect + list in time is abandoned
	// (its context cancelled to tear down the half-open connection/subprocess) and
	// recorded as skipped. The concurrent phase does ONLY I/O and touches no shared
	// state; all validation, conflict detection, and registration happen in the
	// deterministic serial phase below, so the result is identical regardless of
	// completion order.
	type connectResult struct {
		client ToolClient
		remote []RemoteTool
		cancel context.CancelFunc
		err    error
	}
	results := make([]connectResult, len(servers))
	var wg sync.WaitGroup
	for index := range servers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			server := servers[index]
			timeout := connectTimeoutForServer(server, options.ConnectTimeout)
			serverCtx, cancel := context.WithCancel(ctx)
			done := make(chan connectResult, 1)
			go func() {
				client, remote, err := connectAndList(serverCtx, factory, server)
				done <- connectResult{client: client, remote: remote, err: err}
			}()
			select {
			case res := <-done:
				if res.err != nil {
					cancel() // failed: nothing to keep
				} else {
					res.cancel = cancel // keep the context alive; released at Close
				}
				results[index] = res
			case <-time.After(timeout):
				cancel() // abandon the slow connect: tears down the conn/subprocess
				// Reap the goroutine + any partial client in the background so a
				// slow server never blocks startup.
				go func() {
					if res := <-done; res.client != nil {
						_ = res.client.Close()
					}
				}()
				results[index] = connectResult{err: fmt.Errorf("connect timed out after %s", timeout)}
			}
		}(index)
	}
	wg.Wait()

	// Serial, deterministic commit in server order. Building tools reads the
	// permission store and the registry, so it stays single-goroutine. A server is
	// best-effort: one that failed to connect, timed out, returned a nameless tool,
	// or conflicts with an already-committed tool is SKIPPED (recorded, not fatal),
	// and still contributes its tools all-or-none. The conflict check spans the
	// registry plus every tool committed by an earlier server.
	staged := make([]registryTool, 0)
	stagedNames := make(map[string]struct{})
	for index, server := range servers {
		res := results[index]
		if res.err != nil {
			runtime.skipped = append(runtime.skipped, SkippedServer{Name: server.Name, Err: res.err})
			continue
		}
		serverTools, validateErr := buildServerTools(registry, server, res.remote, res.client, options, stagedNames)
		if validateErr != nil {
			if res.cancel != nil {
				res.cancel()
			}
			_ = res.client.Close()
			runtime.skipped = append(runtime.skipped, SkippedServer{Name: server.Name, Err: validateErr})
			continue
		}
		runtime.clients = append(runtime.clients, res.client)
		if res.cancel != nil {
			runtime.cancels = append(runtime.cancels, res.cancel)
		}
		for _, tool := range serverTools {
			stagedNames[tool.Name()] = struct{}{}
			staged = append(staged, tool)
		}
	}
	for _, tool := range staged {
		registry.Register(tool)
	}
	return runtime, nil
}

// connectTimeoutForServer preserves an explicitly supplied timeout (tests,
// diagnostics, and callers that need strict startup latency), while giving the
// default npx Playwright server enough first-run bootstrap time. Custom
// Playwright server names are recognized by their command/args as well.
func connectTimeoutForServer(server Server, configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if isPlaywrightServer(server) {
		return playwrightBootstrapTimeout
	}
	return defaultConnectTimeout
}

func isPlaywrightServer(server Server) bool {
	if strings.EqualFold(strings.TrimSpace(server.Name), "playwright") {
		return true
	}
	text := strings.ToLower(server.Command + " " + strings.Join(server.Args, " "))
	return strings.Contains(text, "@playwright/mcp") || strings.Contains(text, "playwright-mcp")
}

// connectAndList connects to one server and lists its tools. It does ONLY I/O
// (no registry, permission-store, or other shared state), so it is safe to run
// concurrently for every server. On a list error it closes the client.
func connectAndList(ctx context.Context, factory func(context.Context, Server) (ToolClient, error), server Server) (ToolClient, []RemoteTool, error) {
	client, err := factory(ctx, server)
	if err != nil {
		return nil, nil, err
	}
	remoteTools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("list MCP tools for %s: %w", server.Name, err)
	}
	return client, remoteTools, nil
}

// buildServerTools validates a server's remote tools against the registry and the
// names already committed by earlier servers, returning the server's tools only
// when every one is named and conflict-free. It runs in the serial commit phase
// (single goroutine), so its registry and permission-store reads are race-free.
// On error the caller closes the client (it owns the result), so this never does.
func buildServerTools(registry *tools.Registry, server Server, remoteTools []RemoteTool, client ToolClient, options RegisterOptions, stagedNames map[string]struct{}) ([]registryTool, error) {
	serverTools := make([]registryTool, 0, len(remoteTools))
	localNames := make(map[string]struct{})
	for _, remote := range remoteTools {
		if strings.TrimSpace(remote.Name) == "" {
			return nil, fmt.Errorf("MCP server %s returned a tool without a name", server.Name)
		}
		tool := newRegistryTool(server, remote, client, options)
		tool.browserScreenshotTools = browserScreenshotRemoteNames(remoteTools, remote.Name)
		if existing, ok := registry.Get(tool.Name()); ok {
			return nil, fmt.Errorf("MCP tool %s from %s conflicts with existing tool %s", remote.Name, server.Name, existing.Name())
		}
		if _, ok := stagedNames[tool.Name()]; ok {
			return nil, fmt.Errorf("MCP tool %s from %s conflicts with another MCP tool named %s", remote.Name, server.Name, tool.Name())
		}
		if _, ok := localNames[tool.Name()]; ok {
			return nil, fmt.Errorf("MCP tool %s from %s conflicts with another tool from the same server", remote.Name, server.Name)
		}
		localNames[tool.Name()] = struct{}{}
		serverTools = append(serverTools, tool)
	}
	return serverTools, nil
}

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.once.Do(func() {
		for _, client := range runtime.clients {
			if err := client.Close(); err != nil && runtime.err == nil {
				runtime.err = err
			}
		}
		// Release the kept servers' connect contexts AFTER closing the clients: a
		// stdio subprocess is already terminated by Close, so cancelling is then a
		// no-op; it frees the context (and any tied subprocess) either way.
		for _, cancel := range runtime.cancels {
			cancel()
		}
	})
	return runtime.err
}

type registryTool struct {
	name                   string
	server                 Server
	remote                 RemoteTool
	client                 ToolClient
	parameters             tools.Schema
	safety                 tools.Safety
	browserScreenshotTools []string
}

func newRegistryTool(server Server, remote RemoteTool, client ToolClient, options RegisterOptions) registryTool {
	remote.Name = strings.TrimSpace(remote.Name)
	name := registryToolName(server.Name, remote.Name)
	permission := tools.PermissionPrompt
	if isPersistentlyApproved(options.PermissionStore, server, remote.Name, defaultAutonomy(options.Autonomy)) {
		permission = tools.PermissionAllow
	}
	return registryTool{
		name:       name,
		server:     server,
		remote:     remote,
		client:     client,
		parameters: SchemaFromMCP(remote.InputSchema),
		safety: tools.Safety{
			SideEffect: tools.SideEffectNetwork,
			Permission: permission,
			Reason:     fmt.Sprintf("MCP tool %s/%s runs through the configured %s server.", server.Name, remote.Name, server.Type),
		},
	}
}

func (tool registryTool) Name() string {
	return tool.name
}

func (tool registryTool) Description() string {
	if strings.TrimSpace(tool.remote.Description) != "" {
		return tool.remote.Description
	}
	return fmt.Sprintf("Call MCP tool %s/%s", tool.server.Name, tool.remote.Name)
}

func (tool registryTool) Parameters() tools.Schema {
	return tool.parameters
}

func (tool registryTool) Safety() tools.Safety {
	return tool.safety
}

// Deferred marks every MCP tool as deferred-eligible: when many MCP tools are
// registered the agent loop may withhold their full schema and advertise them
// via tool_search. Built-in tools do not implement this interface and stay
// eager.
func (tool registryTool) Deferred() bool {
	return true
}

// MCPServerName reports the tool's originating MCP server name so the deferred-
// tools reminder labels it correctly, even when the sanitized server token in the
// synthesized tool name contains an underscore (which the name-only parser would
// truncate). It returns the true configured server name, not the sanitized token.
func (tool registryTool) MCPServerName() string {
	return tool.server.Name
}

func (tool registryTool) Run(ctx context.Context, args map[string]any) tools.Result {
	return tool.run(ctx, args, nil)
}

func (tool registryTool) RunWithOptions(ctx context.Context, args map[string]any, options tools.RunOptions) tools.Result {
	return tool.run(ctx, args, options.ReportProgress)
}

func (tool registryTool) run(ctx context.Context, args map[string]any, progress func(tools.Progress)) tools.Result {
	if progress != nil {
		progress(tools.Progress{Phase: "mcp", Message: "calling " + tool.server.Name + "/" + tool.remote.Name})
	}
	// Bound the complete call so a non-responsive MCP server cannot hang the
	// agent turn. An earlier deadline on ctx still wins.
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	safeToRetry := mcpToolSafeToRetry(tool.remote)
	attempts := 0
	var result CallToolResult
	var err error
	var lastAttemptErr error
	for attempts < maxMCPCallAttempts {
		attempts++
		attemptCtx, attemptCancel := context.WithTimeout(callCtx, defaultMCPAttemptTimeout)
		result, err = tool.client.CallTool(attemptCtx, tool.remote.Name, args)
		lastAttemptErr = attemptCtx.Err()
		attemptCancel()
		if err == nil {
			break
		}
		if lastAttemptErr == context.Canceled || callCtx.Err() == context.Canceled {
			break
		}
		transient := isTransientMCPError(err) || lastAttemptErr == context.DeadlineExceeded
		if !safeToRetry || !transient || attempts >= maxMCPCallAttempts || callCtx.Err() != nil {
			break
		}
		if progress != nil {
			progress(tools.Progress{
				Phase:        "mcp",
				Message:      "transient MCP failure; retrying " + tool.server.Name + "/" + tool.remote.Name,
				RetryAttempt: attempts + 1,
			})
		}
	}
	if err != nil {
		if progress != nil {
			progress(tools.Progress{Phase: "mcp", Message: "MCP call failed"})
		}
		lifecycle := tools.LifecycleFailed
		kind := tools.ErrorExecution
		retryable := false
		if callCtx.Err() == context.DeadlineExceeded || lastAttemptErr == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
			lifecycle, kind, retryable = tools.LifecycleTimedOut, tools.ErrorTimeout, safeToRetry
		} else if callCtx.Err() == context.Canceled {
			lifecycle, kind = tools.LifecycleCancelled, tools.ErrorCancelled
		} else if isTransientMCPError(err) {
			kind, retryable = tools.ErrorNetwork, safeToRetry
		}
		return tools.Result{
			Status:    tools.StatusError,
			Output:    "Error: MCP tool " + tool.server.Name + "/" + tool.remote.Name + " failed: " + err.Error(),
			Meta:      tool.meta(attempts),
			Lifecycle: lifecycle,
			Error: &tools.StructuredError{
				Kind: kind, Code: string(kind), Message: "MCP tool call failed", Details: err.Error(), Retryable: retryable,
				Hint: mcpFailureHint(safeToRetry),
			},
		}
	}
	status := tools.StatusOK
	if result.IsError {
		status = tools.StatusError
	}
	output, images := contentArtifacts(result.Content)
	meta := tool.meta(attempts)
	if status == tools.StatusOK && mcpBrowserActionNeedsPreview(tool.remote.Name) {
		if previewImages := tool.captureBrowserPreview(callCtx); len(previewImages) > 0 {
			images = append(images, previewImages...)
			meta["browser_preview_screenshot"] = "true"
			meta["screenshot_attached"] = "true"
		}
	}
	if output == "" {
		output = "(empty MCP tool result)"
	}
	if progress != nil {
		progress(tools.Progress{Phase: "mcp", Message: "MCP call completed"})
	}
	var structuredErr *tools.StructuredError
	lifecycle := tools.LifecycleSucceeded
	if result.IsError {
		lifecycle = tools.LifecycleFailed
		structuredErr = &tools.StructuredError{
			Kind: tools.ErrorExecution, Code: "mcp_tool_error", Message: "MCP server reported a tool error",
			Details: output, Hint: "inspect the MCP response and adjust the next action",
		}
	}
	return tools.Result{
		Status:    status,
		Output:    output,
		Meta:      meta,
		Images:    images,
		Lifecycle: lifecycle,
		Error:     structuredErr,
	}
}

// mcpToolSafeToRetry permits automatic replay only when the remote declares the
// call read-only/idempotent, or its name is unambiguously query-like. Explicit
// destructive metadata always wins. Unknown and action-like tools fail once and
// return control to the planner; replaying them could duplicate a side effect.
func mcpBrowserActionNeedsPreview(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(name, "browser_") && !strings.Contains(name, "screenshot")
}

func browserScreenshotRemoteNames(remoteTools []RemoteTool, actionName string) []string {
	if !mcpBrowserActionNeedsPreview(actionName) {
		return nil
	}
	prefix := strings.TrimSuffix(actionName, "_navigate")
	if prefix == actionName {
		prefix = "browser"
	}
	wantPrefix := prefix + "_"
	seen := map[string]bool{}
	names := make([]string, 0, 3)
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, candidate := range []string{prefix + "_take_screenshot", prefix + "_screenshot"} {
		for _, remote := range remoteTools {
			if remote.Name == candidate {
				add(candidate)
				break
			}
		}
	}
	for _, remote := range remoteTools {
		if strings.HasPrefix(remote.Name, wantPrefix) && strings.Contains(remote.Name, "screenshot") {
			add(remote.Name)
		}
	}
	return names
}

func (tool registryTool) captureBrowserPreview(ctx context.Context) []sonutruntime.ImageBlock {
	for _, name := range tool.browserScreenshotTools {
		callCtx, cancel := context.WithTimeout(ctx, defaultMCPAttemptTimeout)
		result, err := tool.client.CallTool(callCtx, name, map[string]any{})
		cancel()
		if err != nil || result.IsError {
			continue
		}
		_, images := contentArtifacts(result.Content)
		if len(images) > 0 {
			return images
		}
	}
	return nil
}

func mcpToolSafeToRetry(remote RemoteTool) bool {
	if remote.Annotations.DestructiveHint {
		return false
	}
	if remote.Annotations.ReadOnlyHint || remote.Annotations.IdempotentHint {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(remote.Name))
	for _, marker := range []string{
		"create", "add", "set", "update", "edit", "write", "delete", "remove",
		"send", "post", "put", "patch", "click", "type", "press", "submit",
		"purchase", "pay", "move", "rename", "install", "execute", "run",
	} {
		for _, token := range strings.FieldsFunc(name, func(r rune) bool {
			return r == '_' || r == '-' || r == '.' || r == '/'
		}) {
			if token == marker {
				return false
			}
		}
	}
	for _, prefix := range []string{
		"get_", "list_", "read_", "search_", "find_", "fetch_", "lookup_",
		"query_", "inspect_", "describe_", "status_", "snapshot_", "screenshot_",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "get", "list", "read", "search", "find", "fetch", "lookup",
		"query", "inspect", "describe", "status", "snapshot", "screenshot":
		return true
	default:
		return false
	}
}

func isTransientMCPError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"timed out", "timeout", "temporarily unavailable", "temporary failure",
		"connection reset", "connection refused", "broken pipe", "transport closing",
		"server unavailable", "service unavailable", "too many requests", "rate limit",
		"status 429", "status 502", "status 503", "status 504",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func mcpFailureHint(safeToRetry bool) string {
	if safeToRetry {
		return "the safe automatic retry was exhausted; check the MCP server status before trying again"
	}
	return "the call was not replayed because it may have side effects; inspect server state before deciding whether to retry"
}

const maxMCPImageBytes = 10 << 20

// contentArtifacts preserves MCP's multimodal blocks instead of flattening the
// whole response to text. Images travel through @code's existing vision path;
// resources remain concise references in model text. Invalid/unknown blocks
// degrade safely without making a useful text result fail.
func contentArtifacts(content []Content) (string, []sonutruntime.ImageBlock) {
	parts := make([]string, 0, len(content))
	images := make([]sonutruntime.ImageBlock, 0, len(content))
	for _, item := range content {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "text":
			if text := strings.TrimSpace(item.Text); text != "" {
				parts = append(parts, text)
			}
		case "image":
			mediaType := sonutruntime.NormalizeImageMediaType(item.MimeType)
			data, err := base64.StdEncoding.DecodeString(item.Data)
			if err != nil || mediaType == "" || len(data) == 0 || len(data) > maxMCPImageBytes {
				parts = append(parts, "MCP returned an image that could not be attached safely.")
				continue
			}
			images = append(images, sonutruntime.ImageBlock{MediaType: mediaType, Data: data})
			parts = append(parts, "MCP returned an image attachment.")
		case "resource", "resource_link":
			if uri := strings.TrimSpace(item.URI); uri != "" {
				parts = append(parts, "MCP returned resource: "+uri)
			}
		default:
			if text := strings.TrimSpace(item.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), images
}

func (tool registryTool) meta(attempts int) map[string]string {
	return map[string]string{
		"mcp.server":   tool.server.Name,
		"mcp.tool":     tool.remote.Name,
		"mcp.identity": tool.server.Identity,
		"mcp.attempts": strconv.Itoa(attempts),
	}
}

func registryToolName(serverName string, toolName string) string {
	serverPart := sanitizeToolNamePart(serverName)
	toolPart := sanitizeToolNamePart(toolName)
	if toolPart == "" {
		toolPart = "tool"
	}
	return "mcp_" + serverPart + "_" + toolPart
}

func sanitizeToolNamePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = unsafeToolNameChars.ReplaceAllString(value, "_")
	value = strings.Trim(value, "_")
	if value == "" {
		return "server"
	}
	return value
}

func isPersistentlyApproved(store *PermissionStore, server Server, toolName string, autonomy PermissionAutonomy) bool {
	if store == nil {
		return false
	}
	approved, err := store.IsToolPersistentlyApproved(CheckToolInput{
		ServerName:        server.Name,
		ServerIdentity:    server.Identity,
		ToolName:          toolName,
		RequestedAutonomy: autonomy,
	})
	return err == nil && approved
}
