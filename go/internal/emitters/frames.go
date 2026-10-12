package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// MCPStartupInput is the input to the process/mcpTelemetry session_startup
// frame (bootstrap/src/nex-protocol/computer-use-operation-event.ts is unrelated;
// the producer is adapters/src/mcp/telemetry.ts recordSessionStartup).
type MCPStartupInput struct {
	Arch          string
	ConfiguredCnt int64
	ConnectedCnt  int64
	FailedCnt     int64
	OccurredAt    int64
	Platform      string
	ProcessCount  int64
	SessionID     string
}

// MCPStartupFrame builds the process/mcpTelemetry session_startup notification.
// Key order matches the TypeScript literal.
func MCPStartupFrame(in MCPStartupInput) protocol.Message {
	o := newObj()
	o.set("arch", v4pub.Str(in.Arch))
	o.set("configuredCount", v4pub.Int(in.ConfiguredCnt))
	o.set("connectedCount", v4pub.Int(in.ConnectedCnt))
	o.set("failedCount", v4pub.Int(in.FailedCnt))
	o.set("kind", v4pub.Str("session_startup"))
	o.set("occurredAt", v4pub.Int(in.OccurredAt))
	o.set("platform", v4pub.Str(in.Platform))
	o.set("processCount", v4pub.Int(in.ProcessCount))
	o.set("sessionId", v4pub.Str(in.SessionID))
	return notification(MethodMCPTelemetry, o.raw())
}

// PrefsScope is the session/requestRuntimePreferences scope.
type PrefsScope string

const (
	// PrefsRuntimeMaterialization is sent before the session runtime is
	// materialized (before process/mcpTelemetry).
	PrefsRuntimeMaterialization PrefsScope = "runtime-materialization"
	// PrefsUserExecution is sent once the runtime is ready to execute the turn.
	PrefsUserExecution PrefsScope = "user-execution"
)

// RuntimePreferencesRequest builds the agent->host
// session/requestRuntimePreferences request. The host replies to the id.
func RuntimePreferencesRequest(id protocol.RequestID, sessionID string, scope PrefsScope) protocol.Message {
	o := newObj()
	o.set("sessionId", v4pub.Str(sessionID))
	o.set("scope", v4pub.Str(string(scope)))
	return protocol.Message{
		Kind:      protocol.KindRequest,
		ID:        id,
		Method:    MethodRequestPrefs,
		Params:    o.raw(),
		HasParams: true,
	}
}

// ── state.updated ──────────────────────────────────────────────────────

// WorkspaceRef mirrors the `workspace` field the state.updated frame carries.
// workspacePath is used for file operations; workspaceKey is the identity key
// (workspaceIdentity trimmed, falling back to the path).
type WorkspaceRef struct {
	WorkspacePath string
	WorkspaceKey  string
}

// ModelRef is a providerId/modelId pair.
type ModelRef struct {
	ProviderID string
	ModelID    string
}

// ModelOption describes one entry of model.available. Options may be nil.
type ModelOption struct {
	Ref            ModelRef
	Label          string
	ProviderLabel  string
	ContextWindow  int64
	MaxOutputToken int64
	// ReasoningLevels are the allowed levels; when empty the reasoning block is
	// omitted entirely (a model without reasoning facts).
	ReasoningLevels []string
	DefaultLevel    string
	HasInputFormat  bool
	SupportsText    bool
	SupportsImage   bool
	SupportsVideo   bool
	SupportsAudio   bool
	SupportsPDF     bool
	// Options is the raw session model options object (runtime selection), e.g.
	// {"reasoningLevel":"none"}. nil omits `options`.
	Options json.RawMessage
}

// ThoughtLevel is one entry of thoughtLevel.available.
type ThoughtLevel struct {
	Label string
	Value string
}

// StatePatch is the `settings` patch carried by the state.updated frame. It is
// built by the session-state mapper; the emitters package only serializes it in
// the exact key order the TypeScript emits.
type StatePatch struct {
	Mode           string
	Available      []ModelOption
	Current        ModelRef
	CurrentOptions json.RawMessage
	LastUsed       *ModelRef
	Permission     string
	ThoughtAvail   []ThoughtLevel
	ThoughtCurrent string
	ThoughtEnabled bool
}

