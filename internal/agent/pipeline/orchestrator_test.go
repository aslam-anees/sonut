package pipeline

import (
	"context"
	"errors"
	"testing"
)

type fakePlanner struct{ plan Plan }

func (f fakePlanner) Plan(ctx context.Context, request string) (Plan, error) { return f.plan, nil }

type scriptedExecutor struct {
	calls []ExecContext
}

func (e *scriptedExecutor) Execute(ctx context.Context, ec ExecContext) (ExecResult, error) {
	e.calls = append(e.calls, ec)
	return ExecResult{Summary: "ran step " + ec.Step.Description}, nil
}

type scriptedVerifier struct{ passAt Rung }

func (v scriptedVerifier) Verify(ctx context.Context, ec ExecContext, result ExecResult) (VerifyResult, error) {
	rung := currentRungFor(ec)
	if rung == v.passAt {
		return VerifyResult{Passed: true}, nil
	}
	return VerifyResult{Passed: false, Evidence: "step still fails at rung " + string(rung)}, nil
}

// currentRungFor infers the rung from ExecContext shape for the fake — real
// verifiers don't need this, but the ladder doesn't pass the rung directly to
// Verify, so tests key off what the orchestrator folds into ExecContext.
func currentRungFor(ec ExecContext) Rung {
	switch {
	case ec.UserAnswer != "":
		return RungUser
	case ec.ResearchNotes != "":
		return RungInternet
	case ec.FileExcerpts["__verifier_evidence__"] != "":
		return RungRetry
	default:
		return RungNone
	}
}

type fakeResearcher struct {
	findings string
	called   bool
}

func (r *fakeResearcher) Research(ctx context.Context, query string) (string, error) {
	r.called = true
	return r.findings, nil
}

type fakeAsker struct {
	answer string
	called bool
	lastQ  AskUserQuestion
}

func (a *fakeAsker) AskUser(ctx context.Context, q AskUserQuestion) (string, error) {
	a.called = true
	a.lastQ = q
	return a.answer, nil
}

func testPlan() Plan {
	return Plan{Steps: []Step{{Number: 1, Description: "create file", AcceptanceCheck: "file X exists"}}}
}

func TestPlanValidateRejectsAmbiguousContracts(t *testing.T) {
	cases := []Plan{
		{},
		{Steps: []Step{{Number: 1, Description: "write", AcceptanceCheck: ""}}},
		{Steps: []Step{
			{Number: 1, Description: "write file", AcceptanceCheck: "file exists"},
			{Number: 2, Description: "write file", AcceptanceCheck: "file is valid"},
		}},
	}
	for i, plan := range cases {
		if err := plan.Validate(); err == nil {
			t.Fatalf("case %d: expected invalid plan", i)
		}
	}
}

func TestPlanValidateAcceptsOrderedSteps(t *testing.T) {
	plan := Plan{Steps: []Step{
		{Number: 1, Description: "write file", AcceptanceCheck: "file exists"},
		{Number: 2, Description: "run tests", AcceptanceCheck: "tests pass"},
	}}
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

func TestOrchestrator_SucceedsOnFirstAttempt(t *testing.T) {
	o := &Orchestrator{
		Planner:  fakePlanner{plan: testPlan()},
		Executor: &scriptedExecutor{},
		Verifier: scriptedVerifier{passAt: RungNone},
	}
	results, err := o.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].Success || results[0].FinalRung != RungNone {
		t.Fatalf("unexpected result: %+v", results)
	}
	if len(results[0].Attempts) != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", len(results[0].Attempts))
	}
}

func TestOrchestrator_SecondLocalFailureTriggersInternetRung(t *testing.T) {
	researcher := &fakeResearcher{findings: "the fix is to use flag --foo"}
	executor := &scriptedExecutor{}
	o := &Orchestrator{
		Planner:    fakePlanner{plan: testPlan()},
		Executor:   executor,
		Verifier:   scriptedVerifier{passAt: RungInternet},
		Researcher: researcher,
		Online:     OnlineCheckerFunc(func(context.Context) bool { return true }),
	}
	results, err := o.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result := results[0]
	if !result.Success || result.FinalRung != RungInternet {
		t.Fatalf("expected success at internet rung, got %+v", result)
	}
	if !researcher.called {
		t.Fatalf("expected researcher to be invoked after 2 local failures")
	}
	// 2 local attempts (rung none + retry) + 1 internet attempt.
	if len(result.Attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d: %+v", len(result.Attempts), result.Attempts)
	}
	if len(executor.calls) != 3 || len(executor.calls[1].FailureHistory) != 1 || len(executor.calls[2].FailureHistory) != 2 {
		t.Fatalf("expected cumulative verifier evidence on retries: %#v", executor.calls)
	}
}

