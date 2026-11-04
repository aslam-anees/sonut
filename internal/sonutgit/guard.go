package sonutgit

import (
	"context"
	"fmt"

	"github.com/aistatine/sonut/internal/no"
)

// GuardError is returned when internal/no reports a blocking finding for a
// git mutation sonutgit was about to perform. Callers can type-assert to
// inspect the underlying findings, or just surface Error().
type GuardError struct {
	Findings no.Findings
}

func (e *GuardError) Error() string {
	f := e.Findings.FirstBlocking()
	if f == nil {
		return "git operation blocked by no-mistakes guard"
	}
	return fmt.Sprintf("git operation blocked by no-mistakes guard (%s): %s", f.Rule, f.Message)
}

// guardCommit runs internal/no's guards against a commit about to happen in
// root on the given branch. It never returns an error for a missing or
// permissive config - see no.LoadConfig - so a repo with no
// .no-mistakes.yaml behaves exactly as before this hook was added. A
// blocking finding is surfaced as a *GuardError; warnings are returned
// alongside a nil error so callers can log/report them without stopping the
// commit.
//
// runGit is optional and only consulted for operations this hook does not
// currently issue (force-push); it is threaded through so future callers
// (e.g. a push helper) can reuse the same guard without duplicating the
// adapter.
func guardCommit(ctx context.Context, root, branch string, runGit Runner) (no.Findings, error) {
	cfg, err := no.LoadConfig(root)
	if err != nil {
		// A malformed .no-mistakes.yaml is a real misconfiguration, but it
		// must not brick every commit in the repo: fail open to the
		// permissive default and let the caller keep working.
		cfg = no.DefaultConfig()
	}

	var adapted no.GitRunner
	if runGit != nil {
		adapted = func(ctx context.Context, dir string, args ...string) (string, error) {
			result, err := runGit(ctx, dir, args...)
			return gitResultOutput(result, err)
		}
	}

	findings, err := no.Check(ctx, cfg, adapted, no.Operation{
		Kind:   no.KindCommit,
		Cwd:    root,
		Branch: branch,
	})
	if err != nil {
		return nil, err
	}
	if findings.Blocking() {
		return findings, &GuardError{Findings: findings}
	}
	return findings, nil
}
