package local

// Argument decoding, view-target parsing and runner-fact helpers of the
// server-owned provider-settings/model-selection channels (split out of
// providersettings.go for the 400-line file budget).

import (
	"encoding/json"
	"strings"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

func (p *ProviderSettings) runnerFor(target viewTarget) string {
	if target.RunnerID != "" {
		return target.RunnerID
	}
	if runnerID, _, ok := parseRunnerIdentity(target.WorkspaceIdentity); ok {
		return runnerID
	}
	if first, ok := p.manager.reg.FirstRunnerID(); ok {
		return first
	}
	return ""
}

// view builds the ProviderSettingsView.
// runnerProviderFacts is the per-provider slice of a runner's configApplied facts.
type runnerProviderFacts struct {
	Configured bool
	Error      string
	Models     map[string]runnerModelFact
}

type runnerModelFact struct {
	ProviderID      string   `json:"providerId"`
	ModelID         string   `json:"modelId"`
	Name            string   `json:"name"`
	API             string   `json:"api"`
	BaseURL         string   `json:"baseUrl"`
	ReasoningLevels []string `json:"reasoningLevels"`
	ContextWindow   int      `json:"contextWindow"`
	MaxTokens       int      `json:"maxTokens"`
	Input           []string `json:"input"`
}

// providerAuthState is the credential state of one provider: configured with a
// usable key (apiKey + hint), or not yet provisioned (and why).
type providerAuthState struct {
	configured bool
	hint       string
	problem    string
}

// provideAuthStateFor decides a provider's credential state from the doc, the
// secret store and (when the runner reported any) its own auth status. The
// secret store is authoritative: a key present on the server is sent to every
// entitled runner, so the row is configured even before the first ack.
func provideAuthStateFor(secrets *configstore.SecretBox, provider *configstore.Provider, facts map[string]runnerProviderFacts, haveFacts bool) providerAuthState {
	hasSecret, hint := secretHint(secrets, provider)
	inlineRef := strings.TrimSpace(provider.APIKey)
	switch {
	case hasSecret:
		return providerAuthState{configured: true, hint: hint}
	case strings.HasPrefix(inlineRef, "${"):
		return providerAuthState{configured: true, hint: inlineRef}
	case inlineRef != "":
		return providerAuthState{configured: true}
	}
	if haveFacts {
		if fact, ok := facts[provider.ID]; ok {
			if fact.Configured {
				return providerAuthState{configured: true}
			}
			if fact.Error != "" {
				return providerAuthState{problem: fact.Error}
			}
		}
	}
	return providerAuthState{problem: "secret-not-provisioned"}
}

func runnerModelFacts(manager *Manager, runnerID string) (map[string]runnerProviderFacts, string) {
	out := map[string]runnerProviderFacts{}
	if runnerID == "" {
		return out, ""
	}
	state, ok := manager.Applied(runnerID)
	if !ok || state.Facts == nil {
		return out, ""
	}
	raw, ok := state.Facts["modelsByProvider"]
	if !ok {
		return out, ""
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return out, ""
	}
	var facts map[string]struct {
		Configured bool              `json:"configured"`
		Error      string            `json:"error"`
		Models     []runnerModelFact `json:"models"`
	}
	if err := json.Unmarshal(encoded, &facts); err != nil {
		return out, ""
	}
	for providerID, fact := range facts {
		entry := runnerProviderFacts{Configured: fact.Configured, Error: fact.Error, Models: map[string]runnerModelFact{}}
		for _, model := range fact.Models {
			entry.Models[model.ModelID] = model
		}
		out[providerID] = entry
	}
	return out, runnerID
}

func readViewTarget(arg rpcwire.Value) viewTarget {
	return readViewTargetFromReason(arg)
}

func readViewTargetFromReason(arg rpcwire.Value) viewTarget {
	return readViewTargetFromArgs(positionalArguments(arg))
}

func readViewTargetFromArgs(args []rpcwire.Value) viewTarget {
	target := viewTarget{}
	for _, entry := range args {
		fields := decodeObjectMap(entry)
		if len(fields) == 0 {
			continue
		}
		if value := jsonString(fields["runnerId"]); value != "" {
			target.RunnerID = value
		}
		if value := jsonString(fields["workspaceIdentity"]); value != "" {
			target.WorkspaceIdentity = value
		}
		if value := jsonString(fields["workspacePath"]); value != "" && target.WorkspacePath == "" {
			target.WorkspacePath = value
		}
	}
	return target
}

// ── argument helpers ──

// positionalArguments splits the browser's `[a, b, c]` call envelope.
//
// The browser sends the positional array inside the wire's JSON-fallback value,
// either directly or as `{"$esc": "[...]"}` when the serializer escaped it; both
// forms are unwrapped to the raw array text before the split.
func positionalArguments(arg rpcwire.Value) []rpcwire.Value {
	switch typed := arg.(type) {
	case []any:
		out := make([]rpcwire.Value, len(typed))
		for index, element := range typed {
			out[index] = unwrapEscaped(element)
		}
		return out
	case json.RawMessage, []byte:
		raw := rawBytes(unwrapEscaped(typed))
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err == nil && strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
			out := make([]rpcwire.Value, len(elements))
			for index, element := range elements {
				out[index] = unwrapEscaped(element)
			}
			return out
		}
		return []rpcwire.Value{raw}
	}
	return []rpcwire.Value{arg}
}

