package agent

import "testing"

func TestReadFileGuardBlocksUnchangedCrossTurnReads(t *testing.T) {
	g := &guardState{}
	args1 := `{"path":"/workspace/index.html","start_line":1,"end_line":80}`
	args2 := `{"path":"/workspace/index.html","start_line":81,"end_line":160}`
	if out := g.recordReadFile("read_file", args1); out.Blocked {
		t.Fatal("first read was blocked")
	}
	if out := g.recordReadFile("read_file", args2); !out.Blocked {
		t.Fatal("second unchanged read should be blocked")
	}
	g.recordWrittenFile("/workspace/index.html")
	if out := g.recordReadFile("read_file", args1); out.Blocked {
		t.Fatal("read after a write should be allowed")
	}
}

func TestReadFileGuardCanonicalizesRelativeAndAbsolutePaths(t *testing.T) {
	g := newGuardState("/workspace")
	relative := `{"path":"index.html"}`
	absolute := `{"path":"/workspace/index.html","start_line":1,"end_line":80}`
	if out := g.recordReadFile("read_file", relative); out.Blocked {
		t.Fatal("first relative read was blocked")
	}
	if out := g.recordReadFile("read_file", absolute); !out.Blocked {
		t.Fatal("absolute spelling of the same unchanged file should be blocked")
	}

	g.recordWrittenFile("/workspace/index.html")
	if out := g.recordReadFile("read_file", relative); out.Blocked {
		t.Fatal("first read of the new file version was blocked")
	}
	if out := g.recordReadFile("read_file", absolute); !out.Blocked {
		t.Fatal("second read of the new unchanged version should be blocked")
	}
}

func TestReadFileGuardStopsAfterRepeatedSuppression(t *testing.T) {
	g := newGuardState("/workspace")
	args := `{"path":"index.html","start_line":1,"end_line":80}`
	if out := g.recordReadFile("read_file", args); out.Blocked || out.Stop {
		t.Fatalf("first read must be allowed, got %+v", out)
	}
	if out := g.recordReadFile("read_file", `{"path":"/workspace/index.html","start_line":81,"end_line":160}`); !out.Blocked || out.Stop {
		t.Fatalf("second read must be suppressed, got %+v", out)
	}
	for count := 3; count < readFileStopAt; count++ {
		if out := g.recordReadFile("read_file", `{"path":"index.html","start_line":161,"end_line":240}`); !out.Blocked || out.Stop {
			t.Fatalf("unchanged read %d must be suppressed without stopping, got %+v", count, out)
		}
	}
	if out := g.recordReadFile("read_file", `{"path":"index.html","start_line":241,"end_line":320}`); !out.Stop {
		t.Fatalf("repeated unchanged reads must eventually stop the loop, got %+v", out)
	}
}

func TestReadFileGuardCoversBatchReads(t *testing.T) {
	g := newGuardState("/workspace")
	first := `{"paths":["index.html","game.js"],"max_lines":80}`
	second := `{"max_lines":80,"paths":["game.js","index.html"]}`
	if out := g.recordReadFile("read_many_files", first); out.Blocked || out.Stop {
		t.Fatalf("first batch read must be allowed, got %+v", out)
	}
	if out := g.recordReadFile("read_many_files", second); !out.Blocked || out.Stop {
		t.Fatalf("same batch with reordered paths must be suppressed, got %+v", out)
	}
	if out := g.recordReadFile("read_file", `{"path":"index.html","start_line":81,"end_line":160}`); out.Blocked || out.Stop {
		t.Fatalf("targeted read after a line-capped batch must remain allowed, got %+v", out)
	}
}
