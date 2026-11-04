package webmirror

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func startTestServer(t *testing.T, onPrompt func(string)) *Server {
	return startTestServerWithStop(t, onPrompt, func() {})
}

func startTestServerWithStop(t *testing.T, onPrompt func(string), onStop func()) *Server {
	t.Helper()
	if onPrompt == nil {
		onPrompt = func(string) {}
	}
	s, err := Start(Options{OnPrompt: onPrompt, OnStop: onStop,
		Commands: []Command{{Name: "/share", Usage: "/share [on|off]", Description: "mirror"}}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func (s *Server) baseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.listener.Addr().(*net.TCPAddr).Port)
}

func TestAuthFailsClosed(t *testing.T) {
	s := startTestServer(t, nil)
	base := s.baseURL()
	for _, path := range []string{"/", "/events", "/prompt", "/stop"} {
		for _, token := range []string{"", "?t=wrong"} {
			resp, err := http.Get(base + path + token)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("GET %s token=%q status = %d, want 401", path, token, resp.StatusCode)
			}
		}
	}
}

func TestPromptRoundTrip(t *testing.T) {
	got := make(chan string, 1)
	s := startTestServer(t, func(text string) { got <- text })
	url := s.baseURL() + "/prompt?t=" + s.token
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(`{"text":" hello from phone "}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	select {
	case text := <-got:
		if text != "hello from phone" {
			t.Fatalf("prompt = %q, want trimmed text", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt never delivered")
	}
	// Empty prompts are rejected, not forwarded.
	resp, err = http.Post(url, "application/json", bytes.NewBufferString(`{"text":"  "}`))
	if err != nil {
		t.Fatalf("POST empty: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty prompt status = %d, want 400", resp.StatusCode)
	}
}

func TestSSESnapshotAndPublish(t *testing.T) {
	s := startTestServer(t, nil)
	s.MaybePublish("v1", true, func() []Event {
		return []Event{{Kind: "user", Text: "hi"}}
	})
	// Same fingerprint must not bump the version.
	s.MaybePublish("v1", true, func() []Event { t.Fatal("build ran for unchanged fingerprint"); return nil })

	resp, err := http.Get(s.baseURL() + "/events?t=" + s.token)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	readSnapshot := func() Snapshot {
		t.Helper()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE: %v", err)
			}
			if strings.HasPrefix(line, "data: ") {
				var snap Snapshot
				if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &snap); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				return snap
			}
		}
	}
	first := readSnapshot()
	if first.Version != 1 || !first.Running || len(first.Events) != 1 || first.Events[0].Text != "hi" {
		t.Fatalf("initial snapshot = %+v", first)
	}
	s.MaybePublish("v2", false, func() []Event {
		return []Event{{Kind: "user", Text: "hi"}, {Kind: "assistant", Text: "yo"}}
	})
	second := readSnapshot()
	if second.Version != 2 || second.Running || len(second.Events) != 2 {
		t.Fatalf("second snapshot = %+v", second)
	}
}

func TestURLContainsToken(t *testing.T) {
	s := startTestServer(t, nil)
	url := s.URL()
	if !strings.Contains(url, "?t="+s.token) || !strings.HasPrefix(url, "http://") {
		t.Fatalf("URL = %q", url)
	}
}

func TestStopRoundTrip(t *testing.T) {
	stopped := make(chan struct{}, 1)
	s := startTestServerWithStop(t, nil, func() { stopped <- struct{}{} })
	resp, err := http.Post(s.baseURL()+"/stop?t="+s.token, "", nil)
	if err != nil {
		t.Fatalf("POST /stop: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("onStop not invoked")
	}
}

func TestMetaFilesUploadAndNotify(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.WriteFile("hello.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := startTestServer(t, nil)
	base := s.baseURL()

	// /meta serves the command list.
	resp, err := http.Get(base + "/meta?t=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Commands []Command `json:"commands"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&meta)
	resp.Body.Close()
	if len(meta.Commands) == 0 || meta.Commands[0].Name != "/share" {
		t.Fatalf("meta = %+v", meta)
	}

	// /files completes workspace paths.
	resp, err = http.Get(base + "/files?q=hel&t=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	var files struct {
		Files []string `json:"files"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&files)
	resp.Body.Close()
	if len(files.Files) != 1 || files.Files[0] != "hello.txt" {
		t.Fatalf("files = %+v", files)
	}

	// /upload stores into .atcode/uploads and returns the relative path.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "../evil name!.png")
	_, _ = fw.Write([]byte("data"))
	mw.Close()
	resp, err = http.Post(base+"/upload?t="+s.token, mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	var up struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&up)
	resp.Body.Close()
	if !strings.HasPrefix(up.Path, ".atcode/uploads/") || strings.Contains(up.Path, "..") {
		t.Fatalf("upload path = %q", up.Path)
	}
	if data, err := os.ReadFile(up.Path); err != nil || string(data) != "data" {
		t.Fatalf("uploaded file: %v %q", err, data)
	}

	// Notify bumps the snapshot version and carries the notice.
	before := s.snapshot.Version
	s.Notify("input", "needs you")
	s.mu.Lock()
	notice := s.snapshot.Notice
	version := s.snapshot.Version
	s.mu.Unlock()
	if notice.Seq != 1 || notice.Kind != "input" || version != before+1 {
		t.Fatalf("notice = %+v version %d->%d", notice, before, version)
	}
}
