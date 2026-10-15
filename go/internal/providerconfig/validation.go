// validation.go ports the zod "complete" schema checks the resolver uses
// (schema-validation.ts + the complete*Schema definitions): a config is
// registry-ready when every required leaf is present and every enum/URL leaf
// is in range. The issue codes are part of the UI contract
// (ConfigValidationIssue.code).
package providerconfig

import (
	"net/url"
	"strings"
	"sync"
)

// ValidationIssue ports ConfigValidationIssue.
type ValidationIssue struct {
	Code    string   `json:"code"`
	Path    []string `json:"path"`
	Message string   `json:"message"`
}

// Issue codes (config-overlay.ts).
const (
	IssueRequiredMissing   = "required-field-missing"
	IssueDuplicateKey      = "duplicate-key"
	IssueDuplicateModel    = "duplicate-model"
	IssueInvalidOptionSpec = "invalid-option-spec"
	IssueInvalidConfig     = "invalid-config"
	IssueInvalidURL        = "invalid-url"
	IssueMissingTemplate   = "missing-template"
)

// validateProviderComplete ports createRegistryProviderConfig →
// validateComplete against completeProviderConfigDataSchema: group, access
// (complete), api (type + URL), and — when a provider reaches the registry —
// every model must validate too (models are checked separately).
func validateProviderComplete(config *ProviderConfig, providerID string) []ValidationIssue {
	path := []string{"providers", providerID}
	if config == nil {
		return []ValidationIssue{{Code: IssueRequiredMissing, Path: path, Message: "缺少必填配置 " + joinPath(path)}}
	}
	var issues []ValidationIssue
	if config.Group == nil || *config.Group == "" {
		issues = append(issues, requiredIssue(append(path, "group")))
	}
	// access must be complete when present: api-key requires a non-blank key;
	// zhipu-account requires accountType/mode/entitled.
	if config.Access != nil {
		accessPath := append(path, "access")
		switch {
		case config.Access.APIKey != nil:
			key := config.Access.APIKey.APIKey
			if key == nil || strings.TrimSpace(*key) == "" {
				issues = append(issues, requiredIssue(append(accessPath, "apiKey")))
			}
		case config.Access.ZhipuAccount != nil:
			if config.Access.ZhipuAccount.AccountType == nil || *config.Access.ZhipuAccount.AccountType == "" {
				issues = append(issues, requiredIssue(append(accessPath, "accountType")))
			}
			if config.Access.ZhipuAccount.Mode == nil || *config.Access.ZhipuAccount.Mode == "" {
				issues = append(issues, requiredIssue(append(accessPath, "mode")))
			}
			if config.Access.ZhipuAccount.Entitled == nil {
				issues = append(issues, requiredIssue(append(accessPath, "entitled")))
			}
		default:
			issues = append(issues, ValidationIssue{Code: IssueInvalidConfig, Path: accessPath, Message: "未知 access type"})
		}
	} else {
		issues = append(issues, requiredIssue(append(path, "access")))
	}
	// api must be complete: type + valid URL.
	if config.API != nil {
		apiPath := append(path, "api")
		if config.API.Type == nil || *config.API.Type == "" {
			issues = append(issues, requiredIssue(append(apiPath, "type")))
		}
		if config.API.BaseURL == nil || *config.API.BaseURL == "" {
			issues = append(issues, requiredIssue(append(apiPath, "baseUrl")))
		} else if !validURL(*config.API.BaseURL) {
			issues = append(issues, ValidationIssue{
				Code: IssueInvalidURL, Path: append(apiPath, "baseUrl"),
				Message: "配置 " + joinPath(append(apiPath, "baseUrl")) + " 必须是有效 URL",
			})
		}
	} else {
		issues = append(issues, requiredIssue(append(path, "api")))
	}
	return issues
}

