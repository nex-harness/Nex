package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

// scheduleTaskTool lets the Agent create, list, update, delete, or test-run scheduled automations
// from natural language conversations (e.g. via /schedule).
type scheduleTaskTool struct {
	node           *Node
	workspacePath  string
	providerID     string
	modelID        string
	thinkingEffort string
}

func (t *scheduleTaskTool) Name() string     { return "schedule_task" }
func (t *scheduleTaskTool) Category() string { return "system" }
func (t *scheduleTaskTool) IsReadOnly(rawArgs json.RawMessage) bool {
	var args struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(rawArgs, &args)
	return strings.EqualFold(strings.TrimSpace(args.Action), "list")
}

func (t *scheduleTaskTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name: t.Name(),
		Description: "Create, list, update, delete, or immediately test-run scheduled automation tasks (cron jobs) for the user. " +
			"Translate natural language schedules (e.g. 'every day at 9am' -> '0 9 * * *', 'every 3 days at 20:00' -> '0 20 */3 * *', 'weekdays at 18:30' -> '30 18 * * 1-5') into standard 5-field cron expressions. " +
			"Defaults to the current runner, workspace, and model unless overridden.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["create", "list", "update", "delete", "run_now"],
					"description": "Operation to perform."
				},
				"job_id": {
					"type": "string",
					"description": "Target automation job ID (required for update, delete, run_now)."
				},
				"title": {
					"type": "string",
					"description": "Short human-readable title for the automation."
				},
				"cron_expr": {
					"type": "string",
					"description": "Standard 5-field cron expression (minute hour day-of-month month day-of-week), e.g. '0 9 * * *'."
				},
				"prompt": {
					"type": "string",
					"description": "Full autonomous prompt executed when the schedule triggers."
				},
				"enabled": {
					"type": "boolean",
					"description": "Whether the automation is enabled (for update)."
				}
			},
			"required": ["action"]
		}`),
	}
}

func (t *scheduleTaskTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ tools.UpdateFunc) (tools.Result, error) {
	var args struct {
		Action   string `json:"action"`
		JobID    string `json:"job_id"`
		Title    string `json:"title"`
		CronExpr string `json:"cron_expr"`
		Prompt   string `json:"prompt"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return tools.Result{Output: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	enabled := true
	if args.Enabled != nil {
		enabled = *args.Enabled
	}
	payload := protocol.CronManagePayload{
		Action: strings.ToLower(strings.TrimSpace(args.Action)),
		JobID:  strings.TrimSpace(args.JobID),
		Job: store.CronJobRecord{
			ID:             strings.TrimSpace(args.JobID),
			RunnerID:       t.node.cfg.RunnerID,
			Title:          strings.TrimSpace(args.Title),
			CronExpr:       strings.TrimSpace(args.CronExpr),
			Prompt:         strings.TrimSpace(args.Prompt),
			WorkspacePath:  t.workspacePath,
			ProviderID:     t.providerID,
			ModelID:        t.modelID,
			ThinkingEffort: t.thinkingEffort,
			PermissionMode: "autonomous",
			Recurring:      true,
			Enabled:        enabled,
		},
	}

	env, err := protocol.NewEnvelope(protocol.TypeCronManage, payload)
	if err != nil {
		return tools.Result{Output: fmt.Sprintf("encode request failed: %v", err), IsError: true}, nil
	}
	reply, err := t.node.CallServerRPC(ctx, env, 15*time.Second)
	if err != nil {
		return tools.Result{Output: fmt.Sprintf("schedule_task failed: %v", err), IsError: true}, nil
	}
	if reply.Type == protocol.TypeError {
		var errObj struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(reply.Payload, &errObj)
		return tools.Result{Output: fmt.Sprintf("schedule_task error: %s", errObj.Error), IsError: true}, nil
	}
	return tools.Result{Output: string(reply.Payload)}, nil
}
