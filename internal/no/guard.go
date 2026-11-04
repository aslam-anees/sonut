package no

import "context"

// Operation describes a git mutation about to be performed, for Check to
// validate before it runs.
type Operation struct {
	// Kind is the mutation being attempted: "commit", "push", or
	// "force-push". Unknown kinds are treated as unguarded (Check returns no
	// findings) so new sonutgit call sites default to permissive rather than
	// silently blocking.
	Kind string

	// Cwd is the working directory the git command will run in. Required for
	// all kinds.
	Cwd string

	// Branch is the current/target branch for a "commit" or "push". Required
	// for the protected-branch guard.
	Branch string

	// The following fields are only consulted for Kind == "force-push"; see
	// ResolveForcePushDecision.
	Remote      string
	Ref         string
	NewHeadSHA  string
	LastSeenSHA string
	BaseSHA     string
}

const (
	KindCommit    = "commit"
	KindPush      = "push"
	KindForcePush = "force-push"
	RuleProtected = "protected-branch"
	RuleForcePush = "force-push-data-loss"
)

// Check runs the guards relevant to op.Kind and returns any findings. cfg is
// the repo's effective config (see LoadConfig); pass DefaultConfig() when the
// caller has none loaded. run is used only for Kind == "force-push", where a
// live git call is unavoidable to see the current remote state; it may be
// nil for other kinds.
//
// Check never itself decides whether to abort an operation - callers should
// consult Findings.Blocking() (or FirstBlocking()) and act accordingly. This
// keeps Check safe to call speculatively (e.g. for a dry-run preview).
func Check(ctx context.Context, cfg Config, run GitRunner, op Operation) (Findings, error) {
	var findings Findings

	switch op.Kind {
	case KindCommit, KindPush, KindForcePush:
		if f := checkProtectedBranch(cfg, op); f != nil {
			findings = append(findings, f)
		}
	}

	if op.Kind == KindForcePush {
		if !cfg.blockForcePush() {
			return findings, nil
		}
		if run == nil {
			// No git runner available to verify remote state; fail safe with
			// a blocking finding rather than silently skip the check.
			findings = append(findings, &Finding{
				Severity: SeverityBlock,
				Rule:     RuleForcePush,
				Message:  "force-push guard requires a GitRunner to verify remote state; none was provided",
			})
			return findings, nil
		}
		decision, err := ResolveForcePushDecision(ctx, run, op.Remote, op.Ref, op.NewHeadSHA, op.LastSeenSHA, op.BaseSHA)
		if err != nil {
			var discard *ForcePushWouldDiscardError
			if asDiscardError(err, &discard) {
				findings = append(findings, &Finding{
					Severity: SeverityBlock,
					Rule:     RuleForcePush,
					Message:  discard.Error(),
				})
				return findings, nil
			}
			// A real infra failure (fetch/ls-remote error): fail closed with a
			// blocking finding instead of returning a bare error, so callers
			// have a single place (Findings) to check.
			findings = append(findings, &Finding{
				Severity: SeverityBlock,
				Rule:     RuleForcePush,
				Message:  "force-push safety check failed: " + err.Error(),
			})
			return findings, nil
		}
		_ = decision // decision itself carries no finding when it succeeds
	}

	return findings, nil
}

func checkProtectedBranch(cfg Config, op Operation) *Finding {
	if !cfg.isProtectedBranch(op.Branch) {
		return nil
	}
	return &Finding{
		Severity: SeverityWarn,
		Rule:     RuleProtected,
		Message:  "branch \"" + op.Branch + "\" is protected; direct " + op.Kind + " operations on it should go through review",
	}
}

func asDiscardError(err error, target **ForcePushWouldDiscardError) bool {
	if de, ok := err.(*ForcePushWouldDiscardError); ok {
		*target = de
		return true
	}
	return false
}
