package tools

import (
	"strings"
	"testing"
)

func TestHTMLToTextKeepsReadableContentAndDropsChrome(t *testing.T) {
	input := `<!doctype html><html><head><title>Hidden</title><style>.x{}</style></head><body><nav>Menu</nav><article><h1>Readable title</h1><p>First&nbsp;paragraph.</p><script>alert('ignored')</script><ul><li>One</li><li>Two</li></ul></article></body></html>`
	got := htmlToText(input)
	for _, want := range []string{"Readable title", "First paragraph.", "One", "Two"} {
		if !strings.Contains(got, want) {
			t.Fatalf("extracted text missing %q: %q", want, got)
		}
	}
	for _, forbidden := range []string{"Hidden", "alert", "<h1>", "<script>"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("extracted text retained %q: %q", forbidden, got)
		}
	}
}

func TestHTMLToTextNormalizesWhitespace(t *testing.T) {
	got := htmlToText(`<article>  alpha   beta <br> gamma </article>`)
	if got != "alpha beta\ngamma" {
		t.Fatalf("normalized text = %q", got)
	}
}
