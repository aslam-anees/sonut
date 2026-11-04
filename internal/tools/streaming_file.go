package tools

// Streaming file mutations deliberately have two phases:
//
//   1. Feed decoded argument bytes into a per-call sibling staging file while
//      the model is still generating the tool arguments.
//   2. Commit that staging file with an atomic rename only after the complete
//      JSON call has gone through the normal permission, sandbox, and hook
//      checks.
//
// This keeps large generations out of memory without exposing a half-written
// target. An interrupted generation leaves the sibling stream file behind as
// recoverable evidence instead of silently discarding the work.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aistatine/sonut/internal/sandbox"
	"github.com/aistatine/sonut/internal/sonuttrash"
)

// StreamingFileSession is the optional runtime sink implemented by file
// mutation tools. Feed is intentionally fire-and-forget: a provider may split
// JSON at any byte boundary. PrepareCommit validates the completed arguments
// and returns a commit closure so the registry can run it after permission and
// before-tool checks have passed.
type StreamingFileSession interface {
	Feed(fragment string)
	PrepareCommit(ctx context.Context, arguments string) (func() Result, bool)
	Recovery() (StreamingFileRecovery, bool)
	Close()
}

// StreamingFileRecovery describes bytes that were safely staged before a
// provider stream stopped. Content is the generated portion only: for an
// append_file session the existing file prefix is removed. The target is never
// changed by Recovery; the agent decides whether to append, continue a new
// file, or keep an existing complete file untouched.
type StreamingFileRecovery struct {
	ToolCallID string
	ToolName   string
	Path       string
	StagePath  string
	Content    string
	Existing   bool
}

// StreamingTool is implemented by file mutation tools that can persist their
// streamed arguments while the model is generating them.
type StreamingTool interface {
	StartStreaming(ctx context.Context, options RunOptions) StreamingFileSession
}

type streamingFileKind string

const (
	streamingWrite  streamingFileKind = "write_file"
	streamingAppend streamingFileKind = "append_file"
	streamingEdit   streamingFileKind = "edit_file"
)

const streamingStageFlushBytes = 16 * 1024

// Keep the sibling stage durable while generation is in flight without doing
// an fsync for every token-sized argument fragment. The target is still
// promoted atomically only after the complete call passes normal validation.
const streamingStageSyncInterval = 250 * time.Millisecond

// streamingStringDecoder is a small incremental JSON string decoder. It only
// decodes one named top-level string field, which is enough for the file tools
// and avoids reparsing the entire growing argument buffer on every delta.
type streamingStringDecoder struct {
	keys       []string
	head       []byte
	started    bool
	closed     bool
	pendingEsc bool
	unicode    []byte
	value      strings.Builder
	collect    bool
	pending    []byte
	onChunk    func([]byte) error
}

func newStreamingStringDecoder(keys []string, collect bool, onChunk func([]byte) error) *streamingStringDecoder {
	return &streamingStringDecoder{keys: keys, collect: collect, onChunk: onChunk}
}

func (decoder *streamingStringDecoder) Feed(fragment string) error {
	if decoder == nil || decoder.closed || fragment == "" {
		return nil
	}
	if !decoder.started {
		decoder.head = append(decoder.head, fragment...)
		start := streamingJSONValueStart(decoder.head, decoder.keys)
		if start < 0 {
			// Paths and file argument names occur near the front of a tool call.
			// Keep a bounded tail if a malformed model response never supplies the
			// requested field, rather than allowing this helper to grow forever.
			if len(decoder.head) > 128*1024 {
				decoder.head = append([]byte(nil), decoder.head[len(decoder.head)-64*1024:]...)
			}
			return nil
		}
		decoder.started = true
		remaining := append([]byte(nil), decoder.head[start:]...)
		decoder.head = nil
		return decoder.consume(remaining)
	}
	return decoder.consume([]byte(fragment))
}

