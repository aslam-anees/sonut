package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

func TestLooksTechnicalShapeIgnoresSentencePeriods(t *testing.T) {
	// Live false positive: multi-sentence Q&A was "technical" because of ".".
	prose := `I want to wash my car. The car wash is 50 meters away. Should I walk or drive?`
	if looksTechnicalShape(prose) {
		t.Fatalf("prose with sentence periods must not look technical: %q", prose)
	}
	if !looksTechnicalShape("fix foo.go") {
		t.Fatal("foo.go must look technical")
	}
	if !looksTechnicalShape("read src/app.ts") {
		t.Fatal("path with / and .ts must look technical")
	}
	if !looksTechnicalShape("see https://example.com") {
		t.Fatal("URL must look technical")
	}
	if looksTechnicalShape("How are you today?") {
		t.Fatal("plain question must not look technical")
	}
}

func TestCarWashIsFlashQuick(t *testing.T) {
	cases := []string{
		`I want to wash my car. The car wash is 50 meters away. Should I walk or drive?`,
		`I want to wash my car. The car wash is 50 meters away. Should I walk or drive?"`, // trailing quote
		`I need to wash my car, the car wash is 10 m should i drive or walk`,
	}
	for _, q := range cases {
		shape := classifyTurnShape(q)
		if shape != TurnShapeQuestion {
			t.Errorf("classifyTurnShape(%q) = %q, want question", q, shape)
		}
		if !isFlashQA(shape, q) {
			t.Errorf("isFlashQA(%q) = false, want true (avoid 55s prefill)", q)
		}
		if RouteRunMode(q) != RunModeQuick {
			t.Errorf("RouteRunMode(%q) = %q, want quick", q, RouteRunMode(q))
		}
	}
}

func TestFlashSystemPromptMentionsGoalCheck(t *testing.T) {
	if !strings.Contains(flashSystemPrompt, "stated goal") {
		t.Fatalf("flash prompt must include goal-check guidance, got: %q", flashSystemPrompt)
	}
}

func TestWorkspaceFollowupKeepsReadOnlyGroundingAvailable(t *testing.T) {
	cases := []string{
		"look at that path",
		"open the file mentioned above",
		"what is in that folder?",
	}
	for _, prompt := range cases {
		if !isWorkspaceFollowup(prompt) {
			t.Errorf("isWorkspaceFollowup(%q) = false", prompt)
		}
		if isFlashQA(classifyTurnShape(prompt), prompt) {
			t.Errorf("workspace follow-up %q must not be treated as tool-less flash Q&A", prompt)
		}
		if RouteRunMode(prompt) == RunModeQuick {
			t.Errorf("RouteRunMode(%q) = quick; indirect workspace references need grounding", prompt)
		}
	}
}

func TestWorkspaceFollowupPromptKeepsToolsAndAddsReferenceGuidance(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{{
		{Type: sonutruntime.StreamEventText, Content: "I can inspect that once the target is clear."},
		{Type: sonutruntime.StreamEventDone},
	}}}
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreTools(root) {
		registry.Register(tool)
	}
	prompt := "look at that path"
	if _, err := Run(context.Background(), prompt, provider, Options{
		Registry: registry,
		Cwd:      root,
		RunMode:  RouteRunMode(prompt),
	}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("workspace follow-up should use one grounded request, got %d", len(provider.requests))
	}
	request := provider.requests[0]
	if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "WORKSPACE FOLLOW-UP") {
		t.Fatalf("workspace reference guidance missing from user message: %q", request.Messages[len(request.Messages)-1].Content)
	}
	for _, definition := range request.Tools {
		if definition.Name == "list_directory" {
			return
		}
	}
	t.Fatalf("workspace follow-up did not expose list_directory: %#v", request.Tools)
}

func TestFastStartTaskSkipsOnlyShortConcreteWork(t *testing.T) {
	for _, prompt := range []string{
		"build temple run game",
		"fix login test",
		"create notes.md",
	} {
		if !isFastStartTask(prompt) {
			t.Errorf("short concrete task %q was not fast-start eligible", prompt)
		}
	}
	for _, prompt := range []string{
		"who are you",
		"how does the build work?",
		"please analyze the repository architecture and explain the safest migration path",
		"continue",
	} {
		if isFastStartTask(prompt) {
			t.Errorf("prompt %q must keep the normal analysis path", prompt)
		}
	}
}
