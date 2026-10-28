package local

// Runner dispatcher for server-owned automations
// (docs/next/automations-views-search.md §A, internal/automations/doc.go).
//
// One dispatch = one `nex-agent.sendConversationCommandV4` forwarded to the
// runner that owns the automation's workspace. The envelope's commandId is the
// run id, so a retried dispatch (60 s retry, or a server restart re-sending a
// claim) is deduplicated by the runner (packages/next-runner/src/v4/commands.ts
// handleCommand). The ack's `result.sessionId` is recorded on the run.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nex-harness/nex/server/internal/automations"
	"github.com/nex-harness/nex/server/internal/runners"
)

// automationsCommandEnvelope is the v4 CommandEnvelope subset a dispatch
// builds (packages/shared/src/nex-protocol-v4/command.ts CommandEnvelope).
// issuedAt is the required client clock hint the schema still carries though
// the runner never uses it for decisions.
type automationsCommandEnvelope struct {
	CommandID string          `json:"commandId"`
	ClientID  string          `json:"clientId"`
	SessionID *string         `json:"sessionId"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	IssuedAt  int64           `json:"issuedAt"`
}

// automationsCommandAck is the v4 CommandAck subset read back from the runner
// (command.ts commandAckSchema).
type automationsCommandAck struct {
	Status     string `json:"status"`
	ReasonCode string `json:"reasonCode"`
	Message    string `json:"message"`
	Result     *struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionId"`
	} `json:"result"`
}

// automationsDispatchClientID is the envelope clientId for server-dispatched
// commands. It never resolves an interaction (automation sessions deny
// approvals on the runner side), so a fixed value is accurate.
const automationsDispatchClientID = "nex-next-server"

// RunnerAutomationsDispatcher delivers automation runs through the runner
// registry. It implements automations.Dispatcher.
type RunnerAutomationsDispatcher struct {
	registry *runners.Registry
	// now supplies issuedAt. Tests replace it.
	now func() int64
}

// NewRunnerAutomationsDispatcher returns a dispatcher bound to registry.
func NewRunnerAutomationsDispatcher(registry *runners.Registry) *RunnerAutomationsDispatcher {
	return &RunnerAutomationsDispatcher{registry: registry, now: nowMillis}
}

// Dispatch implements automations.Dispatcher.
func (d *RunnerAutomationsDispatcher) Dispatch(ctx context.Context, target automations.Target, command automations.Command) (string, error) {
	if d.registry == nil {
		return "", errors.New("runner offline: no registry")
	}
	envelope, err := buildAutomationsEnvelope(command, d.now())
	if err != nil {
		return "", err
	}
	// The runner table entry expects the positional `[arg]` array every
	// ProxyChannel call uses; serve() forwards it verbatim.
	arg, err := json.Marshal([]any{map[string]any{
		"workspacePath": target.WorkspacePath,
		"envelope":      envelope,
	}})
	if err != nil {
		return "", err
	}
	value, err := d.registry.CallWorkspace(ctx, runners.Target{
		WorkspacePath:     target.WorkspacePath,
		WorkspaceIdentity: target.WorkspaceIdentity,
	}, "nex-agent", "sendConversationCommandV4", arg)
	if err != nil {
		return "", err
	}
	return sessionIDFromAutomationsAck(value)
}

// buildAutomationsEnvelope maps one automations.Command onto the runner's
// CommandEnvelope JSON (see automations/doc.go "Dispatcher").
//
// modelSelection/mode travel inside the payload form the runner reads for
// that command type: firstInput for createSession, the sendText payload for a
// bound session (TargetTaskID). automationId rides along in both forms: the
// runner marks the session unattended (approval requests deny immediately)
// and tags task rows with the cron badge (cronAutomationId).
func buildAutomationsEnvelope(command automations.Command, issuedAt int64) (automationsCommandEnvelope, error) {
	if strings.TrimSpace(command.CommandID) == "" {
		return automationsCommandEnvelope{}, errors.New("automation command requires a commandId")
	}
	if !command.CreateSession && strings.TrimSpace(command.SessionID) == "" {
		return automationsCommandEnvelope{}, errors.New("automation sendText requires a sessionId")
	}
	envelope := automationsCommandEnvelope{
		CommandID: command.CommandID,
		ClientID:  automationsDispatchClientID,
		Type:      "createSession",
		IssuedAt:  issuedAt,
	}
	if !command.CreateSession {
		envelope.Type = "sendText"
		sessionID := command.SessionID
		envelope.SessionID = &sessionID
	}
	payload := map[string]any{}
	if len(command.ModelSelection) > 0 {
		var selection any
		if err := json.Unmarshal(command.ModelSelection, &selection); err != nil {
			return automationsCommandEnvelope{}, fmt.Errorf("decoding modelSelection: %w", err)
		}
		payload["modelSelection"] = selection
	}
	if command.Mode != nil {
		payload["mode"] = *command.Mode
	}
	if id := strings.TrimSpace(command.AutomationID); id != "" {
		payload["automationId"] = id
	}
	if command.CreateSession {
		firstInput := map[string]any{"text": command.Text}
		for key, value := range payload {
			firstInput[key] = value
		}
		payload = map[string]any{"firstInput": firstInput}
	} else {
		payload["text"] = command.Text
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return automationsCommandEnvelope{}, err
	}
	envelope.Payload = encoded
	return envelope, nil
}

// sessionIDFromAutomationsAck reads `result.sessionId`. A rejected ack, or an
// accepted ack without a session (not a createSession), is a dispatch failure:
// the run must not be recorded as dispatched onto a session that cannot show
// its outcome.
func sessionIDFromAutomationsAck(value json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return "", errors.New("runner returned no command ack")
	}
	var ack automationsCommandAck
	if err := json.Unmarshal([]byte(trimmed), &ack); err != nil {
		return "", fmt.Errorf("decoding command ack: %w", err)
	}
	sessionID := ""
	if ack.Result != nil {
		sessionID = strings.TrimSpace(ack.Result.SessionID)
	}
	if ack.Status == "accepted" || ack.Status == "duplicate" {
		if sessionID == "" {
			return "", errors.New("runner ack has no sessionId")
		}
		return sessionID, nil
	}
	reason := strings.TrimSpace(ack.ReasonCode)
	if reason == "" {
		reason = "unknown"
	}
	message := strings.TrimSpace(ack.Message)
	if message == "" {
		message = "command rejected"
	}
	return "", fmt.Errorf("runner rejected automation command (%s): %s", reason, message)
}

// nowMillis is the default issuedAt clock.
func nowMillis() int64 { return time.Now().UnixMilli() }
