package llama

import "testing"

func TestThinkStripperSplitAcrossChunks(t *testing.T) {
	s := &thinkStripper{}
	var text, reasoning string
	feed := func(delta string) {
		tOut, rOut := s.feed(delta)
		text += tOut
		reasoning += rOut
	}

	// "<think>" split across three chunks, plus "</think>" split across two.
	feed("hello <thi")
	feed("nk>internal reasoning</thi")
	feed("nk> world")
	tOut, rOut := s.flush()
	text += tOut
	reasoning += rOut

	if text != "hello  world" {
		t.Fatalf("text = %q, want %q", text, "hello  world")
	}
	if reasoning != "internal reasoning" {
		t.Fatalf("reasoning = %q, want %q", reasoning, "internal reasoning")
	}
}

func TestThinkStripperFlushUnclosedAtEOS(t *testing.T) {
	s := &thinkStripper{}
	text, reasoning := s.feed("<think>never closes")
	tOut, rOut := s.flush()
	text += tOut
	reasoning += rOut

	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
	if reasoning != "never closes" {
		t.Fatalf("reasoning = %q, want %q", reasoning, "never closes")
	}
}

func TestThinkStripperFlushPartialTagAtEOS(t *testing.T) {
	s := &thinkStripper{}
	text, _ := s.feed("plain text <thi")
	tOut, _ := s.flush()
	text += tOut

	if text != "plain text <thi" {
		t.Fatalf("text = %q, want %q", text, "plain text <thi")
	}
}

func TestThinkStripperOrphanCloseTag(t *testing.T) {
	s := &thinkStripper{}
	text, reasoning := s.feed("no open tag here</think> trailing")
	if reasoning != "" {
		t.Fatalf("reasoning = %q, want empty", reasoning)
	}
	if text != "no open tag here trailing" {
		t.Fatalf("text = %q, want stray tag stripped", text)
	}
}

// TestThinkStripperThinkingTag tests <thinking>…</thinking> variant.
func TestThinkStripperThinkingTag(t *testing.T) {
	s := &thinkStripper{}
	var text, reasoning string
	feed := func(delta string) {
		tOut, rOut := s.feed(delta)
		text += tOut
		reasoning += rOut
	}

	feed("<thinking>deep thought</thinking> answer")
	tOut, rOut := s.flush()
	text += tOut
	reasoning += rOut

	if text != " answer" {
		t.Fatalf("text = %q, want %q", text, " answer")
	}
	if reasoning != "deep thought" {
		t.Fatalf("reasoning = %q, want %q", reasoning, "deep thought")
	}
}

// TestThinkStripperThinkingTagSplitAcrossChunks tests <thinking> split at the boundary.
func TestThinkStripperThinkingTagSplitAcrossChunks(t *testing.T) {
	s := &thinkStripper{}
	var text, reasoning string
	feed := func(delta string) {
		tOut, rOut := s.feed(delta)
		text += tOut
		reasoning += rOut
	}

	feed("before <thin")
	feed("king>inner</thin")
	feed("king> after")
	tOut, rOut := s.flush()
	text += tOut
	reasoning += rOut

	if text != "before  after" {
		t.Fatalf("text = %q, want %q", text, "before  after")
	}
	if reasoning != "inner" {
		t.Fatalf("reasoning = %q, want %q", reasoning, "inner")
	}
}

// TestThinkStripperThinkingUnclosed verifies unclosed <thinking> goes to reasoning.
func TestThinkStripperThinkingUnclosed(t *testing.T) {
	s := &thinkStripper{}
	text, reasoning := s.feed("<thinking>no close tag")
	tOut, rOut := s.flush()
	text += tOut
	reasoning += rOut

	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
	if reasoning != "no close tag" {
		t.Fatalf("reasoning = %q, want %q", reasoning, "no close tag")
	}
}

// TestThinkStripperOrphanCloseThinkingTag verifies stray </thinking> is stripped.
func TestThinkStripperOrphanCloseThinkingTag(t *testing.T) {
	s := &thinkStripper{}
	text, reasoning := s.feed("text</thinking>more")
	tOut, rOut := s.flush()
	text += tOut
	reasoning += rOut

	if reasoning != "" {
		t.Fatalf("reasoning = %q, want empty", reasoning)
	}
	if text != "textmore" {
		t.Fatalf("text = %q, want %q", text, "textmore")
	}
}
