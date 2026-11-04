package specialist

import (
	"context"
	"fmt"
	"strings"

	"github.com/aistatine/sonut/internal/background"
	"github.com/aistatine/sonut/internal/tools"
)

type StopTool struct {
	manager     *background.Manager
	managerFunc BackgroundManagerFunc
}

type stopParameters struct {
	TaskID string
}

func NewStopTool(manager *background.Manager) *StopTool {
	return &StopTool{manager: manager}
}

func newStopToolWithManagerFunc(managerFunc BackgroundManagerFunc) *StopTool {
	return &StopTool{managerFunc: managerFunc}
}

func (tool *StopTool) Name() string {
	return "TaskStop"
}

func (tool *StopTool) Description() string {
	return "Stop a running background @code specialist task."
}

func (tool *StopTool) Parameters() tools.Schema {
	return tools.Schema{
		Type: "object",
		Properties: map[string]tools.PropertySchema{
			"task_id": {
				Type:        "string",
				Description: "Background task id returned by Task.",
			},
		},
		Required:             []string{"task_id"},
		AdditionalProperties: false,
	}
}

func (tool *StopTool) Safety() tools.Safety {
	return tools.Safety{
		SideEffect:      tools.SideEffectShell,
		Permission:      tools.PermissionPrompt,
		Reason:          "Terminates a background specialist process.",
		AdvertiseInAuto: true,
	}
}

func (tool *StopTool) Run(ctx context.Context, args map[string]any) tools.Result {
	params, err := parseStopParameters(args)
	if err != nil {
		return taskError(err)
	}
	manager, err := tool.backgroundManager()
	if err != nil {
		return taskError(err)
	}
	if err := manager.Kill(params.TaskID); err != nil {
		// A bare number is almost always an exec_command session id, not a
		// background task id — steer the model to the right tool instead of
		// letting it burn turns retrying TaskStop against every session id.
		if isAllDigits(params.TaskID) && strings.Contains(err.Error(), "not found") {
			return taskError(fmt.Errorf("background task not found: %s. That looks like an exec_command session id, not a Task id — running exec sessions are stopped with write_stdin (session_id %s, chars \"\\u0003\"), not TaskStop. Do NOT retry TaskStop with other session ids", params.TaskID, params.TaskID))
		}
		return taskError(err)
	}
	return tools.Result{
		Status: tools.StatusOK,
		Output: fmt.Sprintf("task_id: %s\nstatus: killed", params.TaskID),
		Meta: map[string]string{
			"task_id": params.TaskID,
			"status":  string(background.StatusKilled),
		},
	}
}

func (tool *StopTool) backgroundManager() (*background.Manager, error) {
	if tool.manager != nil {
		return tool.manager, nil
	}
	if tool.managerFunc != nil {
		return tool.managerFunc()
	}
	return nil, fmt.Errorf("background manager is not configured")
}

func parseStopParameters(args map[string]any) (stopParameters, error) {
	taskID, err := optionalTaskString(args, "task_id")
	if err != nil {
		return stopParameters{}, err
	}
	params := stopParameters{TaskID: strings.TrimSpace(taskID)}
	if params.TaskID == "" {
		return stopParameters{}, fmt.Errorf("task stop requires task_id")
	}
	return params, nil
}

// isAllDigits reports whether s is a non-empty run of ASCII digits — the
// shape of an exec_command session id, never of a background task id.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