// validateModelComplete ports createRegistryModelConfig →
// completeModelConfigDataSchema: enabled, properties (complete) and
// optionSpecs (complete, both specs).
func validateModelComplete(config *ModelConfig, path []string) []ValidationIssue {
	if config == nil {
		return []ValidationIssue{{Code: IssueRequiredMissing, Path: path, Message: "缺少必填配置 " + joinPath(path)}}
	}
	var issues []ValidationIssue
	if config.Enabled == nil {
		issues = append(issues, requiredIssue(append(path, "enabled")))
	}
	propsPath := append(path, "properties")
	if config.Properties == nil {
		issues = append(issues, requiredIssue(propsPath))
	} else {
		p := config.Properties
		if p.RequiresMfjsToolSchema == nil {
			issues = append(issues, requiredIssue(append(propsPath, "requiresMfjsToolSchema")))
		}
		if p.ContextWindow == nil || *p.ContextWindow <= 0 {
			issues = append(issues, requiredIssue(append(propsPath, "contextWindow")))
		}
		if p.InputFormat == nil {
			issues = append(issues, requiredIssue(append(propsPath, "inputFormat")))
		} else {
			inPath := append(propsPath, "inputFormat")
			for _, leaf := range []struct {
				name  string
				value *bool
			}{
				{"supportsText", p.InputFormat.SupportsText},
				{"supportsImage", p.InputFormat.SupportsImage},
				{"supportsVideo", p.InputFormat.SupportsVideo},
				{"supportsAudio", p.InputFormat.SupportsAudio},
				{"supportsPdf", p.InputFormat.SupportsPdf},
			} {
				if leaf.value == nil {
					issues = append(issues, requiredIssue(append(inPath, leaf.name)))
				}
			}
		}
		if p.OutputFormat == nil || p.OutputFormat.SupportsText == nil {
			issues = append(issues, requiredIssue(append(propsPath, "outputFormat")))
		}
		for _, leaf := range []struct {
			name  string
			value *bool
		}{
			{"supportsToolCall", p.SupportsToolCall},
			{"supportsJsonSchemaOutput", p.SupportsJsonSchemaOutput},
			{"supportsNativeWebSearch", p.SupportsNativeWebSearch},
			{"supportsMidConversationSystem", p.SupportsMidConversationSystem},
		} {
			if leaf.value == nil {
				issues = append(issues, requiredIssue(append(propsPath, leaf.name)))
			}
		}
	}
	specsPath := append(path, "optionSpecs")
	if config.OptionSpecs == nil {
		issues = append(issues, requiredIssue(specsPath))
	} else {
		s := config.OptionSpecs
		if s.ReasoningLevel == nil {
			issues = append(issues, requiredIssue(append(specsPath, "reasoningLevel")))
		} else {
			rl := s.ReasoningLevel
			rlPath := append(specsPath, "reasoningLevel")
			if len(rl.Values) == 0 {
				issues = append(issues, optionSpecIssue(append(rlPath, "values"), "reasoningLevel.values 不能为空"))
			} else if hasBlank(rl.Values) || hasDuplicate(rl.Values) {
				issues = append(issues, optionSpecIssue(append(rlPath, "values"), "reasoningLevel.values 必须是非空字符串且不能重复"))
			}
		}
		if s.MaxOutputTokens == nil {
			issues = append(issues, requiredIssue(append(specsPath, "maxOutputTokens")))
		} else if s.MaxOutputTokens.Max == nil || *s.MaxOutputTokens.Max <= 0 {
			issues = append(issues, optionSpecIssue(append(specsPath, "maxOutputTokens", "max"), "max 必须是正整数"))
		}
	}
	return issues
}

// Registry ports ProviderRegistry (registry.ts): the published execution
// view with revision and change listeners. In the TS the refresh loop is the
// only writer (single-threaded by construction); the Go refresh loop can have
// a superseded loop running concurrently, so the view swap is mutex-guarded.
type Registry struct {
	mu        sync.Mutex
	revision  int
	providers []RegistryProvider
	byID      map[string]*RegistryProvider
	models    map[string]map[string]*RegistryModel
	listeners []func(revision int, reason string)
}

// NewRegistry builds with the initial (empty) view.
func NewRegistry() *Registry {
	r := &Registry{byID: map[string]*RegistryProvider{}, models: map[string]map[string]*RegistryModel{}}
	return r
}

// Revision is the current view revision.
func (r *Registry) Revision() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revision
}

// Providers returns a copy of the current view.
func (r *Registry) Providers() []RegistryProvider {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RegistryProvider, len(r.providers))
	copy(out, r.providers)
	return out
}

// GetProvider finds by id (a stable pointer into the published view).
func (r *Registry) GetProvider(providerID string) *RegistryProvider {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[providerID]
}

// GetModel finds a provider's model.
func (r *Registry) GetModel(providerID, modelID string) *RegistryModel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.models[providerID][modelID]
}

// Replace swaps the view, bumping the revision, and notifies listeners.
// Duplicate ids/models are a hard error (the TS constructor throws).
func (r *Registry) Replace(providers []RegistryProvider, reason string) error {
	r.mu.Lock()
	byID := map[string]*RegistryProvider{}
	models := map[string]map[string]*RegistryModel{}
	dup := func(kind, id string) *simpleError {
		return &simpleError{"Registry 中存在重复 " + kind + ": " + id}
	}
	for i := range providers {
		p := &providers[i]
		if _, exists := byID[p.ProviderID]; exists {
			r.mu.Unlock()
			return dup("Provider", p.ProviderID)
		}
		byID[p.ProviderID] = p
		m := map[string]*RegistryModel{}
		for j := range p.Models {
			if _, exists := m[p.Models[j].ModelID]; exists {
				r.mu.Unlock()
				return dup("Model", p.ProviderID+"/"+p.Models[j].ModelID)
			}
			m[p.Models[j].ModelID] = &p.Models[j]
		}
		models[p.ProviderID] = m
	}
	r.providers = providers
	r.byID = byID
	r.models = models
	r.revision++
	revision := r.revision
	listeners := append([]func(int, string){}, r.listeners...)
	// Notify AFTER unlock: a listener (registry service's change fan-in) calls
	// back into View/ValidateSelection and must not re-enter this mutex.
	r.mu.Unlock()
	for _, listener := range listeners {
		listener(revision, reason)
	}
	return nil
}

