// Package no vendors the core git-safety checks from the no-mistakes project
// (https://github.com/kunchenguid/no-mistakes) into sonut. It is deliberately
// a small subset of the upstream tool: only the parts that guard against
// destructive git operations (data-losing force-pushes, mutating a protected
// branch by accident) were ported. See internal/no/WIRING.md for the full
// rationale and what was left behind.
package no

import "context"

// GitRunner runs a git subcommand rooted at a working directory and returns
// trimmed stdout, or an error on non-zero exit. Callers (@code's git wrapper, or tests)
// provide a concrete implementation; production code binds this to a plain
// `git` exec.
type GitRunner func(ctx context.Context, dir string, args ...string) (string, error)

// Severity classifies how a Finding should be handled by the caller.
type Severity string

const (
	// SeverityBlock means the operation MUST NOT proceed.
	SeverityBlock Severity = "block"
	// SeverityWarn means the operation may proceed but the caller should
	// surface the finding to the user.
	SeverityWarn Severity = "warn"
)

// Finding is a single guard result. A nil *Finding (or an empty Findings
// slice) means the operation is clean.
type Finding struct {
	Severity Severity
	Rule     string // short machine-readable id, e.g. "protected-branch"
	Message  string
}

func (f *Finding) Error() string {
	if f == nil {
		return ""
	}
	return f.Message
}

// Findings is an ordered list of Finding, in the order the guards produced
// them. Blocking findings should stop the operation; warnings are advisory.
type Findings []*Finding

// Blocking reports whether any finding in the list is a hard block.
func (fs Findings) Blocking() bool {
	for _, f := range fs {
		if f != nil && f.Severity == SeverityBlock {
			return true
		}
	}
	return false
}

// FirstBlocking returns the first blocking finding, or nil if there is none.
func (fs Findings) FirstBlocking() *Finding {
	for _, f := range fs {
		if f != nil && f.Severity == SeverityBlock {
			return f
		}
	}
	return nil
}
