package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (s *Service) call(ctx context.Context, method string, arg json.RawMessage) (json.RawMessage, error) {
	payload, err := decodePayload(arg)
	if err != nil {
		return nil, err
	}
	now := s.now()
	switch method {
	case "listAllAutomations":
		return encodeJSON(s.listAutomations(workspaceScope{}))
	case "listAutomations":
		scope := scopeFrom(stringField(payload, "workspacePath"), stringField(payload, "workspaceIdentity"))
		return encodeJSON(s.listAutomations(scope))
	case "createAutomation":
		var params CreateParams
		if err := decodeParams(payload, &params); err != nil {
			return nil, fmt.Errorf("decoding createAutomation: %w", err)
		}
		created, err := s.createAutomation(params, now)
		if err != nil {
			return nil, err
		}
		return encodeJSON(created)
	case "updateAutomation":
		var params UpdateParams
		if err := decodeParams(payload, &params); err != nil {
			return nil, fmt.Errorf("decoding updateAutomation: %w", err)
		}
		id := stringField(payload, "automationId")
		scope := scopeFrom(stringField(payload, "workspacePath"), stringField(payload, "workspaceIdentity"))
		updated, err := s.updateAutomation(id, params, scope, now)
		if err != nil {
			return nil, err
		}
		return encodeJSON(updated)
	case "deleteAutomation":
		id := stringField(payload, "automationId")
		scope := scopeFrom(stringField(payload, "workspacePath"), stringField(payload, "workspaceIdentity"))
		if _, err := s.deleteAutomation(id, scope); err != nil {
			return nil, err
		}
		return json.RawMessage("null"), nil
	case "setAutomationEnabled":
		id := stringField(payload, "automationId")
		var enabled bool
		if raw, ok := payload["enabled"]; ok {
			if err := json.Unmarshal(raw, &enabled); err != nil {
				return nil, errors.New("invalid enabled")
			}
		}
		scope := scopeFrom(stringField(payload, "workspacePath"), stringField(payload, "workspaceIdentity"))
		if err := s.setEnabled(id, enabled, scope, now); err != nil {
			return nil, err
		}
		return json.RawMessage("null"), nil
	case "restartAutomation":
		id := stringField(payload, "automationId")
		scope := scopeFrom(stringField(payload, "workspacePath"), stringField(payload, "workspaceIdentity"))
		if err := s.restart(id, scope, now); err != nil {
			return nil, err
		}
		return json.RawMessage("null"), nil
	case "runAutomationNow":
		id := stringField(payload, "automationId")
		scope := scopeFrom(stringField(payload, "workspacePath"), stringField(payload, "workspaceIdentity"))
		automation, run, err := s.runNow(id, scope, now)
		if errors.Is(err, ErrDuplicateRun) {
			return json.RawMessage(`{"status":"duplicate"}`), nil
		}
		if err != nil {
			return nil, err
		}
		// Dispatch asynchronously: the response only reports the claim.
		// 修复：不能用请求 ctx——RPC 一返回它就被取消，派发必然 "context canceled"。
		// 与定时派发一致，使用服务生命周期 ctx。
		go s.dispatchManual(s.lifetimeContext(), automation, run)
		return json.RawMessage(`{"status":"queued"}`), nil
	case "listAutomationRuns":
		id := stringField(payload, "automationId")
		scope := scopeFrom(stringField(payload, "workspacePath"), stringField(payload, "workspaceIdentity"))
		return encodeJSON(s.listRuns(id, scope))
	case "deleteAutomationRun":
		id := stringField(payload, "runId")
		scope := scopeFrom(stringField(payload, "workspacePath"), stringField(payload, "workspaceIdentity"))
		if err := s.deleteRun(id, scope); err != nil {
			return nil, err
		}
		return json.RawMessage("null"), nil
	}
	return nil, fmt.Errorf("Method not found: %s", method)
}

// decodeParams decodes the unwrapped argument object into a params struct.
func decodeParams(payload map[string]json.RawMessage, target any) error {
	encoded, err := json.Marshal(stripUndefined(payload))
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

// stripUndefined removes {"$undef":true} values before a typed decode. The
// browser bridges JavaScript `undefined` to that marker, and a payload built
// with spread (e.g. `{...base, maxRuns: maybe}`) carries it for any optional
// field: decoding it into a *float64/*string fails, and into json.RawMessage
// would turn "absent" into "present". Removing the key keeps both cases on
// the "field was not submitted" path, which is exactly the old contract.
func stripUndefined(payload map[string]json.RawMessage) map[string]json.RawMessage {
	cleaned := make(map[string]json.RawMessage, len(payload))
	for key, raw := range payload {
		if isUndefined(raw) {
			continue
		}
		if isNull(raw) {
			cleaned[key] = raw
			continue
		}
		if len(raw) > 0 && raw[0] == '{' {
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(raw, &nested); err == nil {
				cleaned[key] = mustEncode(stripUndefined(nested))
				continue
			}
		}
		cleaned[key] = raw
	}
	return cleaned
}

func mustEncode(value map[string]json.RawMessage) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func encodeJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func stringField(payload map[string]json.RawMessage, key string) string {
	raw, ok := payload[key]
	if !ok || isUndefined(raw) || isNull(raw) {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// decodePayload unwraps the browser's positional argument array `[arg]`.
// `{"$undef":true}` (JavaScript undefined) becomes an empty object; a missing
// argument does too.
func decodePayload(arg json.RawMessage) (map[string]json.RawMessage, error) {
	raw := strings.TrimSpace(string(arg))
	if raw == "" {
		return map[string]json.RawMessage{}, nil
	}
	if strings.HasPrefix(raw, "[") {
		var elements []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &elements); err != nil {
			return nil, fmt.Errorf("decoding arguments: %w", err)
		}
		if len(elements) == 0 {
			return map[string]json.RawMessage{}, nil
		}
		raw = strings.TrimSpace(string(elements[0]))
	}
	if raw == "" || raw == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("decoding argument object: %w", err)
	}
	if payload == nil {
		payload = map[string]json.RawMessage{}
	}
	return payload, nil
}
