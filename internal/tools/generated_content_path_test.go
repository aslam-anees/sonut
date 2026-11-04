package tools

import "testing"

func TestValidateGeneratedContentPath(t *testing.T) {
	if err := validateGeneratedContentPath("index.html", "* { box-sizing: border-box; }"); err == nil {
		t.Fatal("CSS must not be accepted as HTML")
	}
	if err := validateGeneratedContentPath("styles.css", "<!doctype html><html></html>"); err == nil {
		t.Fatal("HTML must not be accepted as CSS")
	}
	if err := validateGeneratedContentPath("index.html", "<!doctype html><html><body></body></html>"); err != nil {
		t.Fatalf("valid HTML rejected: %v", err)
	}
	if err := validateGeneratedContentPath("super", "<!doctype html><html><body><canvas></canvas></body></html>"); err == nil {
		t.Fatal("HTML must not be accepted at an extensionless path")
	}
	// Live supermario thrash: bare JS constants must never land in .html.
	bareJS := "const TILE = 32;\nconst LEVEL_WIDTH = 60;\nconst levelData = [\n"
	if err := validateGeneratedContentPath("supermario.html", bareJS); err == nil {
		t.Fatal("bare JavaScript must not be accepted as HTML")
	}
	if err := validateGeneratedContentPath("game.js", bareJS); err != nil {
		t.Fatalf("bare JS must be allowed on .js: %v", err)
	}
	// Partial HTML first-chunk with canvas is salvageable.
	if err := validateGeneratedContentPath("game.html", "<!DOCTYPE html>\n<html>\n<body>\n<canvas id=\"c\"></canvas>\n<script>\n"); err != nil {
		t.Fatalf("HTML shell prefix rejected: %v", err)
	}
}

func TestLooksLikeHTMLDocument(t *testing.T) {
	if LooksLikeHTMLDocument("const TILE = 32;\n") {
		t.Fatal("JS constants must not look like HTML")
	}
	if !LooksLikeHTMLDocument("<!DOCTYPE html>\n<html>") {
		t.Fatal("DOCTYPE should look like HTML")
	}
	if !LooksLikeHTMLDocument("<canvas id=\"game\"></canvas>\n<script>") {
		t.Fatal("canvas tag should look like HTML")
	}
}

func TestIsSalvageablePartialContent(t *testing.T) {
	if IsSalvageablePartialContent("supermario.html", "const TILE = 32;\nconst LEVEL_WIDTH = 60;\n") {
		t.Fatal("bare JS partial into .html must not be salvageable")
	}
	if IsSalvageablePartialContent("x.html", "ab\n") {
		t.Fatal("tiny fragment must not be salvageable")
	}
	shell := "<!DOCTYPE html>\n<html>\n<head><title>m</title></head>\n<body>\n<canvas></canvas>\n<script>\n// game\n"
	if !IsSalvageablePartialContent("supermario.html", shell) {
		t.Fatal("HTML shell partial must be salvageable")
	}
	if !IsSalvageablePartialContent("game.js", "const TILE = 32;\nconst LEVEL_WIDTH = 60;\nconst h = 16;\n") {
		t.Fatal("JS partial into .js must be salvageable")
	}
}

func TestHTMLPathNeedsRewrite(t *testing.T) {
	if !HTMLPathNeedsRewrite("supermario.html", "const TILE = 32;\nconst LEVEL_WIDTH = 60;\n") {
		t.Fatal("bare-JS .html stub must need rewrite")
	}
	if HTMLPathNeedsRewrite("supermario.html", "<!DOCTYPE html>\n<html><body></body></html>\n") {
		t.Fatal("valid HTML must not need rewrite")
	}
	if HTMLPathNeedsRewrite("game.js", "const TILE = 32;\n") {
		t.Fatal(".js path must not need HTML rewrite")
	}
}

func TestGeneratedContentPathHint(t *testing.T) {
	if got := generatedContentPathHint("index.html", "/* styles.css */\n* {\n  margin: 0;\n}"); got == "" {
		t.Fatal("CSS written to HTML should produce a correction hint")
	}
	if got := generatedContentPathHint("styles.css", "<!doctype html><html></html>"); got == "" {
		t.Fatal("HTML written to CSS should produce a correction hint")
	}
	if got := generatedContentPathHint("index.html", "<html></html>"); got != "" {
		t.Fatalf("matching HTML path should not produce a hint: %q", got)
	}
	if got := generatedContentPathHint("supermario.html", "const TILE = 32;\nfunction loop(){}"); got == "" {
		t.Fatal("bare JS into .html must produce a correction hint")
	}
	if got := generatedContentPathHint("super", "<!doctype html><html><body></body></html>"); got == "" {
		t.Fatal("HTML at an extensionless path must produce a correction hint")
	}
}
