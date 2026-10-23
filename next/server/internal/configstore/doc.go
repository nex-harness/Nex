// Package configstore owns the server-side desired configuration: one versioned,
// layered ConfigDoc (providers + models + their secrets for P0), the strict
// pi-shape validation applied at write time, and the AES-256-GCM secret store.
//
// Spec: docs/next/config-mcp-skills.md (decisions 1-4), research §C1-C3.
//
// Ownership: the server owns the desired doc; runners only report facts back
// through `configApplied`. Nothing here ever logs or returns a secret value.
package configstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// SchemaVersion is the only ConfigDoc schema this build understands.
const SchemaVersion = 1

// Scope names. Layering order is global → runner → workspace → session; P0 only
// fills global and runner. Workspace and session keys exist in the schema (and
// are merged) but are never written by the UI.
const (
	ScopeGlobal    = "global"
	ScopeRunner    = "runner"
	ScopeWorkspace = "workspace"
	ScopeSession   = "session"
)

// Provider API types accepted in the doc, stored in pi's model API vocabulary
// (`openai-completions` is pi's id for chat completions). The UI's
// `openai-chat-completions` format is normalized to it at write time.
const (
	APITypeAnthropicMessages     = "anthropic-messages"
	APITypeOpenAIChatCompletions = "openai-completions"
	APITypeOpenAIResponses       = "openai-responses"
	// APITypeOpenAIChatCompletionsEngine is the UI-facing name of the chat
	// completions API (`ProviderApiType`); both spellings are accepted in the
	// doc and stored as APITypeOpenAIChatCompletions.
	APITypeOpenAIChatCompletionsEngine = "openai-chat-completions"
)

// ReasoningLevels is the ordered set of pi thinking levels a model may support.
// The first entry is the lowest public level; every model view needs a non-empty
// subset of it.
var ReasoningLevels = []string{"off", "minimal", "low", "medium", "high"}

// SecretRefPrefix marks a stored secret id in a provider field. A field that
// starts with it is a reference; any other non-empty value is inline.
const (
	// SecretRefPrefix marks a stored secret id.
	SecretRefPrefix = "sec_"
	// SecretIDProviderAPIKey is the secret id shape for one provider's API key.
	secretIDProviderAPIKey = "prov_%s_apikey"
	SecretIDMCP            = "mcp_%s_%s"
)

// ConfigDoc is the persisted desired configuration.
//
// Version is monotonic: every accepted write bumps it by at least one, and
// runners compare it with their last applied version.
type ConfigDoc struct {
	Schema  int    `json:"schema"`
	Version uint64 `json:"version"`
	// UpdatedAt is informational only (milliseconds).
	UpdatedAt int64     `json:"updatedAt,omitempty"`
	Scopes    DocScopes `json:"scopes"`
}

// DocScopes holds the layered patches.
type DocScopes struct {
	Global    Layer            `json:"global"`
	Runner    map[string]Layer `json:"runner,omitempty"`
	Workspace map[string]Layer `json:"workspace,omitempty"`
	Session   map[string]Layer `json:"session,omitempty"`
}

// Layer is one sparse patch. Every map merges by key (null deletes); arrays
// replace.
type Layer struct {
	Providers map[string]*Provider `json:"providers,omitempty"`
	// Settings is a sparse patch of pi settings (defaultProvider/defaultModel/…).
	// A nil value deletes the key.
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	// ProviderOrder is the UI display order of provider ids (replaces).
	ProviderOrder []string `json:"providerOrder,omitempty"`
	// ModelOrder keeps per-provider model order overrides (replaces per key).
	ModelOrder map[string][]string `json:"modelOrder,omitempty"`
}

// Provider is one provider entry. ID, API type and BaseURL are required.
type Provider struct {
	ID      string            `json:"id"`
	API     string            `json:"api"`
	BaseURL string            `json:"baseUrl"`
	Headers map[string]string `json:"headers,omitempty"`
	// APIKey is either an inline literal, a `${ENV}` reference, or a secretRef
	// (`sec_…`). The API-facing projection never returns it.
	APIKey    string `json:"apiKey,omitempty"`
	Name      string `json:"name,omitempty"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"createdAt,omitempty"`
	// Models is an ordered list; it replaces on merge.
	Models []Model `json:"models"`
	// AuthConfigured is runtime-only (push projection): the runner receives a
	// credential for this provider. Never persisted.
	AuthConfigured bool `json:"authConfigured,omitempty"`
}

