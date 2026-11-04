package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

type frozenFileLineProvider struct {
	calls int
}

type repeatedFileProvider struct {
	calls int
}

func (provider *repeatedFileProvider) StreamCompletion(ctx context.Context, _ sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	provider.calls++
	ch := make(chan sonutruntime.StreamEvent, 8)
	if provider.calls > 1 {
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "done"}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
		close(ch)
		return ch, nil
	}

	go func() {
		defer close(ch)
		clean := strings.Repeat("const particle = {};\n", 16)
		malformed := "let particlesToSpawnInc" + strings.Repeat("Dec", 12) + "\n"
		arguments := `{"path":"game.js","content":` + quoteJSONString(clean+malformed)
		events := []sonutruntime.StreamEvent{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "repeated-write", ToolName: "write_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "repeated-write", ArgumentsFragment: arguments},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "repeated-write"},
			{Type: sonutruntime.StreamEventDone},
		}
		for _, event := range events {
			select {
			case ch <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (provider *frozenFileLineProvider) StreamCompletion(ctx context.Context, _ sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	provider.calls++
	ch := make(chan sonutruntime.StreamEvent, 8)
	if provider.calls > 1 {
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "done"}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
		close(ch)
		return ch, nil
	}

	go func() {
		defer close(ch)
		events := []sonutruntime.StreamEvent{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "write-frozen", ToolName: "write_file"},
			{
				Type:              sonutruntime.StreamEventToolCallDelta,
				ToolCallID:        "write-frozen",
				ArgumentsFragment: `{"path":"game.js","content":"one\ntwo\nthree\nfour\nfive\nsix`,
			},
		}
		for _, event := range events {
			select {
			case ch <- event:
			case <-ctx.Done():
				return
			}
		}

		// Reproduce the production failure: tokens and raw argument bytes keep
		// arriving, but the displayed/source line count never moves again.
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case ch <- sonutruntime.StreamEvent{
					Type:              sonutruntime.StreamEventToolCallDelta,
					ToolCallID:        "write-frozen",
					ArgumentsFragment: "xxxxxxxx",
				}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

func TestParsePartialWriteArgs_complete(t *testing.T) {
	j := `{"path":"foo/bar.go","content":"package main\nfunc main() {}\n"}`
	path, content := parsePartialWriteArgs(j)
	if path != "foo/bar.go" {
		t.Errorf("path = %q, want foo/bar.go", path)
	}
	if !strings.Contains(content, "package main") {
		t.Errorf("content = %q, expected package main", content)
	}
}

func TestParsePartialEditArgs_newString(t *testing.T) {
	// Oversized edit_file rewrite: salvage new_string as write content.
	j := `{"path":"index.html","old_string":"<!DOCTYPE html>\n<html>\n","new_string":"<!DOCTYPE html>\n<html>\n<body>hi</body>\n</html>\n`
	path, content := parsePartialEditArgs(j)
	if path != "index.html" {
		t.Fatalf("path = %q", path)
	}
	if !strings.Contains(content, "<body>hi</body>") {
		t.Fatalf("content = %q", content)
	}
	if len(content) < minEditSalvageBytes && strings.Count(content, "\n") < 2 {
		// Small fixture is fine for parse; salvage gate is separate.
	}
}

func TestParsePartialWriteArgs_truncatedJSON(t *testing.T) {
	// A write_file call truncated mid-content (no closing quote or brace).
	j := `{"path":"src/main.go","content":"package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n\tx := `
	path, content := parsePartialWriteArgs(j)
	if path != "src/main.go" {
		t.Errorf("path = %q, want src/main.go", path)
	}
	if content == "" {
		t.Fatal("expected non-empty content")
	}
	// Must be trimmed to the last complete line — the dangling `x := ` is dropped.
	if !strings.HasSuffix(content, "\n") {
		t.Errorf("content should end at a complete line, got: %q", content)
	}
	if strings.Contains(content, "x :=") {
		t.Errorf("half-written final line should be trimmed, got: %q", content)
	}
}

func TestParsePartialWriteArgs_noCompleteLine(t *testing.T) {
	// One line, no newline → nothing complete to save.
	j := `{"path":"x.go","content":"incomplete line without newline`
	_, content := parsePartialWriteArgs(j)
	if content != "" {
		t.Errorf("expected empty content for no complete line, got %q", content)
	}
}

func TestRepeatedFileContentPrefixStopsRecursiveIdentifier(t *testing.T) {
	content := strings.Repeat("let particlesToSpawnInc", 1) + strings.Repeat("Dec", 12) + "\n"
	content += strings.Repeat("const particle = {};\n", 4)
	if clean, repeated := repeatedFileContentPrefix(content); repeated || clean != "" {
		// The identifier-only fixture is intentionally below the detector's
		// minimum body size; a short legitimate snippet must not be interrupted.
		t.Fatalf("short content triggered repetition detector: repeated=%v clean=%q", repeated, clean)
	}

	content = strings.Repeat("const particle = {};\n", 12)
	content += "let particlesToSpawnInc" + strings.Repeat("Dec", 12) + "\n"
	clean, repeated := repeatedFileContentPrefix(content)
	if !repeated {
		t.Fatal("expected recursive identifier to trigger repetition detector")
	}
	if !strings.HasSuffix(clean, "const particle = {};\n") {
		t.Fatalf("clean prefix lost the complete preceding line: %q", clean)
	}
}

func TestRepeatedFileContentPrefixStopsRepeatedLongLines(t *testing.T) {
	line := "    particles.push({ x: cx, y: cy, color: randomColor() });"
	content := strings.Repeat("const header = true;\n", 8) + strings.Repeat(line+"\n", 3)
	clean, repeated := repeatedFileContentPrefix(content)
	if !repeated || !strings.HasSuffix(clean, "const header = true;\n") {
		t.Fatalf("expected repeated long lines to be removed: repeated=%v clean=%q", repeated, clean)
	}
}

func TestRepeatedFileGenerationSavesCleanPrefixAndContinues(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	registry.Register(tools.NewWriteFileTool(root))
	provider := &repeatedFileProvider{}
	var results []ToolResult

	result, err := Run(context.Background(), "create game.js", provider, Options{
		Registry:       registry,
		FileTracker:    tools.NewFileTracker(),
		Cwd:            root,
		PermissionMode: PermissionModeAuto,
		MaxTurns:       2,
		OnToolResult: func(result ToolResult) {
			results = append(results, result)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("FinalAnswer = %q, want done", result.FinalAnswer)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want one interrupted generation plus one continuation", provider.calls)
	}
	if len(results) != 1 || results[0].Status != tools.StatusOK {
		t.Fatalf("tool results = %#v, want one successful recovered write", results)
	}
	if results[0].Meta["partial_recovery"] != "true" {
		t.Fatalf("recovery meta = %#v, want partial_recovery=true", results[0].Meta)
	}
	want := strings.Repeat("const particle = {};\n", 16)
	got, err := os.ReadFile(filepath.Join(root, "game.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("saved clean prefix = %q, want %q", string(got), want)
	}
}

func TestAgentLoopRejectsExtensionlessHTMLStream(t *testing.T) {
	root := t.TempDir()
	body := "<!doctype html>\n<html><body><canvas id=\"game\"></canvas><script>requestAnimationFrame(()=>{});</script></body></html>\n"
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("write-html", "write_file", `{"path":"super","content":`+quoteJSONString(body)+`}`),
		textTurn("done"),
	}}
	registry := tools.NewRegistry()
	registry.Register(tools.NewWriteFileTool(root))
	var results []ToolResult
	result, err := Run(context.Background(), "create a playable HTML game", provider, Options{
		Registry:       registry,
		FileTracker:    tools.NewFileTracker(),
		Cwd:            root,
		PermissionMode: PermissionModeAuto,
		MaxTurns:       2,
		OnToolResult: func(toolResult ToolResult) {
			results = append(results, toolResult)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("FinalAnswer = %q, want done", result.FinalAnswer)
	}
	if _, statErr := os.Stat(filepath.Join(root, "super")); !os.IsNotExist(statErr) {
		t.Fatalf("extensionless HTML target was created, stat error = %v", statErr)
	}
	if len(results) != 1 || results[0].Status != tools.StatusError || !strings.Contains(results[0].Output, ".html") {
		t.Fatalf("tool results = %#v, want one .html correction error", results)
	}
}

func TestParsePartialWriteArgs_aliases(t *testing.T) {
	// Weaker models sometimes use file_path/text aliases.
	j := `{"file_path":"a/b.txt","text":"line one\nline two\n`
	path, content := parsePartialWriteArgs(j)
	if path != "a/b.txt" {
		t.Errorf("path = %q, want a/b.txt", path)
	}
	if content != "line one\nline two\n" {
		t.Errorf("content = %q", content)
	}
}

func TestParsePartialAppendArgsTrimsIncompleteFinalLine(t *testing.T) {
	j := `{"path":"game.js","content":"function start() {\n  play();\n  const unfinished = `
	path, content := parsePartialAppendArgs(j)
	if path != "game.js" {
		t.Fatalf("path = %q, want game.js", path)
	}
	if content != "function start() {\n  play();\n" {
		t.Fatalf("content = %q, want only complete lines", content)
	}
}

func TestDecodePartialJSONString(t *testing.T) {
	cases := []struct{ in, want string }{
		{`hello\nworld"rest`, "hello\nworld"},
		{`line1\nline2\n`, "line1\nline2\n"},
		{`tab\there"`, "tab\there"},
		{`slash\/path"`, "slash/path"},
		{`backslash\\"`, `backslash\`},
		{`dangling\`, "dangling"}, // truncated at backslash
		{`ok\nmore\`, "ok\nmore"}, // truncated at a later backslash
		{`unicodeA"`, "unicodeA"},
		{`badunicode\u00`, "badunicode"}, // truncated \u escape
	}
	for _, tc := range cases {
		if got := decodePartialJSONString(tc.in); got != tc.want {
			t.Errorf("decodePartialJSONString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPartialWriteRecoveryThroughRegistry exercises the real recovery path: a
// truncated write_file fragment is parsed and persisted through the actual
// write_file tool, and a follow-up append_file resolves to the SAME file, so
// the two chunks concatenate correctly.
func TestPartialWriteRecoveryThroughRegistry(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreWriteTools(root) {
		registry.Register(tool)
	}
	tracker := tools.NewFileTracker()

	// A write_file cut off mid-stream after two complete lines.
	partial := `{"path":"out/app.go","content":"package main\n\nfunc main() {\n`
	path, content := parsePartialWriteArgs(partial)
	if path == "" || content == "" {
		t.Fatalf("parse failed: path=%q content=%q", path, content)
	}

	recovery := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path":      path,
		"content":   content,
		"overwrite": true,
	}, tools.RunOptions{FileTracker: tracker, Cwd: root, PermissionGranted: true})
	if recovery.Status == tools.StatusError {
		t.Fatalf("recovery write failed: %s", recovery.Output)
	}
	if len(recovery.ChangedFiles) == 0 {
		t.Fatal("expected ChangedFiles from recovery write")
	}
	savedPath := recovery.ChangedFiles[0]

	// The model continues with append_file at the same resolved path.
	appendRes := registry.RunWithOptions(context.Background(), "append_file", map[string]any{
		"path":    savedPath,
		"content": "\tprintln(\"done\")\n}\n",
	}, tools.RunOptions{FileTracker: tracker, Cwd: root, PermissionGranted: true})
	if appendRes.Status == tools.StatusError {
		t.Fatalf("append failed: %s", appendRes.Output)
	}

	got, err := os.ReadFile(filepath.Join(root, "out/app.go"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "package main\n\nfunc main() {\n\tprintln(\"done\")\n}\n"
	if string(got) != want {
		t.Errorf("concatenated file = %q, want %q", string(got), want)
	}
}

// TestPartialWriteRecovery_duplicatedRootPath verifies recovery honors the
// hallucinated-path remap: a path that duplicates the workspace folder name is
// landed at the root by write_file, and append_file resolves identically, so
// the chunks still meet on one file (they would diverge with a naive Join).
func TestPartialWriteRecovery_duplicatedRootPath(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "myproj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreWriteTools(root) {
		registry.Register(tool)
	}
	tracker := tools.NewFileTracker()

	// Model emitted "myproj/index.html" while cwd is already .../myproj.
	partial := `{"path":"myproj/index.html","content":"<html>\n<body>\n`
	path, content := parsePartialWriteArgs(partial)

	recovery := registry.RunWithOptions(context.Background(), "write_file", map[string]any{
		"path": path, "content": content, "overwrite": true,
	}, tools.RunOptions{FileTracker: tracker, Cwd: root, PermissionGranted: true})
	if recovery.Status == tools.StatusError {
		t.Fatalf("recovery failed: %s", recovery.Output)
	}
	savedPath := recovery.ChangedFiles[0]

	appendRes := registry.RunWithOptions(context.Background(), "append_file", map[string]any{
		"path": savedPath, "content": "</body>\n</html>\n",
	}, tools.RunOptions{FileTracker: tracker, Cwd: root, PermissionGranted: true})
	if appendRes.Status == tools.StatusError {
		t.Fatalf("append failed: %s", appendRes.Output)
	}

	// Both chunks must have landed on the SAME file (at the remapped location).
	full := filepath.Join(root, savedPath)
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", full, err)
	}
	if string(got) != "<html>\n<body>\n</body>\n</html>\n" {
		t.Errorf("file = %q — chunks did not concatenate on one path", string(got))
	}
}

// TestOversizedAppendIsPersistedAsSuccessfulChunk reproduces the screenshot:
// the model streams an append larger than the collector cap. The loop must save
// its complete lines, report a successful append (not a fake "Recovering…"
// failure), and continue to the next model turn.
func TestOversizedAppendIsPersistedAsSuccessfulChunk(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "game.js")
	if err := os.WriteFile(path, []byte("// prefix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.Register(tools.NewAppendFileTool(root))
	tracker := tools.NewFileTracker()

	// One fragment exceeds the production file-arg ceiling and ends in an
	// incomplete final line. Complete lines before it must survive exactly once.
	// Must exceed defaultMaxFileToolArgBytes (768 KiB) so the collector aborts.
	completeLine := strings.Repeat("x", 400) + "\n"
	complete := strings.Repeat(completeLine, 2200)
	arguments := `{"path":"game.js","content":` + quoteJSONString(complete+"unfinished")
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "append-1", ToolName: "append_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "append-1", ArgumentsFragment: arguments},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "append-1"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventText, Content: "done"},
			{Type: sonutruntime.StreamEventDone},
		},
	}}
	var results []ToolResult
	result, err := Run(context.Background(), "finish game.js", provider, Options{
		Registry:       registry,
		FileTracker:    tracker,
		PermissionMode: PermissionModeAuto,
		MaxTurns:       2,
		OnToolResult: func(result ToolResult) {
			results = append(results, result)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("FinalAnswer = %q, want done", result.FinalAnswer)
	}
	if len(results) != 1 || results[0].Status != tools.StatusOK {
		t.Fatalf("tool results = %#v, want one successful recovered append", results)
	}
	if results[0].Meta["partial_recovery"] != "true" {
		t.Fatalf("recovery meta = %#v, want partial_recovery=true", results[0].Meta)
	}
	if strings.Contains(results[0].Output, "Error:") {
		t.Fatalf("successful append recovery was mislabeled as an error: %q", results[0].Output)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "// prefix\n" + complete
	if string(got) != want {
		t.Fatalf("recovered file size/content mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestOversizedWritePrefixIsProductiveNotFailure(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	registry.Register(tools.NewWriteFileTool(root))
	tracker := tools.NewFileTracker()

	// Exceed the 768 KiB file-arg ceiling so the call is aborted mid-stream.
	completeLine := strings.Repeat("y", 400) + "\n"
	complete := strings.Repeat(completeLine, 2200)
	arguments := `{"path":"game.js","content":` + quoteJSONString(complete+"unfinished")
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "write-1", ToolName: "write_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "write-1", ArgumentsFragment: arguments},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "write-1"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventText, Content: "done"},
			{Type: sonutruntime.StreamEventDone},
		},
	}}
	var results []ToolResult
	_, err := Run(context.Background(), "create game.js", provider, Options{
		Registry:       registry,
		FileTracker:    tracker,
		PermissionMode: PermissionModeAuto,
		MaxTurns:       2,
		OnToolResult: func(result ToolResult) {
			results = append(results, result)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != tools.StatusOK {
		t.Fatalf("recovered write results = %#v, want one productive success", results)
	}
	if results[0].Meta["partial_recovery"] != "true" {
		t.Fatalf("recovered write meta = %#v", results[0].Meta)
	}
	got, err := os.ReadFile(filepath.Join(root, "game.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != complete {
		t.Fatalf("saved prefix mismatch: got %d bytes, want %d", len(got), len(complete))
	}
}

// Regression: recovery of a truncated write must never replace a valid file
// with the generated prefix. This was the destructive sequence visible in the
// production logs: a good HTML write succeeded, a later oversized rewrite
// stalled, and recovery overwrote the good file with fewer/incomplete lines.
func TestOversizedWriteRecoveryPreservesExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "search.html")
	const original = "<!doctype html>\n<html>\n<body>working</body>\n</html>\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.Register(tools.NewWriteFileTool(root))
	tracker := tools.NewFileTracker()

	// Exceed the 768 KiB file-arg ceiling so recovery runs and refuses to
	// clobber the existing good file with a truncated rewrite prefix.
	completeLine := strings.Repeat("z", 400) + "\n"
	partialRewrite := strings.Repeat(completeLine, 2200) + "unfinished"
	arguments := `{"path":"search.html","content":` + quoteJSONString(partialRewrite)
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "write-existing", ToolName: "write_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "write-existing", ArgumentsFragment: arguments},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "write-existing"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventText, Content: "done"},
			{Type: sonutruntime.StreamEventDone},
		},
	}}
	var results []ToolResult
	result, err := Run(context.Background(), "improve search.html", provider, Options{
		Registry:       registry,
		FileTracker:    tracker,
		Cwd:            root,
		PermissionMode: PermissionModeAuto,
		MaxTurns:       2,
		OnToolResult: func(result ToolResult) {
			results = append(results, result)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("FinalAnswer = %q, want done", result.FinalAnswer)
	}
	if len(results) != 1 || results[0].Status != tools.StatusError {
		t.Fatalf("results = %#v, want one fail-closed recovery result", results)
	}
	if !strings.Contains(results[0].Output, "preserved") {
		t.Fatalf("recovery did not explain preservation: %q", results[0].Output)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("existing file was corrupted:\n got %q\nwant %q", string(got), original)
	}
}

func TestOversizedEditRecoveryRejectsAppendToPreservedTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "search.html")
	const original = "<!doctype html>\n<html>\n<body>working</body>\n</html>\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreWriteTools(root) {
		registry.Register(tool)
	}

	// The first call is a whole-file edit_file misuse and trips the 4 KiB edit
	// cap. The next turn reproduces the destructive production response: append
	// the generated CSS fragment directly to the complete HTML target.
	largeEdit := `{"path":"search.html","old_string":"</head>","new_string":"` +
		strings.Repeat(".result { color: blue; }\\n", 300)
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "edit-large", ToolName: "edit_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "edit-large", ArgumentsFragment: largeEdit},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "append-bad", ToolName: "append_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "append-bad", ArgumentsFragment: `{"path":"search.html","content":"<style>\n.bad{}\n</style>\n"}`},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "append-bad"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventText, Content: "done"},
			{Type: sonutruntime.StreamEventDone},
		},
	}}
	var results []ToolResult
	result, err := Run(context.Background(), "style search.html", provider, Options{
		Registry:       registry,
		FileTracker:    tools.NewFileTracker(),
		Cwd:            root,
		PermissionMode: PermissionModeAuto,
		MaxTurns:       3,
		OnToolResult: func(result ToolResult) {
			results = append(results, result)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("FinalAnswer = %q, want done", result.FinalAnswer)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want edit rejection + append rejection", results)
	}
	if !strings.Contains(results[1].Output, "append_file was rejected") {
		t.Fatalf("unsafe append was not rejected: %#v", results[1])
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("preserved target changed:\n got %q\nwant %q", string(got), original)
	}
}

func TestFrozenFileLinesRecoverCompletePrefixAndContinueAgent(t *testing.T) {
	t.Setenv("SONUT_FILE_LINE_STALL_TIMEOUT", "20ms")
	root := t.TempDir()
	registry := tools.NewRegistry()
	registry.Register(tools.NewWriteFileTool(root))
	provider := &frozenFileLineProvider{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var results []ToolResult
	result, err := Run(ctx, "create game.js", provider, Options{
		Registry:       registry,
		FileTracker:    tools.NewFileTracker(),
		PermissionMode: PermissionModeAuto,
		MaxTurns:       2,
		OnToolResult: func(result ToolResult) {
			results = append(results, result)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("FinalAnswer = %q, want done", result.FinalAnswer)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want recovery followed by one continuation", provider.calls)
	}
	if len(results) != 1 || results[0].Status != tools.StatusOK {
		t.Fatalf("tool results = %#v, want one successful recovered write", results)
	}
	if results[0].Meta["partial_recovery"] != "true" {
		t.Fatalf("recovery meta = %#v, want partial_recovery=true", results[0].Meta)
	}
	got, err := os.ReadFile(filepath.Join(root, "game.js"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "one\ntwo\nthree\nfour\nfive\n"
	if string(got) != want {
		t.Fatalf("recovered content = %q, want only complete lines %q", string(got), want)
	}
}

// TestStreamErrorWithPartialWriteSavesContent reproduces perf.log turns 12–20:
// a write_file mid-body is interrupted by a stream error. The harness must save
// complete lines and continue — not discard ToolCalls and re-prefill for minutes.
func TestStreamErrorWithPartialWriteSavesContent(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	registry.Register(tools.NewWriteFileTool(root))

	// Simulate collector runaway abort mid-write: ToolCall Aborted with partial
	// args (same shape the agent sees after byte/line stall). Provider returns
	// that as a completed stream with no Error so the abort path dispatches.
	// A second turn answers after salvage.
	partial := `{"path":"game.js","content":"line1\nline2\nline3\nunfin`
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "w1", ToolName: "write_file"},
			// Exceed line-stall via frozen provider pattern is heavy; inject a
			// complete-enough body then mark via huge fragment that trips cap.
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "w1", ArgumentsFragment: partial},
			// Force abort by exceeding file arg bytes with no closing quote.
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "w1", ArgumentsFragment: strings.Repeat("x", 800*1024)},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventText, Content: "continued"},
			{Type: sonutruntime.StreamEventDone},
		},
	}}
	var results []ToolResult
	result, err := Run(context.Background(), "create game.js", provider, Options{
		Registry:       registry,
		FileTracker:    tools.NewFileTracker(),
		Cwd:            root,
		PermissionMode: PermissionModeAuto,
		MaxTurns:       2,
		ProviderName:   "llama",
		Model:          "atcode-v1",
		OnToolResult: func(r ToolResult) {
			results = append(results, r)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "continued" {
		t.Fatalf("FinalAnswer = %q", result.FinalAnswer)
	}
	if len(results) == 0 {
		t.Fatal("expected salvaged write tool result, got none (content would have been discarded)")
	}
	if results[0].Status != tools.StatusOK {
		t.Fatalf("salvage status = %s output=%q", results[0].Status, results[0].Output)
	}
	got, err := os.ReadFile(filepath.Join(root, "game.js"))
	if err != nil {
		t.Fatalf("file not saved: %v", err)
	}
	if !strings.Contains(string(got), "line1\nline2\nline3\n") {
		t.Fatalf("saved content missing complete lines: %q", string(got))
	}
}

func TestContentDeltaForAppend(t *testing.T) {
	cases := []struct {
		name      string
		existing  string
		generated string
		want      string
	}{
		{"empty gen", "a\n", "", ""},
		{"empty existing", "", "new\n", "new\n"},
		{"identical", "a\nb\n", "a\nb\n", ""},
		{"prefix re-sent", "a\nb\n", "a\nb\nc\n", "c\n"},
		{"suffix already present", "a\nb\nc\n", "b\nc\n", ""},
		{"next chunk only", "a\nb\n", "c\nd\n", "c\nd\n"},
		// A repeated block can legitimately occur earlier in a source file; an
		// interior match is not proof that this chunk was already appended.
		{"interior match is still new", "header\nrepeated block repeated block repeated block\nfooter\n", "repeated block repeated block repeated block\nnext\n", "repeated block repeated block repeated block\nnext\n"},
		// Existing lacks the trailing newline the model re-sent; delta keeps the
		// newline so append reconstructs the intended body exactly.
		{"missing trailing nl on existing", "a\nb", "a\nb\nc\n", "\nc\n"},
	}
	for _, tc := range cases {
		if got := contentDeltaForAppend(tc.existing, tc.generated); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// TestWriteFileRestartAfterPartialAutoAppendsDelta is the production failure
// mode: partial write saved a prefix, then the model re-issued write_file with
// the prefix + new lines. Previously that body was discarded with an error;
// now only the new delta is appended so nothing is regenerated or lost.
func TestWriteFileRestartAfterPartialAutoAppendsDelta(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreWriteTools(root) {
		registry.Register(tool)
	}
	tracker := tools.NewFileTracker()

	// Turn 1: oversized write saves a prefix (exceeds 768 KiB file-arg ceiling).
	completeLine := strings.Repeat("p", 400) + "\n"
	prefix := strings.Repeat(completeLine, 2200)
	// Turn 2: model restarts write_file with prefix + new tail (complete JSON).
	tail := "function jump(){ return true; }\n"
	fullRewrite := prefix + tail

	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "w1", ToolName: "write_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "w1", ArgumentsFragment: `{"path":"game.js","content":` + quoteJSONString(prefix+"unfinished")},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "w1"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "w2", ToolName: "write_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "w2", ArgumentsFragment: `{"path":"game.js","content":` + quoteJSONString(fullRewrite) + `}`},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "w2"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventText, Content: "done"},
			{Type: sonutruntime.StreamEventDone},
		},
	}}
	var results []ToolResult
	result, err := Run(context.Background(), "build game.js", provider, Options{
		Registry:       registry,
		FileTracker:    tracker,
		Cwd:            root,
		PermissionMode: PermissionModeAuto,
		MaxTurns:       3,
		OnToolResult: func(r ToolResult) {
			results = append(results, r)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("FinalAnswer = %q, want done", result.FinalAnswer)
	}
	if len(results) < 2 {
		t.Fatalf("results = %#v, want partial save + auto-append", results)
	}
	// Second result must be the auto-append conversion, not a discard error.
	second := results[1]
	if second.Status != tools.StatusOK {
		t.Fatalf("second result failed: %#v", second)
	}
	if second.Meta["auto_append_delta"] != "true" && !strings.Contains(second.Output, "Auto-continued") {
		t.Fatalf("expected auto-append delta conversion, got %#v", second)
	}
	got, err := os.ReadFile(filepath.Join(root, "game.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fullRewrite {
		t.Fatalf("file mismatch: got %d bytes want %d; tail=%q",
			len(got), len(fullRewrite), lastNLines(string(got), 2))
	}
}

// TestAbortedWriteAfterPartialAppendsDelta: first partial lands, second aborted
// write_file (mid-stream) must append complete new lines instead of fail-closed
// discard ("Large file write stopped safely" with content lost).
func TestAbortedWriteAfterPartialAppendsDelta(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreWriteTools(root) {
		registry.Register(tool)
	}

	completeLine := strings.Repeat("a", 400) + "\n"
	prefix := strings.Repeat(completeLine, 2200)
	// Second call: re-sends prefix + more lines, then aborts mid-stream.
	more := strings.Repeat(strings.Repeat("b", 400)+"\n", 2200)

	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "w1", ToolName: "write_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "w1", ArgumentsFragment: `{"path":"level.js","content":` + quoteJSONString(prefix+"unfinished")},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "w1"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "w2", ToolName: "write_file"},
			{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "w2", ArgumentsFragment: `{"path":"level.js","content":` + quoteJSONString(prefix+more+"unfinished")},
			{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "w2"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventText, Content: "done"},
			{Type: sonutruntime.StreamEventDone},
		},
	}}
	var results []ToolResult
	_, err := Run(context.Background(), "build level.js", provider, Options{
		Registry:       registry,
		FileTracker:    tools.NewFileTracker(),
		Cwd:            root,
		PermissionMode: PermissionModeAuto,
		MaxTurns:       3,
		OnToolResult: func(r ToolResult) {
			results = append(results, r)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 2 {
		t.Fatalf("results = %#v", results)
	}
	for i, r := range results {
		if r.Status == tools.StatusError && strings.Contains(r.Output, "could not save") {
			t.Fatalf("result[%d] lost content: %q", i, r.Output)
		}
	}
	got, err := os.ReadFile(filepath.Join(root, "level.js"))
	if err != nil {
		t.Fatal(err)
	}
	// Prefix + complete lines of `more` must be on disk (not just the first prefix).
	if !strings.HasPrefix(string(got), prefix) {
		t.Fatal("saved prefix missing")
	}
	if len(got) <= len(prefix) {
		t.Fatalf("second aborted write did not append new lines: got %d bytes (prefix %d)", len(got), len(prefix))
	}
	if !strings.Contains(string(got), strings.Repeat("b", 400)) {
		t.Fatal("new b-lines from second generation were lost")
	}
}
