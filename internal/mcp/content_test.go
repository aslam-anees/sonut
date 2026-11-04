package mcp

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestContentArtifactsPreservesTextImagesAndResources(t *testing.T) {
	png := []byte("not-a-real-png-but-valid-transport-data")
	text, images := contentArtifacts([]Content{
		{Type: "text", Text: "page loaded"},
		{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(png)},
		{Type: "resource_link", URI: "https://example.test/report"},
	})
	if len(images) != 1 || images[0].MediaType != "image/png" || string(images[0].Data) != string(png) {
		t.Fatalf("images = %#v", images)
	}
	for _, want := range []string{"page loaded", "image attachment", "https://example.test/report"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text %q missing %q", text, want)
		}
	}
}

func TestContentArtifactsRejectsUnsafeImageWithoutDroppingText(t *testing.T) {
	text, images := contentArtifacts([]Content{{Type: "text", Text: "still useful"}, {Type: "image", MimeType: "image/svg+xml", Data: "bad"}})
	if len(images) != 0 || !strings.Contains(text, "still useful") || !strings.Contains(text, "could not be attached") {
		t.Fatalf("text=%q images=%#v", text, images)
	}
}