// Model is one model of a provider in pi's strict shape.
type Model struct {
	ID              string     `json:"id"`
	Name            string     `json:"name,omitempty"`
	Reasoning       bool       `json:"reasoning"`
	ReasoningLevels []string   `json:"reasoningLevels"`
	ContextWindow   int        `json:"contextWindow"`
	MaxTokens       int        `json:"maxTokens"`
	Input           []string   `json:"input,omitempty"`
	Cost            *ModelCost `json:"cost,omitempty"`
	Enabled         bool       `json:"enabled"`
}

// ModelCost mirrors pi's cost object (USD per million tokens).
type ModelCost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

// Empty reports whether the layer carries no patch at all.
func (l Layer) Empty() bool {
	return len(l.Providers) == 0 && len(l.Settings) == 0 &&
		len(l.ProviderOrder) == 0 && len(l.ModelOrder) == 0
}

// EmptyDoc returns a schema-1 doc with empty layers.
func EmptyDoc() ConfigDoc {
	return ConfigDoc{
		Schema: SchemaVersion,
		Scopes: DocScopes{
			Global:    Layer{},
			Runner:    map[string]Layer{},
			Workspace: map[string]Layer{},
			Session:   map[string]Layer{},
		},
	}
}

// RunnerEnabledOverride allows a runner-scope layer to disable a global provider
// without restating it: `{id, enabled:false}` (or enabled:true).
func (p *Provider) isToggleOnly() bool {
	return p.API == "" && p.BaseURL == "" && len(p.Models) == 0 && p.APIKey == ""
}

// ValidateDoc structurally validates a persisted or incoming doc: schema,
// provider ids, API types and, for every model, the strict pi constraints.
//
// It is the single place where the UI's sparse objects become a strict shape
// (research §C1 "enforced at publish").
func ValidateDoc(doc ConfigDoc) []Issue {
	var issues []Issue
	if doc.Schema != SchemaVersion {
		issues = append(issues, Issue{Path: "schema", Code: "schema-version", Message: fmt.Sprintf("unsupported config schema %d", doc.Schema)})
		return issues
	}
	check := func(scopePath string, layer Layer) {
		for id, provider := range layer.Providers {
			path := scopePath + ".providers." + id
			if strings.TrimSpace(id) == "" {
				issues = append(issues, Issue{Path: path, Code: "provider-id-missing", Message: "provider id is empty"})
				continue
			}
			if provider == nil {
				continue
			}
			if provider.isToggleOnly() {
				continue
			}
			issues = append(issues, validateProvider(path, id, provider)...)
		}
		for modelID, order := range layer.ModelOrder {
			seen := make(map[string]struct{}, len(order))
			for _, entry := range order {
				if _, dup := seen[entry]; dup {
					issues = append(issues, Issue{Path: scopePath + ".modelOrder." + modelID, Code: "duplicate-model-id", Message: "model order lists " + entry + " twice"})
				}
				seen[entry] = struct{}{}
			}
		}
	}
	check("scopes.global", doc.Scopes.Global)
	for id, layer := range doc.Scopes.Runner {
		if strings.TrimSpace(id) == "" {
			issues = append(issues, Issue{Path: "scopes.runner", Code: "runner-id-missing", Message: "runner scope id is empty"})
			continue
		}
		check("scopes.runner."+id, layer)
	}
	for key, layer := range doc.Scopes.Workspace {
		check("scopes.workspace."+key, layer)
	}
	for key, layer := range doc.Scopes.Session {
		check("scopes.session."+key, layer)
	}
	return issues
}

func validateProvider(path, id string, provider *Provider) []Issue {
	var issues []Issue
	if provider.ID != "" && provider.ID != id {
		issues = append(issues, Issue{Path: path + ".id", Code: "provider-id-mismatch", Message: "provider id does not match its map key"})
	}
	switch provider.API {
	case APITypeAnthropicMessages, APITypeOpenAIChatCompletions, APITypeOpenAIResponses,
		APITypeOpenAIChatCompletionsEngine:
	case "":
		issues = append(issues, Issue{Path: path + ".api", Code: "api-required", Message: "provider api type is required"})
	default:
		issues = append(issues, Issue{Path: path + ".api", Code: "api-unsupported", Message: "unsupported provider api type " + provider.API})
	}
	if !IsHTTPURL(provider.BaseURL) {
		issues = append(issues, Issue{Path: path + ".baseUrl", Code: "base-url-invalid", Message: "baseUrl must be an http(s) URL"})
	}
	if ref := strings.TrimSpace(provider.APIKey); ref != "" && !strings.HasPrefix(ref, SecretRefPrefix) && !strings.HasPrefix(ref, "${") {
		// A literal key is legal in the doc, but it must look like one (never a
		// secretRef typo such as "sec" without the underscore).
		if strings.HasPrefix(ref, "sec") {
			issues = append(issues, Issue{Path: path + ".apiKey", Code: "secret-ref-invalid", Message: "apiKey looks like a secret reference but is malformed"})
		}
	}
	seen := make(map[string]struct{}, len(provider.Models))
	for index, model := range provider.Models {
		issues = append(issues, validateModel(fmt.Sprintf("%s.models.%d", path, index), model)...)
		if _, dup := seen[model.ID]; dup {
			issues = append(issues, Issue{Path: fmt.Sprintf("%s.models.%d.id", path, index), Code: "duplicate-model-id", Message: "duplicate model id " + model.ID})
		}
		seen[model.ID] = struct{}{}
	}
	return issues
}