// OnDidChange registers a listener; the returned func unregisters.
func (r *Registry) OnDidChange(listener func(revision int, reason string)) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners = append(r.listeners, listener)
	return func() {
		for i, candidate := range r.listeners {
			if &candidate == &listener {
				r.listeners = append(r.listeners[:i], r.listeners[i+1:]...)
				return
			}
		}
	}
}

// SelectionValidation ports ModelSelectionValidation.
type SelectionValidation struct {
	OK              bool     `json:"ok"`
	Code            string   `json:"code,omitempty"`
	ProviderID      string   `json:"providerId,omitempty"`
	ModelID         string   `json:"modelId,omitempty"`
	ReasoningLevel  string   `json:"reasoningLevel,omitempty"`
	SupportedLevels []string `json:"supportedLevels,omitempty"`
}

// ValidateSelection ports ProviderRegistry.validateSelection.
func (r *Registry) ValidateSelection(selection ModelSelection) SelectionValidation {
	if _, ok := r.byID[selection.ProviderID]; !ok {
		return SelectionValidation{Code: "provider-not-found", ProviderID: selection.ProviderID}
	}
	model := r.GetModel(selection.ProviderID, selection.ModelID)
	if model == nil {
		return SelectionValidation{Code: "model-not-found", ProviderID: selection.ProviderID, ModelID: selection.ModelID}
	}
	return validateModelSelectionOptions(model, selection)
}

// ModelSelection ports ModelSelection (shared/model-selection.ts).
type ModelSelection struct {
	ProviderID string            `json:"providerId"`
	ModelID    string            `json:"modelId"`
	Options    *SelectionOptions `json:"options,omitempty"`
}

// SelectionOptions is the strict options object.
type SelectionOptions struct {
	ReasoningLevel string `json:"reasoningLevel"`
}

// validateModelSelectionOptions ports the same-named helper: reasoningLevel
// must be present and one of the published values.
func validateModelSelectionOptions(model *RegistryModel, selection ModelSelection) SelectionValidation {
	if selection.Options == nil || selection.Options.ReasoningLevel == "" {
		return SelectionValidation{Code: "reasoning-level-missing", ProviderID: selection.ProviderID, ModelID: selection.ModelID}
	}
	values := modelReasoningValues(model)
	if !contains(values, selection.Options.ReasoningLevel) {
		return SelectionValidation{
			Code: "reasoning-level-not-supported", ProviderID: selection.ProviderID,
			ModelID: selection.ModelID, ReasoningLevel: selection.Options.ReasoningLevel,
			SupportedLevels: values,
		}
	}
	return SelectionValidation{OK: true}
}

func modelReasoningValues(model *RegistryModel) []string {
	if model == nil || model.Config == nil || model.Config.OptionSpecs == nil ||
		model.Config.OptionSpecs.ReasoningLevel == nil {
		return nil
	}
	return model.Config.OptionSpecs.ReasoningLevel.Values
}

// ValidateModelSelectionOptions is the exported form for the selection
// facades.
func ValidateModelSelectionOptions(model *RegistryModel, selection ModelSelection) SelectionValidation {
	return validateModelSelectionOptions(model, selection)
}

// helpers

func requiredIssue(path []string) ValidationIssue {
	return ValidationIssue{Code: IssueRequiredMissing, Path: path, Message: "缺少必填配置 " + joinPath(path)}
}

func optionSpecIssue(path []string, message string) ValidationIssue {
	return ValidationIssue{Code: IssueInvalidOptionSpec, Path: path, Message: message}
}

func joinPath(path []string) string { return strings.Join(path, ".") }

func validURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func hasBlank(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return true
		}
	}
	return false
}

func hasDuplicate(values []string) bool {
	return len(stringSet(values)) != len(values)
}

// ValidationIssuesOf is the exported model-completeness check for callers
// holding a resolved config (the facades' resolveModelConfig preview).
func ValidationIssuesOf(config *ModelConfig, path []string) []ValidationIssue {
	return validateModelComplete(config, path)
}
