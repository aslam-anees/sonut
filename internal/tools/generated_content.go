package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// stripToolProtocolResidue removes chat-template markup that a local model can
// accidentally place inside a structured file argument. The first file body is
// valid; everything after a protocol boundary belongs to a subsequent tool
// call and must never be persisted in the current path.
func stripToolProtocolResidue(content string) (string, bool) {
	markers := []string{"</function>", "<tool_call>", "<function=", "<parameter="}
	cut := -1
	for _, marker := range markers {
		for start := 0; ; {
			relative := strings.Index(content[start:], marker)
			if relative < 0 {
				break
			}
			i := start + relative
			lineStart := strings.LastIndexByte(content[:i], '\n') + 1
			if i > 0 && strings.TrimSpace(content[lineStart:i]) == "" && (cut < 0 || i < cut) {
				cut = i
			}
			start = i + len(marker)
		}
	}
	if cut < 0 {
		return content, false
	}
	clean := strings.TrimRight(content[:cut], " \t\r\n") + "\n"
	return clean, true
}

func generatedContentKind(content string) string {
	trimmed := strings.TrimSpace(strings.ToLower(content))
	if trimmed == "" {
		return ""
	}
	if LooksLikeHTMLDocument(content) {
		return "HTML"
	}
	if strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "html,") || strings.HasPrefix(trimmed, "body {") || strings.Contains(trimmed, "{\n  margin:") {
		return "CSS"
	}
	if strings.HasPrefix(trimmed, "const ") || strings.HasPrefix(trimmed, "let ") || strings.HasPrefix(trimmed, "var ") ||
		strings.HasPrefix(trimmed, "function ") || strings.HasPrefix(trimmed, "class ") ||
		strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "export ") ||
		strings.HasPrefix(trimmed, "//") {
		// Pure JS/TS dumps (const TILE=…, // Game constants) are a common local-
		// model failure when the task asked for a single .html game file.
		return "JavaScript"
	}
	return ""
}

// LooksLikeHTMLDocument reports whether content is a plausible HTML document
// start (DOCTYPE/html shell or a real HTML tag). Bare JS/CSS constants must
// never pass as HTML — that is how supermario.html ended up as 6 lines of
// `const TILE = 32` with no canvas and no playable page.
func LooksLikeHTMLDocument(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") {
		return true
	}
	// Partial first-chunk salvage: accept a real tag opener, not code.
	if !strings.HasPrefix(lower, "<") {
		return false
	}
	for _, tag := range []string{
		"<html", "<head", "<body", "<canvas", "<script", "<style",
		"<div", "<main", "<section", "<meta", "<link", "<title",
	} {
		if strings.Contains(lower, tag) {
			return true
		}
	}
	return false
}

func generatedContentPathHint(requestedPath, content string) string {
	kind := generatedContentKind(content)
	if kind == "" {
		return ""
	}
	lowerPath := strings.ToLower(strings.TrimSpace(requestedPath))
	switch {
	case kind == "HTML" && filepath.Ext(strings.TrimSpace(requestedPath)) == "":
		return "The body is HTML; use a .html or .htm path such as index.html."
	case kind == "CSS" && (strings.HasSuffix(lowerPath, ".html") || strings.HasSuffix(lowerPath, ".htm")):
		return "The body is CSS; use a .css path such as styles.css, or wrap it in a <style> block inside a full HTML document."
	case kind == "HTML" && strings.HasSuffix(lowerPath, ".css"):
		return "The body is HTML; use an .html or .htm path."
	case kind == "JavaScript" && (strings.HasSuffix(lowerPath, ".html") || strings.HasSuffix(lowerPath, ".htm")):
		return "The body is bare JavaScript, not HTML. For a single-file game: write_file an HTML shell first (<!DOCTYPE html>…<canvas>…<script>…), then append_file game logic INSIDE the script. Or use a .js path and <script src>."
	case kind == "JavaScript" && strings.HasSuffix(lowerPath, ".css"):
		return "The body is JavaScript; use a .js path."
	default:
		return ""
	}
}

func validateGeneratedContentPath(path, content string) error {
	ext := strings.ToLower(filepath.Ext(path))
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}
	if generatedContentKind(content) == "HTML" && ext == "" {
		return fmt.Errorf("content looks like HTML; use a .html or .htm path")
	}
	switch ext {
	case ".html", ".htm":
		if LooksLikeHTMLDocument(content) {
			return nil
		}
		switch generatedContentKind(content) {
		case "JavaScript":
			return fmt.Errorf("content looks like JavaScript, not HTML")
		case "CSS":
			return fmt.Errorf("content looks like CSS, not HTML")
		default:
			return fmt.Errorf("content does not look like HTML (must start with <!DOCTYPE html>, <html>, or a real HTML tag — not bare JS constants)")
		}
	case ".css":
		if strings.Contains(strings.ToLower(trimmed), "<!doctype html") || strings.Contains(strings.ToLower(trimmed), "<html") {
			return fmt.Errorf("content looks like HTML, not CSS")
		}
	}
	return nil
}

// IsSalvageablePartialContent decides whether a truncated write_file/append_file
// body is worth persisting as a recoverable prefix. Tiny or wrong-type fragments
// (live: 6 lines of `const TILE` into supermario.html) latch append-only thrash
// and never become a playable file — discard them so the model restarts cleanly.
func IsSalvageablePartialContent(path, content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	// Incomplete JSON recovery may leave a half-line only; require a real prefix.
	// Threshold is deliberately low for legitimate short .js chunks, but still
	// blocks one-line thrash stubs.
	lines := strings.Count(content, "\n") + 1
	if len(content) < 40 && lines < 3 {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".html", ".htm":
		return LooksLikeHTMLDocument(content)
	case ".css":
		// Do not salvage pure HTML into a CSS path.
		return validateGeneratedContentPath(path, content) == nil
	default:
		return true
	}
}

// HTMLPathNeedsRewrite reports that an on-disk .html file is not a document
// (bare JS dump). Append would only dig the hole deeper — force overwrite.
func HTMLPathNeedsRewrite(path, existing string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".html" && ext != ".htm" {
		return false
	}
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return false
	}
	return !LooksLikeHTMLDocument(existing)
}
