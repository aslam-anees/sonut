// Package webmirror serves the live TUI session to other devices on the LAN —
// a phone-first web view ported from the "reference only/opencode-manager"
// concept (mobile web UI + SSE-streamed chat + prompt submission), reduced to
// what @code needs: the heavy lifting stays in the laptop TUI, and this
// package only mirrors the transcript out and feeds typed prompts back in.
//
// Security model (fail closed):
//   - Every request must present the per-run random 128-bit token (query ?t=
//     or Authorization: Bearer); tokens compare in constant time.
//   - The token is generated fresh each time the server starts and is only
//     shown inside the local terminal, so possession of the URL implies
//     access to the terminal that started it.
//   - No state-changing endpoint exists besides POST /prompt, whose body is
//     size-capped and which only forwards text into the TUI's normal input
//     path (same guards as locally typed input).
package webmirror

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Event is one transcript entry in the mirrored snapshot.
type Event struct {
	Kind   string `json:"kind"` // user | assistant | thinking | tool | system | error
	Text   string `json:"text"`
	Tool   string `json:"tool,omitempty"`   // tool name, for kind "tool"
	Status string `json:"status,omitempty"` // ok | error | running, for kind "tool"
	Live   bool   `json:"live,omitempty"`   // still streaming
}

// Notice is a one-shot attention signal (run finished, input needed) carried
// inside the snapshot; devices compare Seq to detect a new one.
type Notice struct {
	Seq  int    `json:"seq"`
	Kind string `json:"kind"` // done | input
	Text string `json:"text"`
}

// Snapshot is the full mirrored state pushed to every connected device. Full
// snapshots (not deltas) keep reconnecting phones trivially correct.
type Snapshot struct {
	Version int     `json:"version"`
	Running bool    `json:"running"`
	Events  []Event `json:"events"`
	Notice  Notice  `json:"notice"`
}

// Command describes one slash command for the device-side autocomplete.
type Command struct {
	Name        string `json:"name"`
	Usage       string `json:"usage"`
	Description string `json:"description"`
}

// Options configures Start.
type Options struct {
	// OnPrompt receives each submitted prompt (from HTTP goroutines); it must
	// be safe to call concurrently and should hand off quickly.
	OnPrompt func(string)
	// OnStop receives stop requests (remote Esc). Same constraints as OnPrompt.
	OnStop func()
	// Commands is the slash-command list served to devices for autocomplete.
	Commands []Command
}

// maxPromptBody caps POST /prompt bodies; anything a phone types fits well
// under this, and it keeps the endpoint useless for memory abuse.
const maxPromptBody = 64 << 10

// Server mirrors one TUI session over HTTP.
type Server struct {
	token    string
	listener net.Listener
	httpSrv  *http.Server
	onPrompt func(string)
	onStop   func()
	commands []Command

	mu          sync.Mutex
	snapshot    Snapshot
	fingerprint string
	subs        map[chan struct{}]struct{}
	closed      bool
}

// Start binds the mirror on the LAN and begins serving.
func Start(opts Options) (*Server, error) {
	if opts.OnPrompt == nil || opts.OnStop == nil {
		return nil, errors.New("webmirror: nil callback")
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("webmirror: token: %w", err)
	}
	// Prefer a stable, memorable port so re-enabling /share keeps the same URL
	// across sessions; fall back to an ephemeral port if it is taken.
	listener, err := net.Listen("tcp", ":8899")
	if err != nil {
		if listener, err = net.Listen("tcp", ":0"); err != nil {
			return nil, fmt.Errorf("webmirror: listen: %w", err)
		}
	}
	s := &Server{
		token:    hex.EncodeToString(tokenBytes),
		listener: listener,
		onPrompt: opts.OnPrompt,
		onStop:   opts.OnStop,
		commands: opts.Commands,
		subs:     map[chan struct{}]struct{}{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePage)
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/prompt", s.handlePrompt)
	mux.HandleFunc("/stop", s.handleStop)
	mux.HandleFunc("/meta", s.handleMeta)
	mux.HandleFunc("/files", s.handleFiles)
	mux.HandleFunc("/upload", s.handleUpload)
	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = s.httpSrv.Serve(listener) }()
	return s, nil
}

// URL returns the address to open on another device, token included.
func (s *Server) URL() string {
	port := s.listener.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("http://%s:%d/?t=%s", lanIP(), port, s.token)
}

// Close stops the server and drops every connected device.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	subs := s.subs
	s.subs = map[chan struct{}]struct{}{}
	s.mu.Unlock()
	for ch := range subs {
		close(ch)
	}
	_ = s.httpSrv.Close()
}