func validateModel(path string, model Model) []Issue {
	var issues []Issue
	if strings.TrimSpace(model.ID) == "" {
		issues = append(issues, Issue{Path: path + ".id", Code: "model-id-missing", Message: "model id is required"})
	}
	// The composer requires a non-empty reasoning level list and accepts only
	// values from pi's own set (research A1; docs/next/channels-research.md §4).
	if len(model.ReasoningLevels) == 0 {
		issues = append(issues, Issue{Path: path + ".reasoningLevels", Code: "reasoning-levels-empty", Message: "reasoningLevels must not be empty"})
	}
	seen := make(map[string]struct{}, len(model.ReasoningLevels))
	previous := -1
	for _, level := range model.ReasoningLevels {
		rank := reasoningRank(level)
		if rank < 0 {
			issues = append(issues, Issue{Path: path + ".reasoningLevels", Code: "reasoning-level-unsupported", Message: "unsupported reasoning level " + level})
		} else if rank <= previous {
			// The UI lists levels low → high and derives the default from the
			// first entry; a duplicate or descending list silently breaks it.
			issues = append(issues, Issue{Path: path + ".reasoningLevels", Code: "reasoning-levels-order", Message: "reasoningLevels must be ascending and unique"})
		} else {
			previous = rank
		}
		if _, dup := seen[level]; dup {
			issues = append(issues, Issue{Path: path + ".reasoningLevels", Code: "reasoning-levels-order", Message: "duplicate reasoning level " + level})
		}
		seen[level] = struct{}{}
	}
	if model.ContextWindow <= 0 {
		issues = append(issues, Issue{Path: path + ".contextWindow", Code: "context-window-invalid", Message: "contextWindow must be positive"})
	}
	if model.MaxTokens <= 0 {
		issues = append(issues, Issue{Path: path + ".maxTokens", Code: "max-tokens-invalid", Message: "maxTokens must be positive"})
	}
	for _, modality := range model.Input {
		switch modality {
		case "text", "image", "audio", "video", "pdf":
		default:
			issues = append(issues, Issue{Path: path + ".input", Code: "input-unsupported", Message: "unsupported input modality " + modality})
		}
	}
	return issues
}

func reasoningRank(level string) int {
	for index, known := range ReasoningLevels {
		if known == level {
			return index
		}
	}
	return -1
}

// Issue is one validation or apply error, addressed by JSON path.
type Issue struct {
	Scope   string `json:"scope,omitempty"`
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error implements error so callers can return the first issue as a Go error.
func (i Issue) Error() string {
	if i.Path == "" {
		return i.Code + ": " + i.Message
	}
	return i.Path + ": " + i.Message
}

// IssuesError wraps a list of validation issues.
type IssuesError struct{ Issues []Issue }

// Error implements error.
func (e *IssuesError) Error() string {
	if len(e.Issues) == 0 {
		return "invalid config"
	}
	first := e.Issues[0]
	if len(e.Issues) > 1 {
		return fmt.Sprintf("%s (and %d more)", first.Error(), len(e.Issues)-1)
	}
	return first.Error()
}

// IsHTTPURL is a cheap structural check (the server never fetches these URLs).
func IsHTTPURL(value string) bool {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		return false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(trimmed, "https://"), "http://")
	return rest != "" && !strings.ContainsAny(rest, " \t\r\n")
}

// SortedKeys returns the map keys in stable order (tests + diagnostics).
func SortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ErrNotFound is returned for an unknown provider/model.
var ErrNotFound = errors.New("not found")
