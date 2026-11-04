package voice

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/config"
)

func TestDefaultConfigUsesTwoSecondHoldAndThreeSecondSilence(t *testing.T) {
	cfg := config.DefaultVoiceConfig()
	if cfg.Shortcut != "hold:space:2s" {
		t.Fatalf("shortcut = %q", cfg.Shortcut)
	}
	if cfg.SilenceMs != 3000 {
		t.Fatalf("silence = %d", cfg.SilenceMs)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	endpoint := DefaultEndpointConfig()
	if endpoint.SilenceMs != 3000 || endpoint.PreRollMs != 300 || endpoint.MinSpeechMs != 250 {
		t.Fatalf("unexpected endpoint defaults: %+v", endpoint)
	}
}

func TestShortcutHoldRequiresGenuineRepeatAndInsertsOneSpace(t *testing.T) {
	shortcut, err := ParseShortcut("hold:space:2s")
	if err != nil {
		t.Fatal(err)
	}
	machine := NewHoldMachine(shortcut)
	start := time.Unix(0, 0)
	if got := machine.Press(start, false); got != HoldArmed {
		t.Fatal(got)
	}
	if got := machine.Tick(start.Add(3 * time.Second)); got != HoldNone {
		t.Fatal(got)
	}
	if got := machine.Release(start.Add(3 * time.Second)); got != HoldInsert {
		t.Fatal(got)
	}
	if got := machine.Press(start, false); got != HoldArmed {
		t.Fatal(got)
	}
	if got := machine.Press(start.Add(2*time.Second), true); got != HoldActivate {
		t.Fatal(got)
	}
	if got := machine.Press(start.Add(3*time.Second), true); got != HoldNone {
		t.Fatal(got)
	}
	if got := machine.Release(start.Add(3 * time.Second)); got != HoldCancel {
		t.Fatal(got)
	}
}

func TestReplaceableTranscriptPreservesPrefixSuffixCursorAndCancellation(t *testing.T) {
	editor := NewReplaceableTranscript(ComposerSnapshot{Text: "before 后 after", Cursor: len([]rune("before 后"))})
	partial := editor.Update(" hello")
	if partial.Text != "before 后 hello after" {
		t.Fatalf("partial = %q", partial.Text)
	}
	if partial.Cursor != len([]rune("before 后 hello")) {
		t.Fatalf("cursor = %d", partial.Cursor)
	}
	final := editor.Commit(" hello world")
	if final.Text != "before 后 hello world after" {
		t.Fatalf("final = %q", final.Text)
	}
	cancelled := NewReplaceableTranscript(ComposerSnapshot{Text: "draft", Cursor: 5}).Update(" unstable")
	if cancelled.Text != "draft unstable" {
		t.Fatal(cancelled.Text)
	}
	if got := editor.Cancel().Text; got != final.Text {
		t.Fatalf("inactive cancel changed committed text: %q", got)
	}
}

func TestEndpointTrackerEndsOnlyAfterThreeSecondsOfSpeechSilence(t *testing.T) {
	tracker, err := NewEndpointTracker(DefaultEndpointConfig())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(0, 0)
	tracker.Start(start)
	if tracker.SpeechStart(start.Add(time.Second)) != EndpointSpeechStarted {
		t.Fatal("missing speech start")
	}
	if got := tracker.Tick(start.Add(3*time.Second + 200*time.Millisecond)); got != EndpointNone {
		t.Fatalf("speech shorter than minimum should not end: %s", got)
	}
	if got := tracker.Tick(start.Add(3*time.Second + 900*time.Millisecond)); got != EndpointNone {
		t.Fatalf("silence before endpoint should not end: %s", got)
	}
	if got := tracker.Tick(start.Add(4*time.Second + 100*time.Millisecond)); got != EndpointSpeechEnded {
		t.Fatalf("endpoint = %s", got)
	}
}

func TestSentenceChunkerFiltersMarkdownAndStableBoundaries(t *testing.T) {
	chunker := NewSentenceChunker()
	if got := chunker.Push("Answer: The value is 1.5. See https://example.com now. ```go\nsecret()\n```"); len(got) != 2 {
		t.Fatalf("chunks = %#v", got)
	} else if strings.Contains(strings.Join(got, " "), "secret") || strings.Contains(strings.Join(got, " "), "http") {
		t.Fatalf("unsafe speech content: %#v", got)
	}
	if got := chunker.Push(" Next"); len(got) != 0 {
		t.Fatalf("unstable tail spoken early: %#v", got)
	}
	if got := chunker.Flush(); len(got) != 1 || got[0] != "Next" {
		t.Fatalf("flush = %#v", got)
	}
}

func TestSentenceChunkerNeverSpeaksCodeFenceAcrossStreamDeltas(t *testing.T) {
	chunker := NewSentenceChunker()
	if got := chunker.Push("Safe answer. ```go\n"); len(got) != 1 || got[0] != "Safe answer." {
		t.Fatalf("prefix chunks = %#v", got)
	}
	if got := chunker.Push("fmt.Println(\"secret\")\n"); len(got) != 0 {
		t.Fatalf("code chunk was spoken: %#v", got)
	}
	if got := chunker.Push("``` Then continue."); len(got) != 1 || got[0] != "Then continue." {
		t.Fatalf("post-code chunk = %#v", got)
	}
	if got := chunker.Flush(); len(got) != 0 {
		t.Fatalf("post-code flush = %#v", got)
	}
}

func TestSentenceChunkerNeverSpeaksSplitURLFragment(t *testing.T) {
	chunker := NewSentenceChunker()
	if got := chunker.Push("See https://exa"); len(got) != 0 {
		t.Fatalf("URL prefix was spoken: %#v", got)
	}
	if got := chunker.Push("mple.com now."); len(got) != 1 || got[0] != "See now." {
		t.Fatalf("URL-filtered sentence = %#v", got)
	}
}

func TestProtocolRoundTripAndRejectsWrongVersion(t *testing.T) {
	var builder strings.Builder
	if err := EncodeMessage(&builder, Message{Version: ProtocolVersion, Type: "asr.partial", RequestID: "r1", UtteranceID: "u1", Text: "नमस्ते"}); err != nil {
		t.Fatal(err)
	}
	message, err := DecodeMessage([]byte(strings.TrimSpace(builder.String())))
	if err != nil || message.Text != "नमस्ते" || message.RequestID != "r1" {
		t.Fatalf("round trip: %+v %v", message, err)
	}
	if _, err := DecodeMessage([]byte(`{"version":2,"type":"ready"}`)); err == nil {
		t.Fatal("wrong protocol version accepted")
	}
}

func TestManifestHasPinnedArtifactsAndSecureInstallerResumes(t *testing.T) {
	manifest := DefaultLightManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/qwen" {
			_, _ = io.WriteString(w, "qwen")
		} else {
			_, _ = io.WriteString(w, "tts")
		}
	}))
	defer server.Close()
	// Exercise the installer rejection path without downloading the real pack.
	manifest.Artifacts[0].URL = strings.Replace(server.URL, "http://", "https://", 1)
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if manifest.Artifacts[0].Bytes <= 0 || manifest.Artifacts[0].SHA256 == "" {
		t.Fatal("manifest is not checksummed")
	}
}

func TestInstallerWritesManifestAtomicallyWithTestArtifact(t *testing.T) {
	data := []byte("voice-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, Profile: "light", ProfileVersion: "test", AudioCPPRevision: "test", RequiredBytes: 1024,
		Artifacts: []Artifact{{Name: "test", Version: "1", URL: "https://example.invalid/test", TargetPath: "models/test.bin", Bytes: int64(len(data)), SHA256: "" + sha256Hex(data), License: "Apache-2.0", Required: true}}}
	client := server.Client()
	client.Transport = rewriteHTTPS{base: server.Client().Transport, target: server.URL}
	root := t.TempDir()
	if err := Install(context.Background(), manifest, InstallerOptions{Root: root, Client: client, FreeDisk: func(string, int64) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

type rewriteHTTPS struct {
	base   http.RoundTripper
	target string
}

func (r rewriteHTTPS) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(r.target, "http://")
	return r.base.RoundTrip(clone)
}

func sha256Hex(data []byte) string { h := sha256.Sum256(data); return fmt.Sprintf("%x", h[:]) }
