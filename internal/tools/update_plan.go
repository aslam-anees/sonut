package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type PlanItem struct {
	ID       string
	Content  string
	Status   string
	Notes    string
	Criteria string // done-criteria: what must be true before marking completed
}

type updatePlanTool struct {
	baseTool
	// mu guards currentPlan: Run() writes it on the agent goroutine while
	// CurrentPlan()/ClearPlan() are called from the TUI goroutine (e.g. /plan).
	mu          sync.Mutex
	currentPlan []PlanItem
}

func NewUpdatePlanTool() *updatePlanTool {
	return &updatePlanTool{
		baseTool: baseTool{
			name: "update_plan",
			description: "Create or update the session plan. " +
				"Pass the full ordered step list; it replaces the previous plan and persists for the whole session " +
				"until the task is done or /clear is run. " +
				"Each item requires `content`; add `criteria` describing what must be verified before marking done. " +
				"A step MUST NOT be marked completed unless its criteria are met and verified with a tool call. " +
				"Status values coerce to pending/in_progress/completed/failed; at most one item is in_progress.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"plan": {
						Type:        "array",
						Description: "Ordered list of plan items, replacing any previous plan.",
						Items: &PropertySchema{
							Type: "object",
							Properties: map[string]PropertySchema{
								"content": {
									Type:        "string",
									Description: "The plan step description.",
								},
								"status": {
									Type:        "string",
									Description: "Step status. Only set to completed AFTER verifying criteria are met.",
									Enum:        []string{"pending", "in_progress", "completed", "failed"},
								},
								"criteria": {
									Type:        "string",
									Description: "Done-criteria: what must be true before this step is completed. E.g. file exists, build passes, screenshot matches. Verified via tool call before marking done.",
								},
								"notes": {
									Type:        "string",
									Description: "Optional notes or observations for this step.",
								},
							},
							Required: []string{"content"},
						},
					},
				},
				Required:             []string{"plan"},
				AdditionalProperties: false,
			},
			safety: readOnlySafety("Updates in-memory planning state only."),
		},
	}
}

func (tool *updatePlanTool) Run(_ context.Context, args map[string]any) Result {
	plan, err := parsePlanItems(args["plan"])
	if err != nil {
		return errorResult("Error: Invalid arguments for update_plan: " + err.Error())
	}
	plan = enforceSingleInProgress(plan)
	tool.mu.Lock()
	tool.currentPlan = plan
	tool.mu.Unlock()
	return okResult(formatPlan(plan))
}

func (tool *updatePlanTool) CurrentPlan() []PlanItem {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return append([]PlanItem{}, tool.currentPlan...)
}

func (tool *updatePlanTool) ClearPlan() {
	tool.mu.Lock()
	tool.currentPlan = nil
	tool.mu.Unlock()
}

func parsePlanItems(value any) ([]PlanItem, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("plan must be an array")
	}

	plan := make([]PlanItem, 0, len(items))
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("plan item %d must be an object", index+1)
		}

		content, err := stringArg(object, "content", "", true)
		if err != nil {
			return nil, fmt.Errorf("plan item %d %s", index+1, err.Error())
		}
		// id is optional: weaker models can't reliably mint stable ids, and the
		// plan is displayed by 1-based position anyway. Auto-number when omitted.
		id, err := stringArgWithEmpty(object, "id", fmt.Sprintf("%d", index+1), false, true)
		if err != nil {
			return nil, fmt.Errorf("plan item %d %s", index+1, err.Error())
		}
		if id == "" {
			id = fmt.Sprintf("%d", index+1)
		}
		// status is optional and defaults to pending. Non-canonical values from
		// weaker models (e.g. "done", "in-progress", "nope") are COERCED to the
		// nearest canonical status rather than rejected: a rejected call leaves the
		// stored plan unchanged, which freezes the plan panel on its previous state.
		status, err := stringArgWithEmpty(object, "status", "pending", false, true)
		if err != nil {
			return nil, fmt.Errorf("plan item %d %s", index+1, err.Error())
		}
		status = normalizePlanStatus(status)
		notes, err := stringArgWithEmpty(object, "notes", "", false, true)
		if err != nil {
			return nil, fmt.Errorf("plan item %d %s", index+1, err.Error())
		}
		criteria, err := stringArgWithEmpty(object, "criteria", "", false, true)
		if err != nil {
			return nil, fmt.Errorf("plan item %d %s", index+1, err.Error())
		}

		plan = append(plan, PlanItem{
			ID:       id,
			Content:  content,
			Status:   status,
			Notes:    notes,
			Criteria: criteria,
		})
	}
	return plan, nil
}

// normalizePlanStatus coerces a free-form status into one of the four canonical
// values. Unknown/empty input maps to "pending" so a weak model's stray status
// never fails the whole update_plan call (which would freeze the plan panel).
func normalizePlanStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "done", "finished", "resolved", "✓", "x", "[x]":
		return "completed"
	case "in_progress", "in-progress", "inprogress", "in progress", "active",
		"doing", "started", "current", "wip", "ongoing", "running":
		return "in_progress"
	case "failed", "fail", "error", "errored", "blocked", "cancelled", "canceled", "abandoned", "skipped":
		return "failed"
	default: // pending, todo, not_started, queued, "", or anything unrecognized
		return "pending"
	}
}

// enforceSingleInProgress keeps at most one in_progress item: if several are
// marked, only the LAST stays in_progress and the earlier ones are downgraded to
// completed. Mirrors how a single active step should drive the plan panel.
func enforceSingleInProgress(plan []PlanItem) []PlanItem {
	last := -1
	count := 0
	for i, item := range plan {
		if item.Status == "in_progress" {
			count++
			last = i
		}
	}
	if count <= 1 {
		return plan
	}
	for i := range plan {
		if i != last && plan[i].Status == "in_progress" {
			plan[i].Status = "completed"
		}
	}
	return plan
}

func formatPlan(plan []PlanItem) string {
	if len(plan) == 0 {
		return "Plan is currently empty."
	}

	lines := make([]string, 0, len(plan))
	for index, item := range plan {
		line := fmt.Sprintf("%d. [%s] %s", index+1, item.Status, item.Content)
		if item.Criteria != "" {
			line += "\n   Criteria: " + item.Criteria
		}
		if item.Notes != "" {
			line += "\n   Notes: " + item.Notes
		}
		lines = append(lines, line)
	}
	return "Current Plan:\n" + strings.Join(lines, "\n")
}
