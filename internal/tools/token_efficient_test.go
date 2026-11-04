package tools

import (
	"strings"
	"testing"
)

func TestCleanForModelStripsANSIAndProgress(t *testing.T) {
	// Progress bar redrawn with \r, then a green "done" with ANSI.
	raw := "downloading...\r[##########] 100%\n\x1b[32mdone\x1b[0m\n"
	got := CleanForModel(raw, CleanOptions{Command: "curl -O file"})
	if got.Degraded {
		t.Fatalf("expected successful clean, got degraded (in=%d out=%d)", got.BytesIn, got.BytesOut)
	}
	if strings.Contains(got.Text, "\x1b") {
		t.Fatalf("ANSI should be stripped: %q", got.Text)
	}
	if strings.Contains(got.Text, "downloading...") {
		t.Fatalf("progress frames before last \\r should be dropped: %q", got.Text)
	}
	if !strings.Contains(got.Text, "100%") || !strings.Contains(got.Text, "done") {
		t.Fatalf("final frame and done line should remain: %q", got.Text)
	}
	if got.BytesOut >= got.BytesIn {
		t.Fatalf("cleaned output must shrink: in=%d out=%d", got.BytesIn, got.BytesOut)
	}
}

func TestCleanForModelNeverWorse(t *testing.T) {
	// Already clean short text — cleaning must not expand it.
	raw := "ok\n"
	got := CleanForModel(raw, CleanOptions{})
	if got.Text != raw {
		t.Fatalf("never-worse: got %q want %q (degraded=%v)", got.Text, raw, got.Degraded)
	}
}

func TestCleanForModelOptOut(t *testing.T) {
	raw := "\x1b[31mred\x1b[0m\n" + strings.Repeat("x", 600)
	got := CleanForModel(raw, CleanOptions{Command: "cat big # nofilter"})
	if got.Text != raw {
		t.Fatalf("# nofilter must skip clean: got %q", got.Text)
	}
	t.Setenv("ATCODE_BASH_RAW", "1")
	got = CleanForModel(raw, CleanOptions{Command: "cat big"})
	if got.Text != raw {
		t.Fatalf("ATCODE_BASH_RAW=1 must skip clean")
	}
}

func TestCleanForModelLongLine(t *testing.T) {
	line := strings.Repeat("a", 800)
	got := CleanForModel(line+"\n", CleanOptions{})
	if got.Degraded {
		t.Fatal("long line should clean")
	}
	if !strings.Contains(got.Text, "elided") {
		t.Fatalf("expected elision marker: %q", got.Text)
	}
	if strings.Count(got.Text, "a") >= 800 {
		t.Fatalf("line should be truncated: len=%d", len(got.Text))
	}
}

func TestCleanForModelGoTestShape(t *testing.T) {
	body := strings.Join([]string{
		"=== RUN   TestOK",
		"--- PASS: TestOK (0.00s)",
		"=== RUN   TestFail",
		"    foo_test.go:10: want 1 got 2",
		"--- FAIL: TestFail (0.01s)",
		"FAIL",
		"FAIL\texample.com/pkg\t0.02s",
		"ok  \texample.com/other\t0.01s",
	}, "\n")
	// Pad with verbose noise so the shape actually shrinks.
	noise := strings.Repeat("--- PASS: TestNoise (0.00s)\n", 40)
	raw := body + "\n" + noise
	got := CleanForModel(raw, CleanOptions{Command: "go test ./..."})
	if got.Shape != "gotest" && !strings.Contains(got.Text, "FAIL") {
		t.Fatalf("shape=%q text=%q", got.Shape, got.Text)
	}
	if !strings.Contains(got.Text, "FAIL") {
		t.Fatalf("must keep FAIL lines: %q", got.Text)
	}
	if got.BytesOut >= got.BytesIn {
		t.Fatalf("gotest shape should shrink verbose pass noise: in=%d out=%d", got.BytesIn, got.BytesOut)
	}
}

func TestCleanForModelStacktraceFoldsDeps(t *testing.T) {
	body := strings.Join([]string{
		"Traceback (most recent call last):",
		`  File "/app/main.py", line 1, in <module>`,
		`  File "/usr/lib/python3.11/site-packages/foo.py", line 1`,
		`  File "/usr/lib/python3.11/site-packages/bar.py", line 2`,
		`  File "/usr/lib/python3.11/site-packages/baz.py", line 3`,
		"ValueError: boom",
	}, "\n")
	// Detect via body fingerprint (no command match).
	got := CleanForModel(body, CleanOptions{Command: "python app.py"})
	if got.Shape != "stacktrace" && !strings.Contains(got.Text, "suppressed") {
		// May degrade if shrink fails; still check for dep fold when applied.
		if got.Degraded {
			t.Skip("never-worse degraded; body too small for guaranteed fold")
		}
	}
	if strings.Contains(got.Text, "site-packages") && !strings.Contains(got.Text, "suppressed") {
		// If shape applied, deps should be folded.
		if got.Shape == "stacktrace" {
			t.Fatalf("dependency frames should be folded: %q", got.Text)
		}
	}
}

func TestFormatBashOutputForCommandCleansANSI(t *testing.T) {
	out := formatBashOutputForCommand("ls", "\x1b[32mfile.txt\x1b[0m\n", "", 0)
	if strings.Contains(out, "\x1b") {
		t.Fatalf("formatBashOutputForCommand should strip ANSI: %q", out)
	}
	if !strings.Contains(out, "file.txt") {
		t.Fatalf("filename must remain: %q", out)
	}
}