// MaybePublish replaces the snapshot when fingerprint differs from the last
// published one. build runs only on change, so callers can invoke this on
// every frame with a cheap fingerprint and pay the serialization cost only
// when the transcript actually moved.
func (s *Server) MaybePublish(fingerprint string, running bool, build func() []Event) {
	s.mu.Lock()
	if s.closed || fingerprint == s.fingerprint {
		s.mu.Unlock()
		return
	}
	s.fingerprint = fingerprint
	s.snapshot = Snapshot{
		Version: s.snapshot.Version + 1,
		Running: running,
		Events:  build(),
		Notice:  s.snapshot.Notice, // notices persist across rebuilds; Seq marks new ones
	}
	// Non-blocking notify: each subscriber channel has capacity 1, so a slow
	// phone coalesces bursts into one wake-up and never stalls the TUI.
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
}

// authorized checks the request token in constant time. Fail closed: no
// token, wrong token, or a closed server all read as unauthorized.
func (s *Server) authorized(r *http.Request) bool {
	presented := r.URL.Query().Get("t")
	if presented == "" {
		presented = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) == 1
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, mirrorPage)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	send := func() bool {
		s.mu.Lock()
		payload, err := json.Marshal(s.snapshot)
		s.mu.Unlock()
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() { // full state immediately on (re)connect
		return
	}
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case _, open := <-ch:
			if !open || !send() {
				return
			}
		case <-keepalive.C:
			// SSE comment frame: keeps proxies and phone radios from
			// dropping the idle connection.
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPromptBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		http.Error(w, "empty prompt", http.StatusBadRequest)
		return
	}
	s.onPrompt(text)
	w.WriteHeader(http.StatusAccepted)
}

// handleStop cancels the in-flight run — the remote equivalent of pressing
// Esc in the terminal. Idempotent: stopping when idle is a no-op in the TUI.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.onStop()
	w.WriteHeader(http.StatusAccepted)
}

// Notify pushes a one-shot attention signal (run finished / input needed) to
// every connected device without waiting for the next transcript change.
func (s *Server) Notify(kind, text string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.snapshot.Notice = Notice{Seq: s.snapshot.Notice.Seq + 1, Kind: kind, Text: text}
	s.snapshot.Version++
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
}

// handleMeta serves static metadata the page fetches once: the slash-command
// list for autocomplete.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	commands := s.commands
	if commands == nil {
		commands = []Command{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"commands": commands})
}

// filesSkipDirs are never descended into when completing @paths: huge, and
// never what a prompt wants to reference.
var filesSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	".next": true, "vendor": true, ".venv": true, "__pycache__": true,
	".atcode": true,
}

// handleFiles serves workspace-relative path completions for "@" mentions:
// case-insensitive substring match over the working tree, capped so a huge
// repo cannot stall the request.
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	const maxMatches, maxWalk = 20, 20000
	matches := make([]string, 0, maxMatches)
	walked := 0
	root, err := os.Getwd()
	if err == nil {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if filesSkipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != root) {
					return fs.SkipDir
				}
				return nil
			}
			walked++
			if walked > maxWalk || len(matches) >= maxMatches {
				return fs.SkipAll
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			if query == "" || strings.Contains(strings.ToLower(rel), query) {
				matches = append(matches, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"files": matches})
}

// maxUploadBody caps phone uploads (multipart overhead included).
const maxUploadBody = 25 << 20

// handleUpload stores a device-side file into .atcode/uploads/ in the
// workspace and returns its relative path, so the phone can reference it as
// an @path in the next prompt. Filenames are sanitized to a safe charset and
// deduped; nothing outside the uploads dir can be written.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer file.Close()
	dir := filepath.Join(".atcode", "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "upload dir", http.StatusInternalServerError)
		return
	}
	name := sanitizeUploadName(header.Filename)
	dest := filepath.Join(dir, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(name)
		dest = filepath.Join(dir, fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), i, ext))
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		http.Error(w, "write", http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		_ = os.Remove(dest)
		http.Error(w, "write", http.StatusInternalServerError)
		return
	}
	out.Close()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": filepath.ToSlash(dest)})
}

// sanitizeUploadName reduces a client-supplied filename to a safe basename.
func sanitizeUploadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		out = "upload"
	}
	return out
}

// lanIP picks the machine's LAN address so the printed URL works from other
// devices. Loopback is the fallback when no interface qualifies (still useful
// for same-machine browsers).
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost"
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return "localhost"
}