func TestOrchestrator_OfflineSkipsInternetRungGoesStraightToUser(t *testing.T) {
	researcher := &fakeResearcher{findings: "should never be used"}
	asker := &fakeAsker{answer: "use approach B"}
	o := &Orchestrator{
		Planner:    fakePlanner{plan: testPlan()},
		Executor:   &scriptedExecutor{},
		Verifier:   scriptedVerifier{passAt: RungUser},
		Researcher: researcher,
		Online:     OnlineCheckerFunc(func(context.Context) bool { return false }),
		AskUser:    asker,
	}
	results, err := o.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if researcher.called {
		t.Fatalf("researcher must not be called while offline")
	}
	if !asker.called {
		t.Fatalf("expected ask-user rung to be reached")
	}
	result := results[0]
	if !result.Success || result.FinalRung != RungUser {
		t.Fatalf("expected success at user rung, got %+v", result)
	}
	if len(asker.lastQ.Options) < 2 || len(asker.lastQ.Options) > 3 {
		t.Fatalf("expected 2-3 concrete options, got %v", asker.lastQ.Options)
	}
}

func TestOrchestrator_UserAnswerUnblocksAndPipelineContinues(t *testing.T) {
	asker := &fakeAsker{answer: "the button selector is #submit"}
	plan := Plan{Steps: []Step{
		{Number: 1, Description: "click submit", AcceptanceCheck: "form is submitted"},
		{Number: 2, Description: "verify confirmation", AcceptanceCheck: "confirmation banner visible"},
	}}
	o := &Orchestrator{
		Planner:  fakePlanner{plan: plan},
		Executor: &scriptedExecutor{},
		Verifier: scriptedVerifier{passAt: RungUser},
		Online:   OnlineCheckerFunc(func(context.Context) bool { return false }),
		AskUser:  asker,
	}
	results, err := o.Run(context.Background(), "submit the form")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected the pipeline to continue past the unblocked step, got %d results", len(results))
	}
	if !results[0].Success || results[0].UserAnswer != asker.answer {
		t.Fatalf("expected step 1 to succeed with the user's answer recorded, got %+v", results[0])
	}
	if !results[1].Success {
		t.Fatalf("expected step 2 to also succeed, got %+v", results[1])
	}
}

func TestOrchestrator_NeverLoopsPastUserRung(t *testing.T) {
	asker := &fakeAsker{answer: "still doesn't work"}
	o := &Orchestrator{
		Planner:  fakePlanner{plan: testPlan()},
		Executor: &scriptedExecutor{},
		// Never passes, even after the user answers: this exercises the "give
		// up after the user rung" path rather than looping forever.
		Verifier: scriptedVerifier{passAt: "never"},
		Online:   OnlineCheckerFunc(func(context.Context) bool { return false }),
		AskUser:  asker,
	}
	results, err := o.Run(context.Background(), "do the impossible thing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result := results[0]
	if result.Success {
		t.Fatalf("expected the step to remain failed after the user rung")
	}
	if result.FinalRung != RungUser {
		t.Fatalf("expected final rung to be user, got %v", result.FinalRung)
	}
	// Exactly one AskUser call: retry + internet(skipped) + one user attempt.
	if len(result.Attempts) != 3 {
		t.Fatalf("expected 3 attempts (retry, and one post-user), got %d", len(result.Attempts))
	}
}

func TestOrchestrator_PlanErrorPropagates(t *testing.T) {
	o := &Orchestrator{
		Planner:  fakePlannerErr{err: errors.New("model unavailable")},
		Executor: &scriptedExecutor{},
		Verifier: scriptedVerifier{passAt: RungNone},
	}
	if _, err := o.Run(context.Background(), "do the thing"); err == nil {
		t.Fatalf("expected plan error to propagate")
	}
}

type fakePlannerErr struct{ err error }

func (f fakePlannerErr) Plan(ctx context.Context, request string) (Plan, error) {
	return Plan{}, f.err
}

func TestRenderStatusLine_IncludesEscalationBadge(t *testing.T) {
	line := RenderStatusLine(Status{Role: RoleExecutor, Step: Step{Number: 2}, Rung: RungInternet, Note: "researching"}, 5)
	if line != "Executor · step 2/5 · researching [escalation: internet]" {
		t.Fatalf("unexpected line: %q", line)
	}
	clean := RenderStatusLine(Status{Role: RoleVerifier, Step: Step{Number: 1}, Note: "verifying"}, 3)
	if clean != "Verifier · step 1/3 · verifying" {
		t.Fatalf("unexpected line without badge: %q", clean)
	}
}