// unwrapEscaped turns one list element's `{"$esc": <json>}` sentinel back into
// its raw JSON text (the wire has one escape level used by the Node serializer).
func unwrapEscaped(value rpcwire.Value) rpcwire.Value {
	// rpcwire falls back to the plain `[]byte` type (Go aliases json.RawMessage
	// to it, so a type switch on json.RawMessage never matches).
	var raw []byte
	switch typed := value.(type) {
	case json.RawMessage:
		raw = typed
	case []byte:
		raw = typed
	default:
		return value
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil || len(probe) != 1 {
		return value
	}
	escaped, ok := probe["$esc"]
	if !ok {
		return value
	}
	// `$esc` carries the already-encoded JSON text, not a JSON string.
	if !json.Valid(escaped) {
		return value
	}
	return json.RawMessage(append([]byte(nil), escaped...))
}

// unquoteElements fixes the nested `map[string]json.RawMessage` trap: the
// outer decode stores every value at its own nesting level (an object becomes
// object text, a string keeps its quotes), so a second decode to the raw value
// is needed before the value can be used as JSON again.
func unquoteElements(values map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		var inner json.RawMessage
		if err := json.Unmarshal(value, &inner); err != nil || len(inner) == 0 {
			out[key] = value
			continue
		}
		out[key] = inner
	}
	return out
}

// argumentString reads a JSON string (or an already-decoded Go string).
func argumentString(value rpcwire.Value) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	raw := rawBytes(value)
	if raw == nil {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	return ""
}

// rawBytes returns a JSON-fallback value as bytes. The wire decoder yields the
// plain `[]byte` type, and Go aliases json.RawMessage to it, so a type switch
// must test []byte explicitly.
func rawBytes(value any) []byte {
	switch typed := value.(type) {
	case []byte:
		return typed
	case json.RawMessage:
		return typed
	}
	return nil
}

func argumentStringList(value rpcwire.Value) []string {
	raw := rawBytes(value)
	if raw == nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		raw = encoded
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func decodeObjectMap(value any) map[string]json.RawMessage {
	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case json.RawMessage:
		raw = typed
	case map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return map[string]json.RawMessage{}
		}
		raw = encoded
	case nil:
		return map[string]json.RawMessage{}
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return map[string]json.RawMessage{}
		}
		raw = encoded
	}
	// Values are decoded one level at a time: a nested map element still holds
	// its own JSON text (jsontext.Value round trips only once), so an element is
	// reinterpreted as raw JSON before it is handed back to callers.
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]json.RawMessage{}
	}
	for key, value := range out {
		var inner json.RawMessage
		if json.Unmarshal(value, &inner) == nil {
			out[key] = inner
		}
	}
	return out
}

func objectFieldRaw(value rpcwire.Value, key string) rpcwire.Value {
	fields := decodeObjectMap(value)
	if raw, ok := fields[key]; ok {
		return rpcwire.Value(raw)
	}
	return nil
}

func intField(fields map[string]json.RawMessage, key string) int {
	raw, ok := fields[key]
	if !ok {
		return 0
	}
	var value int
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var floatValue float64
	if json.Unmarshal(raw, &floatValue) == nil {
		return int(floatValue)
	}
	return 0
}

func boolField(fields map[string]json.RawMessage, key string) bool {
	raw, ok := fields[key]
	if !ok {
		return false
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

func stringListField(fields map[string]json.RawMessage, key string) []string {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	return list
}

func isUndefined(value rpcwire.Value) bool {
	switch value.(type) {
	case nil, rpcwire.Undefined:
		return true
	}
	return false
}

func isNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// jsonText returns the JSON text of a raw element. A nested-marshal element is
// double-quoted (a string keeps its quotes), so every element is decoded once
// and re-read as raw JSON; values that do not decode are used as-is.
func jsonText(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var inner json.RawMessage
	if err := json.Unmarshal(raw, &inner); err == nil && len(inner) > 0 {
		return inner
	}
	return raw
}

// jsonString decodes a JSON string element (tolerating the double-encoded form).
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	if err := json.Unmarshal(jsonText(raw), &text); err == nil {
		return text
	}
	return ""
}
