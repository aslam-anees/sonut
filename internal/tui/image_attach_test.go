package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/llama"
	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

func TestModelSupportsVisionTUI(t *testing.T) {
	cases := []struct {
		modelName string
		want      bool
	}{
		{modelName: "gpt-4.1", want: true},                 // vision model in the catalog
		{modelName: "claude-sonnet-4.5", want: true},       // vision model in the catalog
		{modelName: "claude-haiku-3.5", want: false},       // claude-3-5-haiku has no image input support
		{modelName: "totally-unknown-custom", want: false}, // not in catalog -> can't confirm
		{modelName: "", want: false},
	}
	for _, tc := range cases {
		m := newModel(t.Context(), Options{ModelName: tc.modelName})
		got := m.modelSupportsVisionTUI()
		if got != tc.want {
			t.Fatalf("modelSupportsVisionTUI(%q) = %v, want %v", tc.modelName, got, tc.want)
		}
	}
}

func writeTestPNG(t *testing.T, dir, name string) string {
	t.Helper()
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

func lastTranscriptText(m model) string {
	if len(m.transcript) == 0 {
		return ""
	}
	return m.transcript[len(m.transcript)-1].text
}

func TestImageCommandAttachRendersChip(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, root, "photo.png")

	m := newModel(context.Background(), Options{Cwd: root, ModelName: "gpt-4.1"})
	m.input.SetValue("/image photo.png")
	updated, _ := m.handleSubmit()
	next := updated.(model)

	if len(next.pendingImages) != 1 {
		t.Fatalf("expected 1 pending image, got %d", len(next.pendingImages))
	}
	if next.pendingImages[0].MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", next.pendingImages[0].MediaType)
	}
	if len(next.pendingImageLabels) != 1 || next.pendingImageLabels[0] != "photo.png" {
		t.Fatalf("labels = %v, want [photo.png]", next.pendingImageLabels)
	}
	if chips := renderImageChips(next.pendingImageLabels); chips == "" {
		t.Fatal("expected a chip row for pending images")
	} else if !strings.Contains(chips, "[Image #1]") {
		// Grok-style path-free chips; filename is in the inspector, not the bar.
		t.Fatalf("chip row %q should be Grok-style [Image #1]", chips)
	}
	if !next.attachmentView.active {
		t.Fatal("attaching an image should open the Format/Dimensions/Size/Path inspector")
	}
}

func TestImageCommandClear(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, root, "photo.png")

	m := newModel(context.Background(), Options{Cwd: root, ModelName: "gpt-4.1"})
	m.input.SetValue("/image photo.png")
	updated, _ := m.handleSubmit()
	m = updated.(model)

	m.input.SetValue("/image clear")
	updated, _ = m.handleSubmit()
	next := updated.(model)

	if len(next.pendingImages) != 0 || len(next.pendingImageLabels) != 0 {
		t.Fatalf("expected cleared pending images, got %d/%d", len(next.pendingImages), len(next.pendingImageLabels))
	}
}

func TestImageCommandNonVisionRefuses(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, root, "photo.png")

	// A custom/unknown model id is treated as non-vision (can't confirm).
	m := newModel(context.Background(), Options{Cwd: root, ModelName: "totally-unknown-custom"})
	m.input.SetValue("/image photo.png")
	updated, _ := m.handleSubmit()
	next := updated.(model)

	if len(next.pendingImages) != 0 {
		t.Fatalf("non-vision model must refuse: got %d pending images", len(next.pendingImages))
	}
	notice := lastTranscriptText(next)
	if !strings.Contains(notice, "does not support image input") {
		t.Fatalf("expected an inline refusal notice, got %q", notice)
	}
}

func TestLlamaVisionProbeIsUpgradeOnly(t *testing.T) {
	// A positive live probe grants vision the startup snapshot missed.
	m := newModel(context.Background(), Options{ProviderName: "llama"})
	if m.llamaVision {
		t.Fatal("precondition: expected llamaVision false")
	}
	updated, _ := m.updateModel(llamaVisionProbedMsg{vision: true})
	if !updated.(model).llamaVision {
		t.Fatal("positive probe must grant vision")
	}

	// A negative probe must NEVER revoke a capability already in effect.
	granted := newModel(context.Background(), Options{ProviderName: "llama", LlamaVision: true})
	updated, _ = granted.updateModel(llamaVisionProbedMsg{vision: false})
	if !updated.(model).llamaVision {
		t.Fatal("negative probe must not revoke already-granted vision")
	}
}