func (decoder *streamingStringDecoder) consume(fragment []byte) error {
	for _, current := range fragment {
		if len(decoder.unicode) > 0 {
			if !isHexDigit(current) {
				decoder.emit([]byte("�"))
				decoder.unicode = nil
				// Continue processing the malformed byte as ordinary content. The
				// complete JSON validation at commit time will reject it.
			} else {
				decoder.unicode = append(decoder.unicode, current)
				if len(decoder.unicode) == 4 {
					value, err := strconv.ParseUint(string(decoder.unicode), 16, 16)
					if err != nil {
						return err
					}
					decoder.emit(encodeStreamingRune(rune(value)))
					decoder.unicode = nil
				}
			}
			continue
		}
		if decoder.pendingEsc {
			decoder.pendingEsc = false
			if current == 'u' {
				decoder.unicode = decoder.unicode[:0]
				continue
			}
			switch current {
			case 'n':
				decoder.emit([]byte{'\n'})
			case 't':
				decoder.emit([]byte{'\t'})
			case 'r':
				decoder.emit([]byte{'\r'})
			case 'b':
				decoder.emit([]byte{'\b'})
			case 'f':
				decoder.emit([]byte{'\f'})
			case '"', '\\', '/':
				decoder.emit([]byte{current})
			default:
				decoder.emit([]byte{current})
			}
			continue
		}
		switch current {
		case '\\':
			decoder.pendingEsc = true
		case '"':
			decoder.closed = true
			return decoder.Flush()
		default:
			decoder.emit([]byte{current})
		}
	}
	return decoder.Flush()
}

func (decoder *streamingStringDecoder) emit(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if decoder.collect {
		_, _ = decoder.value.Write(chunk)
	}
	if decoder.onChunk == nil {
		return
	}
	decoder.pending = append(decoder.pending, chunk...)
	if len(decoder.pending) >= streamingStageFlushBytes {
		_ = decoder.Flush()
	}
}

func (decoder *streamingStringDecoder) Flush() error {
	if decoder == nil || len(decoder.pending) == 0 || decoder.onChunk == nil {
		return nil
	}
	chunk := append([]byte(nil), decoder.pending...)
	decoder.pending = decoder.pending[:0]
	return decoder.onChunk(chunk)
}

func (decoder *streamingStringDecoder) Value() string {
	if decoder == nil {
		return ""
	}
	return decoder.value.String()
}

func (decoder *streamingStringDecoder) Started() bool { return decoder != nil && decoder.started }
func (decoder *streamingStringDecoder) Closed() bool {
	return decoder != nil && decoder.closed && len(decoder.unicode) == 0 && !decoder.pendingEsc
}

func streamingJSONValueStart(raw []byte, keys []string) int {
	for _, key := range keys {
		marker := []byte(`"` + key + `"`)
		from := 0
		for from < len(raw) {
			index := bytes.Index(raw[from:], marker)
			if index < 0 {
				break
			}
			index += from
			i := skipJSONSpaceBytes(raw, index+len(marker))
			if i < len(raw) && raw[i] == ':' {
				i = skipJSONSpaceBytes(raw, i+1)
				if i < len(raw) && raw[i] == '"' {
					return i + 1
				}
			}
			from = index + len(marker)
		}
	}
	return -1
}

func skipJSONSpaceBytes(raw []byte, index int) int {
	for index < len(raw) {
		switch raw[index] {
		case ' ', '\t', '\r', '\n':
			index++
		default:
			return index
		}
	}
	return index
}

