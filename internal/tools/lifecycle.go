package tools

import (
	"context"
	"strings"
)

// NormalizeResultLifecycle supplies terminal metadata for existing tools that
// still return only Status/Output. It runs after redaction and never changes
// Output, preserving the compact text sent to the model.
func NormalizeResultLifecycle(result *Result, ctxErr error) {
	if result == nil {
		return
	}
	if ctxErr == context.Canceled {
		result.Status = StatusError
		result.Lifecycle = LifecycleCancelled
		result.Error = &StructuredError{Kind: ErrorCancelled, Code: "cancelled", Message: "tool call cancelled"}
		return
	}
	if ctxErr == context.DeadlineExceeded {
		result.Status = StatusError
		result.Lifecycle = LifecycleTimedOut
		result.Error = &StructuredError{Kind: ErrorTimeout, Code: "timeout", Message: "tool call timed out", Retryable: true, Hint: "retry only if the operation is safe to repeat"}
		return
	}
	if result.Status != StatusError {
		if result.Lifecycle == "" || result.Lifecycle == LifecyclePending || result.Lifecycle == LifecycleRunning {
			result.Lifecycle = LifecycleSucceeded
		}
		return
	}
	if result.Lifecycle == "" || result.Lifecycle == LifecyclePending || result.Lifecycle == LifecycleRunning {
		result.Lifecycle = inferLifecycle(result.Output)
	}
	if result.Error == nil {
		kind := inferErrorKind(result.Output)
		result.Error = &StructuredError{Kind: kind, Code: string(kind), Message: firstErrorLine(result.Output), Retryable: retryable(kind), Hint: hint(kind)}
	}
}

func inferLifecycle(output string) LifecycleStatus {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "cancelled") || strings.Contains(lower, "canceled"):
		return LifecycleCancelled
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "deadline exceeded"):
		return LifecycleTimedOut
	case strings.Contains(lower, "sandbox"), strings.Contains(lower, "permission denied"), strings.Contains(lower, "approval required"), strings.Contains(lower, "blocked by"):
		return LifecycleBlocked
	default:
		return LifecycleFailed
	}
}

func inferErrorKind(output string) ErrorKind {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "invalid argument"), strings.Contains(lower, "failed to parse arguments"):
		return ErrorInvalidArguments
	case strings.Contains(lower, "not found"), strings.Contains(lower, "does not exist"):
		return ErrorNotFound
	case strings.Contains(lower, "hook") && strings.Contains(lower, "block"):
		return ErrorHookBlocked
	case strings.Contains(lower, "sandbox"):
		return ErrorSandboxBlocked
	case strings.Contains(lower, "permission"), strings.Contains(lower, "approval"):
		return ErrorPermissionDenied
	case strings.Contains(lower, "changed on disk"), strings.Contains(lower, "conflict"):
		return ErrorConflict
	case strings.Contains(lower, "cancelled"), strings.Contains(lower, "canceled"):
		return ErrorCancelled
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "deadline exceeded"):
		return ErrorTimeout
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "too many requests"):
		return ErrorRateLimited
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "network"), strings.Contains(lower, "dns"):
		return ErrorNetwork
	case strings.Contains(lower, "unavailable"), strings.Contains(lower, "temporarily"):
		return ErrorServiceUnavailable
	default:
		return ErrorExecution
	}
}

func retryable(kind ErrorKind) bool {
	return kind == ErrorTimeout || kind == ErrorRateLimited || kind == ErrorNetwork || kind == ErrorServiceUnavailable
}

func hint(kind ErrorKind) string {
	switch kind {
	case ErrorInvalidArguments:
		return "check the tool arguments and retry"
	case ErrorPermissionDenied, ErrorSandboxBlocked, ErrorHookBlocked:
		return "request the required approval or use an allowed operation"
	case ErrorConflict:
		return "read the latest file contents before retrying"
	case ErrorTimeout, ErrorRateLimited, ErrorNetwork, ErrorServiceUnavailable:
		return "retry only if the operation is safe to repeat"
	default:
		return "inspect the error details and adjust the next action"
	}
}

func firstErrorLine(output string) string {
	if line, _, ok := strings.Cut(strings.TrimSpace(output), "\n"); ok {
		return strings.TrimSpace(line)
	}
	if output = strings.TrimSpace(output); output != "" {
		return output
	}
	return "tool failed without an error message"
}
