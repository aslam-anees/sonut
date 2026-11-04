package agent

import (
	"strings"
	"testing"
)

// The mario.html failure mode: ls -lh X, ls -la X, ls -la X, cd dir && file X —
// never byte-identical, so the exact-repeat guards stay silent while every
// turn re-verifies a file whose metadata is already in context. The path-keyed
// inspection guard nudges on the second look and blocks from the third.
func TestFileInspectionNearDuplicateGuard(t *testing.T) {
	var g guardState
	if out := g.recordCallSignature("bash", `{"command":"ls -lh /Users/a/games/mario.html"}`); out.Blocked || out.Stop {
		t.Fatalf("first inspection must dispatch, got %+v", out)
	}
	if g.takeRepeatNudge() != "" {
		t.Fatal("no nudge on first inspection")
	}
	if out := g.recordCallSignature("bash", `{"command":"ls -la /Users/a/games/mario.html"}`); out.Blocked {
		t.Fatalf("second inspection dispatches but arms nudge, got %+v", out)
	}
	if !strings.Contains(g.takeRepeatNudge(), "re-inspected 'mario.html'") {
		t.Fatal("second inspection of same path must arm a nudge")
	}
	// Different flavor again, and via cd-prefix + basename — still the same path.
	out := g.recordCallSignature("bash", `{"command":"cd /Users/a/games && file mario.html"}`)
	if !out.Blocked || !strings.Contains(out.BlockNotice, "mario.html") {
		t.Fatalf("third inspection must be blocked, got %+v", out)
	}
	// A different file is untouched.
	if out := g.recordCallSignature("bash", `{"command":"ls -la index.html"}`); out.Blocked || out.Stop {
		t.Fatalf("inspection of a new path must dispatch, got %+v", out)
	}
}

func TestFileInspectTargetIgnoresNonInspections(t *testing.T) {
	cases := map[string]string{
		`{"command":"go test ./..."}`:               "",
		`{"command":"ls -la mario.html | head -3"}`: "", // pipes may transform output
		`{"command":"cat mario.html"}`:              "", // content read, not metadata
		`{"command":"ls $HOME/games/mario.html"}`:   "", // shell expansion unknown
		`{"command":"rm -rf mario.html"}`:           "",
		`{"command":"wc -l mario.html"}`:            "mario.html",
		`{"command":"stat -f %z games/mario.html"}`: "mario.html", // keys on the final operand (the path)
		`{"command":"du -sh build/"}`:               "build",
		`{"command":"ls"}`:                          "", // bare ls: no operand to key on
		`not-json`:                                  "",
	}
	for args, want := range cases {
		got := fileInspectTarget("bash", args)
		if got != want {
			t.Errorf("fileInspectTarget(%s) = %q, want %q", args, got, want)
		}
	}
	if got := fileInspectTarget("read_file", `{"command":"ls mario.html"}`); got != "" {
		t.Errorf("non-shell tools must not match, got %q", got)
	}
}

func TestShellContentInspectionTargetsRecognizePipelines(t *testing.T) {
	cases := map[string][]string{
		`{"command":"cat supermario.html"}`:                                   {"supermario.html"},
		`{"command":"cat supermario.html | head -c 1500"}`:                    {"supermario.html"},
		`{"command":"wc -l supermario.html && head -c 2000 supermario.html"}`: {"supermario.html"},
		`{"command":"ls -la supermario.html | head -3"}`:                      nil,
		`{"command":"grep -n mario supermario.html"}`:                         nil,
	}
	for args, want := range cases {
		got := shellContentInspectionTargets("exec_command", args)
		if len(got) != len(want) {
			t.Errorf("shellContentInspectionTargets(%s) = %v, want %v", args, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("shellContentInspectionTargets(%s) = %v, want %v", args, got, want)
				break
			}
		}
	}
}

func TestShellContentInspectionGuardBlocksVariantsAndStops(t *testing.T) {
	var g guardState
	commands := []string{
		`{"command":"cat supermario.html"}`,
		`{"command":"cat supermario.html | head -c 1500"}`,
		`{"command":"tail -c 2000 supermario.html"}`,
		`{"command":"wc -l supermario.html"}`,
		`{"command":"sed -n '1,80p' supermario.html"}`,
		`{"command":"nl -ba supermario.html | head -40"}`,
	}

	if out := g.recordCallSignature("exec_command", commands[0]); out.Blocked || out.Stop {
		t.Fatalf("first shell read must dispatch, got %+v", out)
	}
	if out := g.recordCallSignature("exec_command", commands[1]); out.Blocked || out.Stop {
		t.Fatalf("second shell read must dispatch with a nudge, got %+v", out)
	}
	if got := g.takeRepeatNudge(); !strings.Contains(got, "supermario.html") {
		t.Fatalf("second shell read must arm a target-specific nudge, got %q", got)
	}

	for i := 2; i < len(commands)-1; i++ {
		out := g.recordCallSignature("exec_command", commands[i])
		if !out.Blocked || out.Stop {
			t.Fatalf("shell read variant %d must be blocked without stopping, got %+v", i+1, out)
		}
	}
	out := g.recordCallSignature("exec_command", commands[len(commands)-1])
	if !out.Stop || !strings.Contains(out.StopNotice, "supermario.html") {
		t.Fatalf("sixth shell read must hard-stop with the target, got %+v", out)
	}
}

func TestShellContentInspectionResetsAfterWrite(t *testing.T) {
	var g guardState
	first := `{"command":"cat supermario.html"}`
	second := `{"command":"head -100 supermario.html"}`
	third := `{"command":"tail -100 supermario.html"}`

	g.recordCallSignature("bash", first)
	g.recordCallSignature("bash", second)
	if out := g.recordCallSignature("bash", third); !out.Blocked {
		t.Fatal("third inspection before a write must be blocked")
	}

	g.recordWrittenFile("supermario.html")
	if out := g.recordCallSignature("bash", first); out.Blocked || out.Stop {
		t.Fatalf("inspection of the new file version must dispatch, got %+v", out)
	}
}