func isHexDigit(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

// utf8.EncodeRune accepts a byte slice in the standard library only through
// utf8.RuneLen/EncodeRune's []byte-free signature. Keep this tiny wrapper here
// to make the decoder's escape handling easy to read.
func encodeStreamingRune(value rune) []byte {
	if value >= 0xD800 && value <= 0xDFFF {
		return []byte("�")
	}
	var encoded [utf8.UTFMax]byte
	n := utf8.EncodeRune(encoded[:], value)
	return encoded[:n]
}

type streamingFileSession struct {
	mu             sync.Mutex
	kind           streamingFileKind
	workspace      string
	scope          PathScope
	options        RunOptions
	path           *streamingStringDecoder
	content        *streamingStringDecoder
	oldString      *streamingStringDecoder
	newString      *streamingStringDecoder
	prepared       bool
	disabled       bool
	writeErr       error
	target         string
	relative       string
	stage          string
	stageFile      *os.File
	stageSinceSync int64
	lastStageSync  time.Time
	pendingData    []byte
	original       []byte
	existed        bool
	newWritten     int
	editStarted    bool
	editSuffix     bool
	closed         bool
	commitReady    bool
}

func newStreamingFileSession(kind streamingFileKind, workspace string, scope PathScope, options RunOptions) *streamingFileSession {
	session := &streamingFileSession{
		kind:      kind,
		workspace: normalizeWorkspaceRoot(workspace),
		scope:     scope,
		options:   options,
	}
	session.path = newStreamingStringDecoder([]string{"path", "file", "file_path", "filepath", "filename", "target_file"}, true, nil)
	switch kind {
	case streamingEdit:
		session.oldString = newStreamingStringDecoder([]string{"old_string", "old", "search", "find", "old_str"}, true, nil)
		session.newString = newStreamingStringDecoder([]string{"new_string", "new_content", "replacement_content", "content", "contents", "new", "replace", "replacement", "new_str"}, true, nil)
	default:
		session.content = newStreamingStringDecoder([]string{"content", "contents", "text", "body", "data", "file_content"}, false, nil)
	}
	return session
}

func (session *streamingFileSession) Feed(fragment string) {
	if session == nil || fragment == "" {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.disabled {
		return
	}
	if err := session.path.Feed(fragment); err != nil {
		session.disable(err)
		return
	}
	// Do not resolve the target from a path prefix. Local providers stream tool
	// arguments character-by-character, so `supermario.html` briefly appears as
	// `super`; resolving at that point would stage and later publish the body at
	// the wrong extensionless target. Wait for the JSON string's closing quote,
	// then resolve the complete path once.
	if session.target == "" && session.path.Closed() {
		if !session.prepareTarget() {
			return
		}
	}
	if session.kind == streamingEdit {
		if err := session.oldString.Feed(fragment); err != nil {
			session.disable(err)
			return
		}
		if err := session.newString.Feed(fragment); err != nil {
			session.disable(err)
			return
		}
		session.syncEditStage()
		return
	}
	if session.content != nil {
		if err := session.content.Feed(fragment); err != nil {
			session.disable(err)
		}
	}
}

func (session *streamingFileSession) prepareTarget() bool {
	requestedPath := session.path.Value()
	if requestedPath == "" {
		return false
	}
	var (
		absolute string
		relative string
		err      error
	)
	if session.kind == streamingEdit {
		absolute, relative, err = resolveScopedPath(session.workspace, session.scope, requestedPath, session.options.Cwd)
	} else {
		absolute, relative, err = resolveScopedTargetPath(session.workspace, session.scope, requestedPath, session.options.Cwd)
	}
	if err != nil {
		session.disable(err)
		return false
	}
	if session.options.Sandbox != nil {
		decision := session.options.Sandbox.Evaluate(context.Background(), sandbox.Request{
			WorkspaceRoot:     session.workspace,
			ToolName:          string(session.kind),
			SideEffect:        sandbox.SideEffectWrite,
			Permission:        sandbox.PermissionPrompt,
			PermissionGranted: session.options.PermissionGranted,
			PermissionMode:    sandbox.PermissionMode(session.options.PermissionMode),
			Args:              map[string]any{"path": requestedPath},
			Reason:            "streaming file mutation",
		})
		// A prompt is never guessed at stream time. The completed call still
		// receives its normal interactive permission decision.
		if decision.Action != sandbox.ActionAllow {
			session.disable(fmt.Errorf("sandbox did not pre-authorize streaming file mutation: %s", decision.ErrorString()))
			return false
		}
	}
	if err := recheckScopedWriteTargetAt(session.workspace, session.scope, requestedPath, session.options.Cwd); err != nil {
		session.disable(err)
		return false
	}
	current, readErr := os.ReadFile(absolute)
	if readErr != nil && !os.IsNotExist(readErr) {
		session.disable(readErr)
		return false
	}
	session.target = absolute
	session.relative = relative
	session.original = append([]byte(nil), current...)
	session.existed = readErr == nil
	if session.kind == streamingEdit {
		return true
	}
	if !session.openStage() {
		return false
	}
	if session.kind == streamingAppend && len(current) > 0 {
		if _, err := session.stageFile.Write(current); err != nil {
			session.disable(err)
			return false
		}
	}
	if len(session.pendingData) > 0 {
		if _, err := session.stageFile.Write(session.pendingData); err != nil {
			session.disable(err)
			return false
		}
		session.pendingData = nil
	}
	session.prepared = true
	return true
}

func (session *streamingFileSession) openStage() bool {
	if session.stageFile != nil {
		return true
	}
	sum := sha256.Sum256([]byte(session.options.ToolCallID))
	suffix := hex.EncodeToString(sum[:])[:12]
	// Never reuse a deterministic stage name: reconnects and repeated tool-call
	// ids must not truncate bytes from an earlier interrupted generation.
	session.stage = fmt.Sprintf("%s.sonut.stream.%s.%d", session.target, suffix, time.Now().UnixNano())
	if info, err := os.Lstat(session.stage); err == nil && info.Mode()&os.ModeSymlink != 0 {
		session.disable(fmt.Errorf("stream staging path is a symlink"))
		return false
	}
	if err := os.MkdirAll(filepath.Dir(session.stage), 0o755); err != nil {
		session.disable(err)
		return false
	}
	file, err := os.OpenFile(session.stage, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		session.disable(err)
		return false
	}
	session.stageFile = file
	_ = syncDirectory(filepath.Dir(session.stage))
	session.lastStageSync = time.Now()
	return true
}

func (session *streamingFileSession) syncEditStage() {
	if session.kind != streamingEdit || session.disabled || session.target == "" || !session.oldString.Closed() || !session.newString.Started() {
		return
	}
	old := session.oldString.Value()
	if old == "" {
		return
	}
	if !session.editStarted {
		if strings.Count(string(session.original), old) != 1 {
			session.disable(fmt.Errorf("streamed edit anchor is not unique"))
			return
		}
		if !session.openStage() {
			return
		}
		index := bytes.Index(session.original, []byte(old))
		if index < 0 {
			session.disable(fmt.Errorf("streamed edit anchor was not found"))
			return
		}
		if _, err := session.stageFile.Write(session.original[:index]); err != nil {
			session.disable(err)
			return
		}
		session.editStarted = true
	}
	newValue := session.newString.Value()
	if len(newValue) <= session.newWritten {
		return
	}
	if _, err := session.stageFile.Write([]byte(newValue[session.newWritten:])); err != nil {
		session.disable(err)
		return
	}
	session.newWritten = len(newValue)
	if session.newString.Closed() && !session.editSuffix {
		index := bytes.Index(session.original, []byte(old))
		end := index + len(old)
		if end >= 0 && end <= len(session.original) {
			if _, err := session.stageFile.Write(session.original[end:]); err != nil {
				session.disable(err)
			} else {
				session.editSuffix = true
			}
		}
	}
	_ = session.stageFile.Sync()
}

func (session *streamingFileSession) writeContentChunk(chunk []byte) error {
	if session.disabled {
		return nil
	}
	if session.stageFile == nil {
		session.pendingData = append(session.pendingData, chunk...)
		return nil
	}
	if _, err := session.stageFile.Write(chunk); err != nil {
		session.disable(err)
		return err
	}
	session.stageSinceSync += int64(len(chunk))
	if session.stageSinceSync >= streamingStageFlushBytes || time.Since(session.lastStageSync) >= streamingStageSyncInterval {
		if err := session.stageFile.Sync(); err != nil {
			session.disable(err)
			return err
		}
		session.stageSinceSync = 0
		session.lastStageSync = time.Now()
	}
	return nil
}

func (session *streamingFileSession) PrepareCommit(ctx context.Context, arguments string) (func() Result, bool) {
	if session == nil {
		return nil, false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.disabled || session.target == "" {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false
	}
	if session.kind == streamingEdit {
		session.syncEditStage()
		if !session.editStarted || !session.oldString.Closed() || !session.newString.Closed() {
			return nil, false
		}
	} else if session.content == nil || !session.content.Closed() {
		return nil, false
	}
	if session.stageFile != nil {
		_ = session.stageFile.Sync()
		_ = session.stageFile.Close()
		session.stageFile = nil
	}
	args := map[string]any{}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, false
	}
	requestedPath, err := aliasedStringArg(args, []string{"path", "file", "file_path", "filepath", "filename", "target_file"}, "", false, false)
	if err != nil || requestedPath == "" || requestedPath != session.path.Value() {
		return nil, false
	}
	stageBytes, err := os.ReadFile(session.stage)
	if err != nil {
		return nil, false
	}
	if err := recheckScopedWriteTargetAt(session.workspace, session.scope, requestedPath, session.options.Cwd); err != nil {
		return nil, false
	}
	var expected []byte
	switch session.kind {
	case streamingWrite:
		content, contentErr := fileContentArg(args)
		if contentErr != nil {
			return nil, false
		}
		content, _ = stripToolProtocolResidue(content)
		if validateErr := validateGeneratedContentPath(requestedPath, content); validateErr != nil {
			// A streamed call has already bypassed the ordinary Run method by the
			// time it reaches this boundary. Return a precomputed error instead of
			// falling through to a second commit path: otherwise an HTML body at an
			// extensionless path can be promoted as `index` even though write_file
			// correctly rejects the same arguments when called directly.
			body := append([]byte(nil), stageBytes...)
			message := "Error: write_file content/path mismatch for " + requestedPath + ": " + validateErr.Error() + "."
			if hint := generatedContentPathHint(requestedPath, content); hint != "" {
				message += " " + hint
			}
			message += " The generated body was preserved; retry with the corrected path, overwrite: true, reuse_content: true, and omit content. Do not regenerate it."
			return func() Result {
				// The corrected path may differ from the rejected extensionless
				// spelling, so leave TargetPath empty and let reuse_content match the
				// durable artifact after the model supplies the corrected path.
				_, _ = saveGeneratedRecovery(session.workspace, "write_file", requestedPath, "", string(body))
				return errorResult(message)
			}, true
		}
		if !bytes.Equal(stageBytes, []byte(content)) {
			return nil, false
		}
		overwrite, boolErr := boolArg(args, "overwrite", false)
		if boolErr != nil {
			return nil, false
		}
		if session.existed && !overwrite {
			if _, tracked := session.options.FileTracker.Version(session.target); !tracked {
				return nil, false
			}
		}
		expected = append([]byte(nil), stageBytes...)
	case streamingAppend:
		content, contentErr := fileContentArg(args)
		if contentErr != nil || content == "" {
			return nil, false
		}
		content, _ = stripToolProtocolResidue(content)
		expected = append(append([]byte(nil), session.original...), []byte(content)...)
		if !bytes.Equal(stageBytes, expected) {
			return nil, false
		}
	case streamingEdit:
		oldString, oldErr := aliasedStringArg(args, []string{"old_string", "old", "search", "find", "old_str"}, "", true, false)
		newString, newErr := aliasedStringArg(args, []string{"new_string", "new_content", "replacement_content", "content", "contents", "new", "replace", "replacement", "new_str"}, "", true, true)
		if oldErr != nil || newErr != nil || oldString != session.oldString.Value() || newString != session.newString.Value() {
			return nil, false
		}
		replaceAll, boolErr := boolArg(args, "replace_all", false)
		if boolErr != nil {
			return nil, false
		}
		original := string(session.original)
		if replaceAll {
			expected = []byte(strings.ReplaceAll(original, oldString, newString))
		} else if strings.Count(original, oldString) == 1 {
			expected = []byte(strings.Replace(original, oldString, newString, 1))
		} else {
			return nil, false
		}
		if !bytes.Equal(stageBytes, expected) {
			return nil, false
		}
	}
	if bytes.Equal(session.original, expected) && session.kind != streamingWrite {
		return nil, false
	}
	if session.commitReady {
		return nil, false
	}
	session.commitReady = true
	target := session.target
	stage := session.stage
	relative := session.relative
	original := append([]byte(nil), session.original...)
	existed := session.existed
	kind := session.kind
	workspace := session.workspace
	tracker := session.options.FileTracker
	finalBytes := append([]byte(nil), expected...)
	return func() Result {
		if err := ctx.Err(); err != nil {
			return errorResult("Error committing streamed file mutation for " + relative + ": " + err.Error())
		}
		current, readErr := os.ReadFile(target)
		if existed {
			if readErr != nil || !bytes.Equal(current, original) {
				return errorResult(fileConflictMessage(relative))
			}
			if err := tracker.CheckConflict(target, current); err != nil {
				return errorResult(fileConflictMessage(relative))
			}
		} else if readErr == nil {
			return errorResult("Error committing streamed file mutation for " + relative + ": the target appeared while the model was generating it; the existing file was preserved.")
		} else if !os.IsNotExist(readErr) {
			return errorResult("Error reading " + relative + ": " + readErr.Error())
		}
		if err := recheckScopedWriteTargetAt(workspace, session.scope, requestedPath, session.options.Cwd); err != nil {
			return errorResult("Error writing " + relative + ": " + err.Error())
		}
		if kind == streamingWrite {
			if validateErr := validateGeneratedContentPath(requestedPath, string(finalBytes)); validateErr != nil {
				_, _ = saveGeneratedRecovery(workspace, "write_file", requestedPath, "", string(finalBytes))
				message := "Error: write_file content/path mismatch for " + requestedPath + ": " + validateErr.Error() + "."
				if hint := generatedContentPathHint(requestedPath, string(finalBytes)); hint != "" {
					message += " " + hint
				}
				return errorResult(message + " The generated body was preserved; retry with the corrected path, reuse_content: true, and omit content. Do not regenerate it.")
			}
		}
		if existed && kind == streamingWrite {
			_ = sonuttrash.Put(workspace, target)
			go sonuttrash.Sweep(workspace)
		}
		if err := os.Rename(stage, target); err != nil {
			return errorResult("Error committing streamed file mutation for " + relative + ": " + err.Error())
		}
		info, _ := os.Stat(target)
		tracker.Record(target, finalBytes, info)
		lines := countFileLines(finalBytes)
		switch kind {
		case streamingWrite:
			verb := "Created"
			if existed {
				verb = "Overwrote"
			}
			summary := fmt.Sprintf("%s %s (%d lines).", verb, relative, lines)
			result := okResult(summary)
			result.ChangedFiles = []string{relative}
			result.Display = Display{Summary: summary, Kind: "file", Preview: boundedUnifiedDiff(relative, string(original), string(finalBytes))}
			return result
		case streamingAppend:
			appended := len(finalBytes) - len(original)
			appendLines := countFileLines(finalBytes[len(original):])
			summary := fmt.Sprintf("Appended to %s (%d lines).", relative, appendLines)
			result := okResult(summary)
			result.ChangedFiles = []string{relative}
			result.Display = Display{Summary: summary, Kind: "file"}
			if total := countFileLines(finalBytes); total > 0 {
				result.Meta = map[string]string{"file.total_lines": strconv.Itoa(total), "stream.bytes": strconv.Itoa(appended)}
			}
			return result
		default:
			summary := fmt.Sprintf("Successfully edited %s.", relative)
			result := okResult(summary)
			result.ChangedFiles = []string{relative}
			result.Display = Display{Summary: fmt.Sprintf("Edited %s", relative), Kind: "diff", Preview: boundedUnifiedDiff(relative, string(original), string(finalBytes))}
			return result
		}
	}, true
}

func (session *streamingFileSession) Recovery() (StreamingFileRecovery, bool) {
	if session == nil {
		return StreamingFileRecovery{}, false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.stage == "" || session.target == "" || session.kind == streamingEdit {
		return StreamingFileRecovery{}, false
	}
	if session.stageFile != nil {
		_ = session.stageFile.Sync()
	}
	stageBytes, err := os.ReadFile(session.stage)
	if err != nil || len(stageBytes) == 0 {
		return StreamingFileRecovery{}, false
	}
	content := stageBytes
	if session.kind == streamingAppend {
		if len(stageBytes) < len(session.original) || !bytes.Equal(stageBytes[:len(session.original)], session.original) {
			return StreamingFileRecovery{}, false
		}
		content = stageBytes[len(session.original):]
	}
	if len(content) == 0 {
		return StreamingFileRecovery{}, false
	}
	return StreamingFileRecovery{
		ToolCallID: session.options.ToolCallID,
		ToolName:   string(session.kind),
		Path:       session.path.Value(),
		StagePath:  session.stage,
		Content:    string(content),
		Existing:   session.existed,
	}, true
}

func countFileLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}

func (session *streamingFileSession) disable(err error) {
	session.disabled = true
	session.writeErr = err
}

func (session *streamingFileSession) Close() {
	if session == nil {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return
	}
	if session.content != nil {
		_ = session.content.Flush()
	}
	if session.stageFile != nil {
		_ = session.stageFile.Sync()
		_ = session.stageFile.Close()
		session.stageFile = nil
	}
	session.closed = true
}

func (tool writeFileTool) StartStreaming(_ context.Context, options RunOptions) StreamingFileSession {
	if !options.PermissionGranted {
		return nil
	}
	session := newStreamingFileSession(streamingWrite, tool.workspaceRoot, tool.scope, options)
	session.content.onChunk = session.writeContentChunk
	return session
}

func (tool appendFileTool) StartStreaming(_ context.Context, options RunOptions) StreamingFileSession {
	if !options.PermissionGranted {
		return nil
	}
	session := newStreamingFileSession(streamingAppend, tool.workspaceRoot, tool.scope, options)
	session.content.onChunk = session.writeContentChunk
	return session
}

func (tool editFileTool) StartStreaming(_ context.Context, options RunOptions) StreamingFileSession {
	if !options.PermissionGranted {
		return nil
	}
	return newStreamingFileSession(streamingEdit, tool.workspaceRoot, tool.scope, options)
}