func TestVisionRefusalNoticeNonLocalIsPlain(t *testing.T) {
	// A cloud/custom provider with no llama port always gets the plain refusal —
	// never the local-only "relaunch with ATCODE_FORCE_VISION" hint, even if an
	// mmproj happens to sit on disk from prior local use.
	m := newModel(context.Background(), Options{ProviderName: "openai", ModelName: "some-text-model", LlamaPort: 0})
	// Ensure the test does not accidentally inherit a local-llama identity.
	m.llamaPort = 0
	notice := m.visionRefusalNotice("attachment")
	if !strings.Contains(notice, "does not support image input") {
		t.Fatalf("cloud provider must get plain refusal, got %q", notice)
	}
	if strings.Contains(notice, "ATCODE_FORCE_VISION") {
		t.Fatalf("cloud provider must not get the local force-vision hint, got %q", notice)
	}
	if strings.Contains(notice, "relaunch") {
		t.Fatalf("cloud provider must not get the relaunch hint, got %q", notice)
	}
}

// @code always runs inference on the local llama-server; Preferences may still
// name the provider "openai". When LlamaPort is set AND vision is gated/stale,
// the recovery message must fire (empty model name used to produce the useless
// "the active model" dead-end the user hit when attaching screenshots).
func TestVisionRefusalNoticeLocalPortEvenIfProviderNameCloud(t *testing.T) {
	// Only meaningful when an mmproj is present and the launch gate or live
	// server state makes vision recovery actionable. CI without local GGUFs
	// correctly falls through to the plain refusal.
	if !llama.VisionGatedOff() && !llama.VisionProjectorMissingFromServer(42611) {
		t.Skip("no local mmproj / vision-gate state to exercise recovery wording")
	}
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "",
		LlamaPort:    42611,
	})
	notice := m.visionRefusalNotice("attachment")
	if strings.Contains(notice, "the active model does not support image input") {
		t.Fatalf("local llama port must not produce dead-end refusal, got %q", notice)
	}
	if !strings.Contains(notice, "relaunch") && !strings.Contains(notice, "ATCODE_FORCE_VISION") {
		t.Fatalf("expected recovery wording, got %q", notice)
	}
}

func TestImageCommandMissingFileNotice(t *testing.T) {
	root := t.TempDir()
	m := newModel(context.Background(), Options{Cwd: root, ModelName: "gpt-4.1"})
	m.input.SetValue("/image nope.png")
	updated, _ := m.handleSubmit()
	next := updated.(model)

	if len(next.pendingImages) != 0 {
		t.Fatal("a missing file must not attach")
	}
	if notice := lastTranscriptText(next); !strings.Contains(notice, "nope.png") {
		t.Fatalf("expected a notice naming the missing file, got %q", notice)
	}
}

func TestTranscriptViewShowsImageChips(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	m.width = 100
	m.height = 30
	m.pendingImageLabels = []string{"photo.png", "diagram.gif"}

	view := m.transcriptView()
	if !strings.Contains(view, "[Image #1]") || !strings.Contains(view, "[Image #2]") {
		t.Fatalf("transcript view should show Grok-style [Image #N] chips, got:\n%s", view)
	}
}

