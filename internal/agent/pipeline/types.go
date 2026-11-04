// Package pipeline implements the 3-role Planner/Executor/Verifier agent
// pipeline: a short, concrete numbered plan (Planner), one step executed at a
// time with minimal context (Executor), and a per-step acceptance check
// against reality (Verifier). All three roles are expected to run against the
// same llama-server using separate parallel slots so each role's
// system-prompt prefix stays KV-cached; this package only owns the
// orchestration and escalation logic, not the model transport, so it can be
// exercised with fakes and wired to the real Planner/Executor/Verifier
// implementations independently.
package pipeline

import (
	"fmt"
	"strings"
	"time"
)

// Step is one numbered, concrete unit of work with a checkable exit
// condition, e.g. "file X exists and contains Y" or "command Z exits 0".
type Step struct {
	Number          int
	Description     string
	AcceptanceCheck string
}

// Plan is the Planner's output: a short, front-loaded list of steps.
type Plan struct {
	Steps []Step
}

// Validate checks the planner's contract before execution. A plan is a small
// set of independently verifiable steps; accepting empty or duplicate steps
// makes the executor improvise and makes verification ambiguous.
func (p Plan) Validate() error {
	if len(p.Steps) == 0 {
		return fmt.Errorf("plan has no steps")
	}
	if len(p.Steps) > 8 {
		return fmt.Errorf("plan has %d steps; maximum is 8", len(p.Steps))
	}
	seen := make(map[string]struct{}, len(p.Steps))
	for i, step := range p.Steps {
		if step.Number != i+1 {
			return fmt.Errorf("step %d has number %d", i+1, step.Number)
		}
		if strings.TrimSpace(step.Description) == "" {
			return fmt.Errorf("step %d has no description", i+1)
		}
		if strings.TrimSpace(step.AcceptanceCheck) == "" {
			return fmt.Errorf("step %d has no acceptance check", i+1)
		}
		key := strings.ToLower(strings.Join(strings.Fields(step.Description), " "))
		if _, exists := seen[key]; exists {
			return fmt.Errorf("step %d duplicates an earlier step", i+1)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ExecContext is the minimal context handed to the Executor for one step:
// the step itself plus relevant file excerpts, never the full run history.
type ExecContext struct {
	Step           Step
	FileExcerpts   map[string]string
	ResearchNotes  string   // set only on the internet-assisted retry rung
	UserAnswer     string   // set only after the ask-user rung resolves
	FailureHistory []string // concrete evidence from every prior failed attempt
}

// ExecResult is what the Executor produced for a step.
type ExecResult struct {
	Summary string
	Error   error
}

// VerifyResult is the Verifier's judgment against the step's acceptance
// check, checked against reality (disk, build, browser snapshot).
type VerifyResult struct {
	Passed   bool
	Evidence string // concrete failure evidence when Passed is false
}

// Rung is a position on the escalation ladder for a stuck step.
type Rung string

const (
	RungNone     Rung = ""         // not yet failed
	RungRetry    Rung = "retry"    // first bounce-back retry (2 local attempts total)
	RungInternet Rung = "internet" // internet-assisted retry after 2 local failures
	RungUser     Rung = "user"     // waiting on the user after internet rung also failed
)

// Attempt records one Executor+Verifier round for a step.
type Attempt struct {
	Rung      Rung
	Result    ExecResult
	Verify    VerifyResult
	Timestamp time.Time
}

// AskUserQuestion is the focused question presented to the user when a step
// exhausts the local and internet rungs.
type AskUserQuestion struct {
	Step           Step
	Attempts       []Attempt
	ResearchNotes  string
	Blocker        string
	FailureHistory []string
	Options        []string
}

// StepResult is the final outcome of running one step through the ladder.
type StepResult struct {
	Step       Step
	Success    bool
	Attempts   []Attempt
	FinalRung  Rung
	UserAnswer string
}
