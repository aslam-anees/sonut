package no

import (
	"context"
	"fmt"
	"strings"
)

// This file is ported from no-mistakes' internal/pipeline/steps/forcepush.go
// (resolveForcePushDecision and friends), trimmed to the pure decision logic
// and re-pointed at sonut's GitRunner. See internal/no/WIRING.md.

// ForcePushDecision describes how to push a head to a remote branch safely.
// Exactly one of NewBranch / UpToDate is true, or neither (a guarded
// force-push anchored to RemoteSHA is required).
type ForcePushDecision struct {
	RemoteSHA string // current remote head; the lease anchor for a force-push
	NewBranch bool   // the branch does not exist on the remote -> plain push
	UpToDate  bool   // the remote already points at the head -> no push needed
}

// ForcePushWouldDiscardError reports that a force-push would discard commits
// present on the remote branch that were never incorporated locally.
// Refusing is the whole point: it is what keeps a stale-base rebase or an
// out-of-band push from silently dropping work that already landed upstream.
type ForcePushWouldDiscardError struct {
	Ref       string
	RemoteSHA string
	Dropped   []string
}

func (e *ForcePushWouldDiscardError) Error() string {
	sample := e.Dropped
	if len(sample) > 5 {
		sample = sample[:5]
	}
	return fmt.Sprintf(
		"refusing to force-push %s: remote head %s carries %d commit(s) never incorporated locally (e.g. %s); pushing would discard upstream work. Re-fetch and rebase onto the current remote, or push manually if this overwrite is intended.",
		e.Ref, shortSHA(e.RemoteSHA), len(e.Dropped), strings.Join(shortSHAs(sample), ", "),
	)
}

// ResolveForcePushDecision re-reads the current state of ref on the push
// remote and decides whether force-pushing newHeadSHA would discard commits
// never seen locally. It returns a decision the caller acts on, or a non-nil
// error when the push must NOT proceed: either git failed (fail safe rather
// than degrade to a blind --force), or the push would discard unseen
// upstream commits.
//
// lastSeenSHA is the remote head last observed for this branch (e.g. the
// remote-tracking ref last fetched). When the remote still points there the
// push is safe (it only fast-forwards or replays prior state). Otherwise the
// remote moved out of band and the push is allowed only when every commit
// now on the remote is already incorporated, by content (patch-id), into
// newHeadSHA, or is part of history already known (reachable from baseSHA)
// and thus a deliberate rewrite rather than a clobber. Anything else is
// refused rather than discarded.
func ResolveForcePushDecision(ctx context.Context, run GitRunner, pushURL, ref, newHeadSHA, lastSeenSHA, baseSHA string) (ForcePushDecision, error) {
	current, err := lsRemoteSHA(ctx, run, pushURL, ref)
	if err != nil {
		return ForcePushDecision{}, fmt.Errorf("resolve remote head for %s: %w", ref, err)
	}
	if current == "" {
		return ForcePushDecision{NewBranch: true}, nil
	}
	if current == newHeadSHA {
		return ForcePushDecision{RemoteSHA: current, UpToDate: true}, nil
	}
	if lastSeenSHA != "" && current == lastSeenSHA {
		return ForcePushDecision{RemoteSHA: current}, nil
	}
	dropped, err := remoteCommitsNotIncorporated(ctx, run, pushURL, ref, newHeadSHA, current, baseSHA)
	if err != nil {
		return ForcePushDecision{}, fmt.Errorf("verify force-push safety for %s: %w", ref, err)
	}
	if len(dropped) == 0 {
		return ForcePushDecision{RemoteSHA: current}, nil
	}
	return ForcePushDecision{}, &ForcePushWouldDiscardError{Ref: ref, RemoteSHA: current, Dropped: dropped}
}

// lsRemoteSHA returns the SHA a ref resolves to on a remote, or "" when the
// ref does not exist there.
func lsRemoteSHA(ctx context.Context, run GitRunner, remote, ref string) (string, error) {
	out, err := run(ctx, "", "ls-remote", remote, ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// remoteCommitsNotIncorporated returns the commits reachable from remoteSHA
// whose changes are not already present (by patch-id) in newHeadSHA and are
// not part of history already known (reachable from baseSHA). It fetches the
// remote branch tip into FETCH_HEAD first so the commits are available
// locally for the comparison, without disturbing remote-tracking refs.
func remoteCommitsNotIncorporated(ctx context.Context, run GitRunner, pushURL, ref, newHeadSHA, remoteSHA, baseSHA string) ([]string, error) {
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if _, err := run(ctx, "", "fetch", "--no-tags", pushURL, "refs/heads/"+branch); err != nil {
		return nil, fmt.Errorf("fetch remote branch: %w", err)
	}
	args := []string{"rev-list", "--cherry-pick", "--right-only", newHeadSHA + "..." + remoteSHA}
	if baseSHA != "" && !isZeroSHA(baseSHA) {
		if _, err := run(ctx, "", "rev-parse", "--verify", "--quiet", baseSHA+"^{commit}"); err == nil {
			args = append(args, "^"+baseSHA)
		}
	}
	out, err := run(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			commits = append(commits, line)
		}
	}
	return commits, nil
}

// isZeroSHA reports whether sha is git's null/zero ref (40 zeros), used for
// new or deleted branches.
func isZeroSHA(sha string) bool {
	return sha == "0000000000000000000000000000000000000000"
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func shortSHAs(shas []string) []string {
	out := make([]string, len(shas))
	for i, s := range shas {
		out[i] = shortSHA(s)
	}
	return out
}
