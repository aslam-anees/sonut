package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// StatusListener receives per-role status updates as the ladder progresses,
// for the TUI's per-role status lines and escalation badge.
type StatusListener interface {
	OnStatus(status Status)
}

// Status is one point-in-time snapshot of a running step, enough to render
// "Planner/Executor/Verifier with current step" plus the escalation badge.
type Status struct {
	Role Role
	Step Step
	Rung Rung
	Note string
}

// Role identifies which pipeline role is currently active, for the TUI's
// per-role status lines.
type Role string

const (
	RolePlanner  Role = "planner"
	RoleExecutor Role = "executor"
	RoleVerifier Role = "verifier"
)

// StatusListenerFunc adapts a plain function to StatusListener.
type StatusListenerFunc func(Status)

func (f StatusListenerFunc) OnStatus(s Status) { f(s) }

// Orchestrator runs the Planner/Executor/Verifier pipeline with the strict,
// local-only escalation ladder:
//
//  1. Executor fails a step -> Verifier bounces it back with the concrete
//     failure evidence; Executor retries once (2 local attempts total).
//  2. Second failure -> internet-assisted retry: a mandatory research phase
//     looks up the exact error/API, and the retry prompt includes the
//     findings. Skipped when offline, going straight to rung 3.
//  3. Still failing -> ask the user: pause and present what was attempted,
//     the exact blocker, and 2-3 concrete options. The user's answer is
//     injected as ground truth and the step retries once more. The ladder
//     never loops past this point and never calls an external foundational
//     model.
type Orchestrator struct {
	Planner    Planner
	Executor   Executor
	Verifier   Verifier
	Researcher Researcher
	AskUser    UserAsker
	Online     OnlineChecker
	Status     StatusListener
}

func (o *Orchestrator) emit(s Status) {
	if o.Status != nil {
		o.Status.OnStatus(s)
	}
}

// Run plans the request, then runs every step through the escalation ladder
// in order, stopping at the first step that fails even after the user rung
// (checkpointing means any step can later be retried in isolation without
// redoing prior steps).
func (o *Orchestrator) Run(ctx context.Context, request string) ([]StepResult, error) {
	o.emit(Status{Role: RolePlanner, Note: "planning"})
	plan, err := o.Planner.Plan(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("plan contract: %w", err)
	}

	results := make([]StepResult, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		result := o.RunStep(ctx, step)
		results = append(results, result)
		if !result.Success {
			break
		}
	}
	return results, nil
}

// RunStep runs a single step through the full escalation ladder. It is
// exported so a step can be retried in isolation (checkpoint-per-step)
// without redoing earlier steps.
func (o *Orchestrator) RunStep(ctx context.Context, step Step) StepResult {
	ec := ExecContext{Step: step}
	failureHistory := make([]string, 0, 3)

	// Rungs 0 and 1: up to two local Executor+Verifier attempts.
	var attempts []Attempt
	attempt, ok := o.attempt(ctx, ec, RungNone)
	attempts = append(attempts, attempt)
	if ok {
		return StepResult{Step: step, Success: true, Attempts: attempts, FinalRung: RungNone}
	}

	o.emit(Status{Role: RoleExecutor, Step: step, Rung: RungRetry, Note: "retrying with verifier evidence"})
	failureHistory = appendFailure(failureHistory, attempt.Verify.Evidence)
	ec.FailureHistory = append([]string(nil), failureHistory...)
	ec.researchOrEvidence(attempt.Verify.Evidence, failureHistory)
	attempt, ok = o.attempt(ctx, ec, RungRetry)
	attempts = append(attempts, attempt)
	if ok {
		return StepResult{Step: step, Success: true, Attempts: attempts, FinalRung: RungRetry}
	}
	failureHistory = appendFailure(failureHistory, attempt.Verify.Evidence)

	// Rung 2: internet-assisted retry, skipped when offline.
	if o.Researcher != nil && o.isOnline(ctx) {
		o.emit(Status{Role: RoleExecutor, Step: step, Rung: RungInternet, Note: "researching the failure before retrying"})
		query := step.AcceptanceCheck + " — " + attempt.Verify.Evidence
		findings, err := o.Researcher.Research(ctx, query)
		if err == nil && findings != "" {
			ec.ResearchNotes = findings
			ec.FailureHistory = append([]string(nil), failureHistory...)
			attempt, ok = o.attempt(ctx, ec, RungInternet)
			attempts = append(attempts, attempt)
			if ok {
				return StepResult{Step: step, Success: true, Attempts: attempts, FinalRung: RungInternet}
			}
		}
	}

	// Rung 3: ask the user. Never loop past this point.
	if o.AskUser == nil {
		return StepResult{Step: step, Success: false, Attempts: attempts, FinalRung: RungUser}
	}
	o.emit(Status{Role: RoleExecutor, Step: step, Rung: RungUser, Note: "waiting on user"})
	question := AskUserQuestion{
		Step:           step,
		Attempts:       attempts,
		ResearchNotes:  ec.ResearchNotes,
		Blocker:        attempt.Verify.Evidence,
		FailureHistory: append([]string(nil), failureHistory...),
		Options:        []string{"skip this step", "try a different approach", "give me the exact fix"},
	}
	answer, err := o.AskUser.AskUser(ctx, question)
	if err != nil || answer == "" {
		return StepResult{Step: step, Success: false, Attempts: attempts, FinalRung: RungUser}
	}
	ec.UserAnswer = answer
	attempt, ok = o.attempt(ctx, ec, RungUser)
	attempts = append(attempts, attempt)
	return StepResult{Step: step, Success: ok, Attempts: attempts, FinalRung: RungUser, UserAnswer: answer}
}

func appendFailure(history []string, evidence string) []string {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return history
	}
	for _, previous := range history {
		if previous == evidence {
			return history
		}
	}
	return append(history, evidence)
}

func (ec *ExecContext) researchOrEvidence(evidence string, history []string) {
	// The retry rung folds the verifier's concrete failure evidence straight
	// into the excerpt map so the executor sees exactly what broke, without
	// re-sending the whole run history.
	if ec.FileExcerpts == nil {
		ec.FileExcerpts = map[string]string{}
	}
	ec.FileExcerpts["__verifier_evidence__"] = evidence
	if len(history) > 1 {
		ec.FileExcerpts["__verifier_history__"] = strings.Join(history, "\n- ")
	}
}

func (o *Orchestrator) isOnline(ctx context.Context) bool {
	if o.Online == nil {
		return true
	}
	return o.Online.Online(ctx)
}

func (o *Orchestrator) attempt(ctx context.Context, ec ExecContext, rung Rung) (Attempt, bool) {
	o.emit(Status{Role: RoleExecutor, Step: ec.Step, Rung: rung, Note: "executing"})
	result, err := o.Executor.Execute(ctx, ec)
	if err != nil {
		result.Error = err
	}

	o.emit(Status{Role: RoleVerifier, Step: ec.Step, Rung: rung, Note: "verifying"})
	verify, verr := o.Verifier.Verify(ctx, ec, result)
	if verr != nil {
		verify = VerifyResult{Passed: false, Evidence: verr.Error()}
	}

	attempt := Attempt{Rung: rung, Result: result, Verify: verify, Timestamp: time.Now().UTC()}
	return attempt, verify.Passed
}