func TestSubmitThreadsImagesThenClears(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, root, "photo.png")

	captured := make(chan []sonutruntime.ImageBlock, 1)
	provider := &fakeProvider{events: []sonutruntime.StreamEvent{
		{Type: sonutruntime.StreamEventText, Content: "ok"},
		{Type: sonutruntime.StreamEventDone},
	}}

	m := newModel(context.Background(), Options{
		Cwd:          root,
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	// Capture the Images the agent run is launched with.
	m.agentOptions.OnText = func(string) {}
	m.captureRunImages = func(imgs []sonutruntime.ImageBlock) { captured <- imgs }

	m.input.SetValue("/image photo.png")
	updated, _ := m.handleSubmit()
	m = updated.(model)
	if len(m.pendingImages) != 1 {
		t.Fatalf("setup: expected 1 staged image, got %d", len(m.pendingImages))
	}

	m.input.SetValue("describe this")
	updated, cmd := m.handleSubmit()
	next := updated.(model)
	if cmd == nil {
		t.Fatal("expected a prompt submit to start a run")
	}
	if len(next.pendingImages) != 0 || len(next.pendingImageLabels) != 0 {
		t.Fatalf("submit must clear pending images, got %d/%d", len(next.pendingImages), len(next.pendingImageLabels))
	}

	execCmd(cmd) // run the agent goroutine; it invokes captureRunImages

	select {
	case imgs := <-captured:
		if len(imgs) != 1 || imgs[0].MediaType != "image/png" {
			t.Fatalf("agent run should receive 1 png image, got %#v", imgs)
		}
	default:
		t.Fatal("expected captureRunImages to be invoked with the staged image")
	}
}

// TestSubmitDropsImagesWhenModelSwitchedToNonVision attaches an image on a vision
// model, then simulates a /model switch to a non-vision id before submit. The
// submit-time re-check must drop the images (the agent run receives none), append
// an inline notice mirroring exec's drop+warn wording, and still clear pending
// state.
func TestSubmitDropsImagesWhenModelSwitchedToNonVision(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, root, "photo.png")

	captured := make(chan []sonutruntime.ImageBlock, 1)
	provider := &fakeProvider{events: []sonutruntime.StreamEvent{
		{Type: sonutruntime.StreamEventText, Content: "ok"},
		{Type: sonutruntime.StreamEventDone},
	}}

	m := newModel(context.Background(), Options{
		Cwd:          root,
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.agentOptions.OnText = func(string) {}
	m.captureRunImages = func(imgs []sonutruntime.ImageBlock) { captured <- imgs }

	// Attach on the vision model.
	m.input.SetValue("/image photo.png")
	updated, _ := m.handleSubmit()
	m = updated.(model)
	if len(m.pendingImages) != 1 {
		t.Fatalf("setup: expected 1 staged image, got %d", len(m.pendingImages))
	}

	// Simulate a /model switch to a non-vision (catalog-unknown) model.
	m.modelName = "totally-unknown-custom"

	m.input.SetValue("describe this")
	updated, cmd := m.handleSubmit()
	next := updated.(model)
	if cmd == nil {
		t.Fatal("expected a prompt submit to start a run")
	}
	if len(next.pendingImages) != 0 || len(next.pendingImageLabels) != 0 {
		t.Fatalf("submit must clear pending images, got %d/%d", len(next.pendingImages), len(next.pendingImageLabels))
	}
	if notice := lastTranscriptText(next); !strings.Contains(notice, "does not support image input") {
		t.Fatalf("expected an inline drop notice, got %q", notice)
	}

	execCmd(cmd) // run the agent goroutine; it invokes captureRunImages

	select {
	case imgs := <-captured:
		if len(imgs) != 0 {
			t.Fatalf("non-vision model must receive no images, got %#v", imgs)
		}
	default:
		t.Fatal("expected captureRunImages to be invoked (with no images)")
	}
}

// writeTestPDF writes a tiny single-page PDF whose text layer is the given
// string and returns its path. It mirrors the in-package fixture builder so the
// TUI test exercises the real LoadDocument path on real PDF bytes.
func writeTestPDF(t *testing.T, dir, name, text string) string {
	t.Helper()
	var buf bytes.Buffer
	offsets := make([]int, 0, 8)
	startObj := func() { offsets = append(offsets, buf.Len()) }

	buf.WriteString("%PDF-1.4\n")
	startObj()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	startObj()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	startObj()
	buf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n")
	content := "BT /F1 24 Tf 72 700 Td (" + text + ") Tj ET"
	startObj()
	buf.WriteString("4 0 obj\n<< /Length " + strconv.Itoa(len(content)) + " >>\nstream\n")
	buf.WriteString(content)
	buf.WriteString("\nendstream\nendobj\n")
	startObj()
	buf.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	xrefStart := buf.Len()
	buf.WriteString("xref\n0 " + strconv.Itoa(len(offsets)+1) + "\n0000000000 65535 f \n")
	for _, off := range offsets {
		buf.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}
	buf.WriteString("trailer\n<< /Size " + strconv.Itoa(len(offsets)+1) + " /Root 1 0 R >>\nstartxref\n" + strconv.Itoa(xrefStart) + "\n%%EOF\n")

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	return path
}

// A PDF carries a text layer every model can read, so /image <file.pdf> stages a
// pending document even on a non-vision model -- unlike a raw image, which is
// refused. No page images are staged without a rasterizer.
func TestImageCommandAttachesPDFTextOnNonVisionModel(t *testing.T) {
	root := t.TempDir()
	writeTestPDF(t, root, "spec.pdf", "Design spec body text")

	m := newModel(context.Background(), Options{Cwd: root, ModelName: "totally-unknown-custom"})
	m.input.SetValue("/image spec.pdf")
	updated, _ := m.handleSubmit()
	next := updated.(model)

	if len(next.pendingDocuments) != 1 {
		t.Fatalf("expected 1 pending document, got %d", len(next.pendingDocuments))
	}
	if next.pendingDocuments[0].label != "spec.pdf" {
		t.Fatalf("document label = %q, want spec.pdf", next.pendingDocuments[0].label)
	}
	if !strings.Contains(next.pendingDocuments[0].text, "Design spec body text") {
		t.Fatalf("document text %q should contain the body", next.pendingDocuments[0].text)
	}
	if len(next.pendingImages) != 0 {
		t.Fatalf("no rasterizer: expected 0 page images, got %d", len(next.pendingImages))
	}
	// A successful attach is silent now (the [Document N] composer chip is the
	// confirmation); the pending-document assertions above verify it.
}

// A real PDF whose path lacks a ".pdf" extension is still routed to the document
// path by a content sniff (not the extension), so its text layer attaches even on
// a non-vision model instead of being refused as a non-image.
func TestImageCommandAttachesExtensionlessPDFByContent(t *testing.T) {
	root := t.TempDir()
	writeTestPDF(t, root, "spec", "Extensionless PDF body text")

	m := newModel(context.Background(), Options{Cwd: root, ModelName: "totally-unknown-custom"})
	m.input.SetValue("/image spec")
	updated, _ := m.handleSubmit()
	next := updated.(model)

	if len(next.pendingDocuments) != 1 {
		t.Fatalf("expected 1 pending document from a content-sniffed PDF, got %d", len(next.pendingDocuments))
	}
	if !strings.Contains(next.pendingDocuments[0].text, "Extensionless PDF body text") {
		t.Fatalf("document text %q should contain the body", next.pendingDocuments[0].text)
	}
	if notice := lastTranscriptText(next); strings.Contains(notice, "does not support image input") {
		t.Fatalf("a real PDF must not be refused at the vision gate, got %q", notice)
	}
}

// A .pdf-named file that is not a real PDF is rejected with the explicit
// not-a-PDF notice and stages nothing.
func TestImageCommandRejectsFakePDF(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fake.pdf"), []byte("definitely not a pdf"), 0o644); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	m := newModel(context.Background(), Options{Cwd: root, ModelName: "gpt-4.1"})
	m.input.SetValue("/image fake.pdf")
	updated, _ := m.handleSubmit()
	next := updated.(model)

	if len(next.pendingDocuments) != 0 || len(next.pendingImages) != 0 {
		t.Fatal("a fake PDF must stage nothing")
	}
	if notice := lastTranscriptText(next); !strings.Contains(notice, "not a PDF") {
		t.Fatalf("expected a not-a-PDF notice, got %q", notice)
	}
}

// /image clear removes staged documents as well as images.
func TestImageCommandClearAlsoClearsDocuments(t *testing.T) {
	root := t.TempDir()
	writeTestPDF(t, root, "spec.pdf", "some text")

	m := newModel(context.Background(), Options{Cwd: root, ModelName: "gpt-4.1"})
	m.input.SetValue("/image spec.pdf")
	updated, _ := m.handleSubmit()
	m = updated.(model)
	if len(m.pendingDocuments) != 1 {
		t.Fatalf("setup: expected 1 staged document, got %d", len(m.pendingDocuments))
	}

	m.input.SetValue("/image clear")
	updated, _ = m.handleSubmit()
	next := updated.(model)
	if len(next.pendingDocuments) != 0 {
		t.Fatalf("clear must drop staged documents, got %d", len(next.pendingDocuments))
	}
}

// The chip row shows a Grok-style "[Document #N]" entry for staged documents.
func TestTranscriptViewShowsDocumentChips(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	m.width = 100
	m.height = 30
	m.pendingDocuments = []pendingDocument{{label: "spec.pdf", text: "body"}}

	view := m.transcriptView()
	if !strings.Contains(view, "[Document #1]") {
		t.Fatalf("transcript view should show Grok-style [Document #1] chip, got:\n%s", view)
	}
}

// On submit, the staged document text is prepended to the prompt the agent
// receives (so the model can read it), and the pending documents are cleared.
func TestSubmitPrependsDocumentTextThenClears(t *testing.T) {
	root := t.TempDir()
	writeTestPDF(t, root, "spec.pdf", "Top secret design notes")

	provider := &fakeProvider{events: []sonutruntime.StreamEvent{
		{Type: sonutruntime.StreamEventText, Content: "ok"},
		{Type: sonutruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		Cwd:          root,
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.agentOptions.OnText = func(string) {}

	m.input.SetValue("/image spec.pdf")
	updated, _ := m.handleSubmit()
	m = updated.(model)
	if len(m.pendingDocuments) != 1 {
		t.Fatalf("setup: expected 1 staged document, got %d", len(m.pendingDocuments))
	}

	m.input.SetValue("summarize the attached doc")
	updated, cmd := m.handleSubmit()
	next := updated.(model)
	if cmd == nil {
		t.Fatal("expected a prompt submit to start a run")
	}
	if len(next.pendingDocuments) != 0 {
		t.Fatalf("submit must clear staged documents, got %d", len(next.pendingDocuments))
	}

	updated, _ = next.Update(execCmd(cmd))
	_ = updated.(model)

	if len(provider.requests) != 1 {
		t.Fatalf("expected one provider request, got %d", len(provider.requests))
	}
	// The user turn must carry both the document text and the user's question.
	// (A planner seed may follow it, so read the last USER message.)
	msgs := provider.requests[0].Messages
	var last sonutruntime.Message
	for _, msg := range msgs {
		if msg.Role == sonutruntime.MessageRoleUser {
			last = msg
		}
	}
	if last.Role != sonutruntime.MessageRoleUser {
		t.Fatalf("no user message found in request")
	}
	if !strings.Contains(last.Content, "Top secret design notes") {
		t.Fatalf("agent prompt should contain the document text, got:\n%s", last.Content)
	}
	if !strings.Contains(last.Content, "summarize the attached doc") {
		t.Fatalf("agent prompt should contain the user question, got:\n%s", last.Content)
	}
	if !strings.Contains(last.Content, "spec.pdf") {
		t.Fatalf("agent prompt should name the attached document, got:\n%s", last.Content)
	}
}

func TestRenderAttachmentChips(t *testing.T) {
	if got := renderAttachmentChips(nil, nil); got != "" {
		t.Fatalf("empty attachments should render no chips, got %q", got)
	}
	got := renderAttachmentChips([]string{"a.png", "b.png"}, []pendingDocument{{label: "spec.pdf"}})
	if got != "[Image #1] [Image #2] [Document #1]" {
		// Path-free Grok-style bar; inspect overlay holds Format/Size/Path.
		t.Fatalf("chip row = %q, want Grok-style path-free chips", got)
	}
}

func TestAttachmentViewShowsMetadata(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, root, "photo.png")

	m := newModel(context.Background(), Options{Cwd: root, ModelName: "gpt-4.1"})
	m.width = 80
	m.height = 30
	m.input.SetValue("/image photo.png")
	updated, _ := m.handleSubmit()
	next := updated.(model)
	if !next.attachmentView.active {
		t.Fatal("expected attachment inspector open after /image")
	}
	card := next.renderAttachmentCard(60)
	for _, want := range []string{"Format", "PNG", "Size", "Path", "photo.png"} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q:\n%s", want, card)
		}
	}
	// /image view re-opens inspector
	next.attachmentView = attachmentViewState{}
	next.input.SetValue("/image view")
	updated, _ = next.handleSubmit()
	next = updated.(model)
	if !next.attachmentView.active {
		t.Fatal("/image view should reopen inspector")
	}
}
