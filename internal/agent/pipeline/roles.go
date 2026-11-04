package pipeline

import "context"

// Planner converts a user request into a short, concrete, numbered plan.
// Real implementations call the shared llama-server at super effort with no
// tools.
type Planner interface {
	Plan(ctx context.Context, request string) (Plan, error)
}

// Executor executes exactly one step with tools, given minimal context. Real
// implementations call the shared llama-server at flash effort.
type Executor interface {
	Execute(ctx context.Context, ec ExecContext) (ExecResult, error)
}

// Verifier checks a step's acceptance check against reality after execution.
// Real implementations call the shared llama-server at flash effort.
type Verifier interface {
	Verify(ctx context.Context, ec ExecContext, result ExecResult) (VerifyResult, error)
}

// Researcher performs the mandatory research phase of the internet-assisted
// retry rung: it looks up the exact error message or API in question and
// returns findings to fold into the retry prompt.
type Researcher interface {
	Research(ctx context.Context, query string) (findings string, err error)
}

// UserAsker pauses the run and presents a focused question to the user,
// returning their answer as ground truth for the step. It is never called
// more than once per step.
type UserAsker interface {
	AskUser(ctx context.Context, question AskUserQuestion) (answer string, err error)
}

// OnlineChecker reports whether internet access is currently available, so
// the ladder can skip straight to the ask-user rung when offline.
type OnlineChecker interface {
	Online(ctx context.Context) bool
}

// OnlineCheckerFunc adapts a plain function to OnlineChecker.
type OnlineCheckerFunc func(ctx context.Context) bool

func (f OnlineCheckerFunc) Online(ctx context.Context) bool { return f(ctx) }
