package contracts

import (
	"fmt"
	"sort"
	"strings"
)

// Refinements are the zod `.refine`/`.superRefine` rules that JSON Schema cannot express
// (they cross fields or inspect values zod has already narrowed). Each is keyed by schema
// id and reproduces the TypeScript rule exactly; go/contracts/refinements_test.go requires
// every id in RefinedSchemas to have an implementation here and vice versa.
//
// Source of truth: the TypeScript schemas listed in each comment. When a rule changes
// there, the corpus (G0.6) fails until this file is updated — that is the point.
var refinements = map[string]func(value any) error{
	"helloMessage":                    refineHelloMessage,
	"nexStorageStartupState":          refineStorageStartupState,
	"pendingInteraction":              refinePendingInteraction,
	"conversationTopicFrame":          refineConversationTopicFrame,
	"conversationTopicWireFrame":      refineConversationTopicWireFrame,
	"commandsQueryParams":             refineCommandsQueryParams,
	"commandPayloadSchemas$$sendText": refineSendText,
	"localTtftDetail":                 refineLocalTtftDetail,
}

func applyRefinement(id string, value any, path string) error {
	fn, ok := refinements[id]
	if !ok {
		return fail(path, "no refinement implemented for schema %q", id)
	}
	if err := fn(value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// RefinementIDs returns the schema ids with a hand-written refinement, sorted.
func RefinementIDs() []string {
	ids := make([]string, 0, len(refinements))
	for id := range refinements {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func asObject(value any) (map[string]any, bool) {
	obj, ok := value.(map[string]any)
	return obj, ok
}

func stringField(obj map[string]any, key string) (string, bool) {
	s, ok := obj[key].(string)
	return s, ok
}

func numberField(obj map[string]any, key string) (float64, bool, bool) {
	v, present := obj[key]
	if !present || v == nil {
		return 0, present, true
	}
	n, ok := v.(float64)
	return n, present, ok
}

// refines/helloMessage: trusted clientMode and deliveryProfile must match.
// packages/shared/src/nex-protocol-v4/transport.ts
func refineHelloMessage(value any) error {
	obj, ok := asObject(value)
	if !ok {
		return nil // structural check already rejected non-objects
	}
	mode, _ := stringField(obj, "clientMode")
	profile, _ := stringField(obj, "deliveryProfile")
	expected := "replayable"
	if mode == "desktop-continuous" {
		expected = "continuous"
	}
	if profile != expected {
		return fmt.Errorf("trusted clientMode and deliveryProfile must match")
	}
	return nil
}

// refines/nexStorageStartupState: phase "failed" requires errorCode.
// packages/shared/src/nex-protocol/index.ts
func refineStorageStartupState(value any) error {
	obj, ok := asObject(value)
	if !ok {
		return nil
	}
	phase, _ := stringField(obj, "phase")
	code, hasCode := stringField(obj, "errorCode")
	if phase == "failed" && (!hasCode || code == "") {
		return fmt.Errorf("failed requires errorCode")
	}
	return nil
}

// refines/pendingInteraction:
//   - interaction.kind must equal payload.kind;
//   - workspaceHookReview cannot carry autoResolution;
//   - workspaceHookReview interactionId must equal payload.interactionId.
//
// packages/shared/src/nex-protocol-v4/snapshot.ts
func refinePendingInteraction(value any) error {
	obj, ok := asObject(value)
	if !ok {
		return nil
	}
	kind, _ := stringField(obj, "kind")
	payload, _ := obj["payload"].(map[string]any)
	if payload != nil {
		payloadKind, _ := stringField(payload, "kind")
		if kind != payloadKind {
			return fmt.Errorf("pending interaction kind must match payload kind")
		}
		if payloadKind == "workspaceHookReview" {
			if id, ok := stringField(obj, "interactionId"); ok {
				if pid, ok := stringField(payload, "interactionId"); ok && id != pid {
					return fmt.Errorf("workspaceHookReview interaction id must match its immutable payload")
				}
			}
		}
	}
	if kind == "workspaceHookReview" {
		if _, has := obj["autoResolution"]; has {
			if obj["autoResolution"] != nil {
				return fmt.Errorf("workspaceHookReview cannot use AskUserQuestion auto-resolution")
			}
		}
	}
	return nil
}

// refines/conversationTopicFrame: topic must start with "conversation/" plus a suffix.
// packages/shared/src/nex-protocol-v4/transport.ts
func refineConversationTopicFrame(value any) error {
	return requireTopicPrefix(value, "conversation/")
}

// refines/conversationTopicWireFrame: the frame's topic (nested) must be a conversation
// topic. The wire frame wraps the logical frame, so check the inner topic too.
// packages/shared/src/nex-protocol-v4/transport.ts
func refineConversationTopicWireFrame(value any) error {
	if err := requireTopicPrefix(value, "conversation/"); err != nil {
		return err
	}
	obj, ok := asObject(value)
	if !ok {
		return nil
	}
	if frame, ok := obj["frame"].(map[string]any); ok {
		return requireTopicPrefix(frame, "conversation/")
	}
	return nil
}

func requireTopicPrefix(value any, prefix string) error {
	obj, ok := asObject(value)
	if !ok {
		return nil
	}
	topic, ok := stringField(obj, "topic")
	if !ok || !strings.HasPrefix(topic, prefix) || len(topic) == len(prefix) {
		return fmt.Errorf("invalid conversation topic")
	}
	return nil
}

// refines/commandsQueryParams: when clock is set, every command must be session-less.
// packages/shared/src/nex-protocol-v4/command.ts
func refineCommandsQueryParams(value any) error {
	obj, ok := asObject(value)
	if !ok {
		return nil
	}
	clock, hasClock := obj["clock"]
	if !hasClock || clock != true {
		return nil
	}
	commands, _ := obj["commands"].([]any)
	for _, command := range commands {
		entry, ok := command.(map[string]any)
		if !ok {
			continue
		}
		if entry["sessionId"] != nil {
			return fmt.Errorf("clock probes cannot query session commands")
		}
	}
	return nil
}

// refines/commandPayloadSchemas$$sendText (superRefine):
//   - automationId and offPeakTaskId are mutually exclusive;
//   - offPeakRunType requires offPeakTaskId;
//   - modelExecution requires modelSelection.
//
// packages/shared/src/nex-protocol-v4/command.ts
func refineSendText(value any) error {
	obj, ok := asObject(value)
	if !ok {
		return nil
	}
	_, hasAutomation := obj["automationId"]
	_, hasOffPeak := obj["offPeakTaskId"]
	if hasAutomation && hasOffPeak {
		return fmt.Errorf("automationId and offPeakTaskId are mutually exclusive")
	}
	if _, hasRunType := obj["offPeakRunType"]; hasRunType && !hasOffPeak {
		return fmt.Errorf("offPeakRunType requires offPeakTaskId")
	}
	if _, hasExecution := obj["modelExecution"]; hasExecution {
		if _, hasSelection := obj["modelSelection"]; !hasSelection {
			return fmt.Errorf("modelExecution requires modelSelection")
		}
	}
	return nil
}

// refines/localTtftDetail: end is absent or end >= start.
// packages/shared/src/localTtft.ts
func refineLocalTtftDetail(value any) error {
	obj, ok := asObject(value)
	if !ok {
		return nil
	}
	end, present, endOK := numberField(obj, "end")
	if !present || !endOK {
		return nil
	}
	start, _, startOK := numberField(obj, "start")
	if !startOK {
		return nil
	}
	if end < start {
		return fmt.Errorf("end must be >= start")
	}
	return nil
}