// StateUpdatedFrame builds the state.updated notification. Patch key order
// mirrors mapSessionSettings; the envelope key order mirrors emitStateUpdated.
func StateUpdatedFrame(sessionID string, workspace WorkspaceRef, reason string, revision int64, patch StatePatch) protocol.Message {
	inner := buildSettingsPatch(patch)
	env := newObj()
	env.set("patch", v4pub.RawMessageOf(inner))
	env.set("reason", v4pub.Str(reason))
	env.set("revision", v4pub.Int(revision))
	env.set("scope", v4pub.Str("session"))
	env.set("sessionId", v4pub.Str(sessionID))
	env.set("type", v4pub.Str("state.updated"))
	ws := newObj()
	ws.set("workspacePath", v4pub.Str(workspace.WorkspacePath))
	ws.set("workspaceKey", v4pub.Str(workspace.WorkspaceKey))
	env.set("workspace", v4pub.RawMessageOf(ws.raw()))
	return notification(MethodStateUpdated, env.raw())
}

func buildSettingsPatch(p StatePatch) json.RawMessage {
	o := newObj()
	mode := newObj()
	mode.set("current", v4pub.Str(p.Mode))
	o.set("mode", v4pub.RawMessageOf(mode.raw()))

	model := newObj()
	avail := make([]v4pub.JSONValue, 0, len(p.Available))
	for _, m := range p.Available {
		avail = append(avail, v4pub.RawMessageOf(modelOptionRaw(m)))
	}
	model.set("available", v4pub.Arr(avail...))
	cur := newObj()
	cur.set("providerId", v4pub.Str(p.Current.ProviderID))
	cur.set("modelId", v4pub.Str(p.Current.ModelID))
	if len(p.CurrentOptions) > 0 {
		cur.set("options", v4pub.RawMessageOf(p.CurrentOptions))
	}
	model.set("current", v4pub.RawMessageOf(cur.raw()))
	if p.LastUsed != nil {
		lu := newObj()
		lu.set("providerId", v4pub.Str(p.LastUsed.ProviderID))
		lu.set("modelId", v4pub.Str(p.LastUsed.ModelID))
		model.set("lastUsed", v4pub.RawMessageOf(lu.raw()))
	}
	o.set("model", v4pub.RawMessageOf(model.raw()))

	perm := newObj()
	perm.set("mode", v4pub.Str(p.Permission))
	o.set("permission", v4pub.RawMessageOf(perm.raw()))

	thought := newObj()
	tl := make([]v4pub.JSONValue, 0, len(p.ThoughtAvail))
	for _, l := range p.ThoughtAvail {
		item := newObj()
		item.set("label", v4pub.Str(l.Label))
		item.set("value", v4pub.Str(l.Value))
		tl = append(tl, v4pub.RawMessageOf(item.raw()))
	}
	thought.set("available", v4pub.Arr(tl...))
	thought.set("current", v4pub.Str(p.ThoughtCurrent))
	thought.set("enabled", v4pub.Bool(p.ThoughtEnabled))
	o.set("thoughtLevel", v4pub.RawMessageOf(thought.raw()))
	return o.raw()
}

func modelOptionRaw(m ModelOption) json.RawMessage {
	o := newObj()
	ref := newObj()
	ref.set("providerId", v4pub.Str(m.Ref.ProviderID))
	ref.set("modelId", v4pub.Str(m.Ref.ModelID))
	o.set("ref", v4pub.RawMessageOf(ref.raw()))
	o.set("label", v4pub.Str(m.Label))
	o.set("providerLabel", v4pub.Str(m.ProviderLabel))
	o.set("contextWindow", v4pub.Int(m.ContextWindow))
	o.set("maxOutputTokens", v4pub.Int(m.MaxOutputToken))
	if len(m.ReasoningLevels) > 0 {
		r := newObj()
		levels := make([]v4pub.JSONValue, 0, len(m.ReasoningLevels))
		for _, lvl := range m.ReasoningLevels {
			item := newObj()
			item.set("value", v4pub.Str(lvl))
			item.set("label", v4pub.Str(lvl))
			levels = append(levels, v4pub.RawMessageOf(item.raw()))
		}
		r.set("levels", v4pub.Arr(levels...))
		if m.DefaultLevel != "" {
			r.set("defaultLevel", v4pub.Str(m.DefaultLevel))
		}
		o.set("reasoning", v4pub.RawMessageOf(r.raw()))
	}
	if m.HasInputFormat {
		props := newObj()
		in := newObj()
		in.set("supportsText", v4pub.Bool(m.SupportsText))
		in.set("supportsImage", v4pub.Bool(m.SupportsImage))
		in.set("supportsVideo", v4pub.Bool(m.SupportsVideo))
		in.set("supportsAudio", v4pub.Bool(m.SupportsAudio))
		in.set("supportsPdf", v4pub.Bool(m.SupportsPDF))
		props.set("inputFormat", v4pub.RawMessageOf(in.raw()))
		out := newObj()
		out.set("supportsText", v4pub.Bool(m.SupportsText))
		props.set("outputFormat", v4pub.RawMessageOf(out.raw()))
		o.set("properties", v4pub.RawMessageOf(props.raw()))
	}
	return o.raw()
}
