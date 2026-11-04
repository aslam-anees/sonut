package pipeline

import "fmt"

// RenderStatusLine renders one role's status line for the TUI, e.g.:
//
//	Executor · step 2/5 · executing
//	Executor · step 2/5 · retrying with verifier evidence [escalation: retry]
func RenderStatusLine(s Status, totalSteps int) string {
	line := fmt.Sprintf("%s · step %d/%d · %s", roleLabel(s.Role), s.Step.Number, totalSteps, s.Note)
	if badge := RenderEscalationBadge(s.Rung); badge != "" {
		line += " [" + badge + "]"
	}
	return line
}

// RenderEscalationBadge renders the visible escalation badge showing which
// rung a stuck step is on: retry -> internet -> waiting on user.
func RenderEscalationBadge(r Rung) string {
	switch r {
	case RungRetry:
		return "escalation: retry"
	case RungInternet:
		return "escalation: internet"
	case RungUser:
		return "escalation: waiting on user"
	default:
		return ""
	}
}

func roleLabel(r Role) string {
	switch r {
	case RolePlanner:
		return "Planner"
	case RoleExecutor:
		return "Executor"
	case RoleVerifier:
		return "Verifier"
	default:
		return string(r)
	}
}
