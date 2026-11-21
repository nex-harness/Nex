package ai

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
)

//go:embed models_seed.json
var embeddedModelsSeed []byte

// DefaultModelsDevURL is the public models.dev JSON registry endpoint.
const DefaultModelsDevURL = "https://models.dev/api.json"

// ModelMeta holds capability, context window, and token pricing metadata for an LLM.
type ModelMeta struct {
	ID                string       `json:"id"`
	ProviderID        string       `json:"provider_id"`
	DisplayName       string       `json:"display_name"`
	Source            string       `json:"source"` // "models.dev" | "custom"
	ContextWindow     int          `json:"context_window"`
	MaxOutputTokens   int          `json:"max_output_tokens"`
	SupportsVision    bool         `json:"supports_vision"`
	SupportsPDF       bool         `json:"supports_pdf"`
	SupportsAudio     bool         `json:"supports_audio"`
	SupportsVideo     bool         `json:"supports_video"`
	SupportsTools     bool         `json:"supports_tools"`
	SupportsReasoning bool              `json:"supports_reasoning"`
	SupportsCaching   bool              `json:"supports_caching"`
	ReasoningLevels   []string          `json:"reasoning_levels,omitempty"`
	ReasoningMap      map[string]string `json:"reasoning_map,omitempty"`
	Pricing           ModelPricing      `json:"pricing"`
}

// DefaultReasoningLevels returns the ordered reasoning effort levels supported by a model.
// Levels are strictly ordered from lowest (off) to highest (on/high/xhigh/max).
// Non-reasoning models return nil unless explicitly configured with ReasoningLevels.
func DefaultReasoningLevels(m ModelMeta) []string {
	if len(m.ReasoningLevels) > 0 {
		return m.ReasoningLevels
	}
	if !m.SupportsReasoning {
		return nil
	}
	idLower := strings.ToLower(m.ID)
	switch {
	case strings.Contains(idLower, "deepseek"):
		return []string{"low", "high", "max"}
	case strings.Contains(idLower, "claude") && (strings.Contains(idLower, "opus") || strings.Contains(idLower, "sonnet") || strings.Contains(idLower, "4") || strings.Contains(idLower, "5")):
		return []string{"off", "low", "medium", "high", "max"}
	case strings.Contains(idLower, "gpt-5") || strings.Contains(idLower, "gpt-6") || strings.Contains(idLower, "codex") || strings.Contains(idLower, "o3") || strings.Contains(idLower, "o4"):
		return []string{"off", "low", "medium", "high", "xhigh"}
	case strings.Contains(idLower, "gemini-4") || (strings.Contains(idLower, "gemini-3") && strings.Contains(idLower, "pro")):
		return []string{"low", "medium", "high"}
	case strings.Contains(idLower, "gemini"):
		return []string{"off", "minimal", "low", "medium", "high"}
	case strings.Contains(idLower, "qwq") || strings.Contains(idLower, "glm-4.5") || strings.Contains(idLower, "glm-z1"):
		return []string{"off", "on"}
	default:
		return []string{"off", "low", "medium", "high"}
	}
}

// CanonicalEffortRank maps standard effort words to a comparable intensity rank (0..6).
// Unknown custom words return -1.
func CanonicalEffortRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "off", "none", "disabled":
		return 0
	case "minimal", "min":
		return 1
	case "low":
		return 2
	case "medium", "med", "on", "enabled", "auto":
		return 3
	case "high":
		return 4
	case "xhigh", "extra_high", "extra-high":
		return 5
	case "max", "maximum", "ultra":
		return 6
	default:
		return -1
	}
}

// ResolveWireEffort translates a requested ThinkingEffort into a provider-compatible wire value
// by checking req.ReasoningMap first, then matching or clamping against driverSupported values
// by canonical intensity rank. Returns ("", false) when reasoning should be omitted/off.
func ResolveWireEffort(req *StreamRequest, driverSupported []string) (wireValue string, active bool) {
	raw := strings.TrimSpace(string(req.ThinkingEffort))
	lower := strings.ToLower(raw)

	// 1. Check explicit per-model ReasoningMap first (supports custom gateway words & explicit off mapping like "none")
	if len(req.ReasoningMap) > 0 {
		lookupKey := lower
		if lookupKey == "" {
			lookupKey = "off"
		}
		for k, v := range req.ReasoningMap {
			if strings.EqualFold(strings.TrimSpace(k), lookupKey) {
				mapped := strings.TrimSpace(v)
				if mapped == "" || strings.EqualFold(mapped, "null") {
					return "", false
				}
				if lookupKey == "off" || lookupKey == "none" {
					return mapped, false
				}
				return mapped, true
			}
		}
	}

	if lower == "" || lower == "off" || lower == "none" || lower == "disabled" {
		return "", false
	}

	// 2. If the model's own declared ReasoningLevels explicitly includes this level (e.g. DeepSeek "max"),
	// send it verbatim so model-specific effort names are honored!
	for _, declared := range req.ReasoningLevels {
		if strings.EqualFold(strings.TrimSpace(declared), raw) {
			return strings.TrimSpace(declared), true
		}
	}

	// 3. If the requested value is directly in driverSupported, send it verbatim
	for _, s := range driverSupported {
		if strings.EqualFold(s, raw) {
			return s, true
		}
	}

	// 3. If the model explicitly declared this custom word in ReasoningLevels (not in canonical catalog),
	// pass it through verbatim so custom gateway values work without hardcoding!
	reqRank := CanonicalEffortRank(lower)
	if reqRank < 0 {
		return raw, true
	}

	// 4. Clamp to closest supported wire level at or below reqRank (or lowest above if none below)
	bestVal := ""
	bestRank := -1
	for _, s := range driverSupported {
		sr := CanonicalEffortRank(s)
		if sr <= 0 {
			continue
		}
		if sr <= reqRank && sr > bestRank {
			bestRank = sr
			bestVal = s
		}
	}
	if bestVal != "" {
		return bestVal, true
	}
	// Fallback: pick lowest positive supported value
	lowestRank := 999
	for _, s := range driverSupported {
		sr := CanonicalEffortRank(s)
		if sr > 0 && sr < lowestRank {
			lowestRank = sr
			bestVal = s
		}
	}
	if bestVal != "" {
		return bestVal, true
	}
	return raw, true
}

// NewProvider instantiates a streaming Provider for the given Driver.
func NewProvider(driver Driver, baseURL, apiKey string, httpClient *http.Client) (Provider, error) {
	switch driver {
	case DriverOpenAIResponses:
		return NewOpenAIResponsesProvider(OpenAIResponsesConfig{
			BaseURL:    baseURL,
			APIKey:     apiKey,
			HTTPClient: httpClient,
		}), nil
	case DriverOpenAIChat:
		return NewOpenAIChatProvider(OpenAIChatConfig{
			BaseURL:    baseURL,
			APIKey:     apiKey,
			HTTPClient: httpClient,
		}), nil
	case DriverAnthropic:
		return NewAnthropicProvider(AnthropicConfig{
			BaseURL:    baseURL,
			APIKey:     apiKey,
			HTTPClient: httpClient,
		}), nil
	default:
		return nil, fmt.Errorf("ai: unsupported provider driver %q", driver)
	}
}

// EmbeddedModelsCatalog returns the offline built-in models seed snapshot.
func EmbeddedModelsCatalog() ([]ModelMeta, error) {
	var out []ModelMeta
	if err := json.Unmarshal(embeddedModelsSeed, &out); err != nil {
		return nil, fmt.Errorf("ai: parse embedded models seed: %w", err)
	}
	for i := range out {
		if len(out[i].ReasoningLevels) == 0 {
			out[i].ReasoningLevels = DefaultReasoningLevels(out[i])
		}
	}
	return out, nil
}

type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	Models map[string]modelsDevEntry `json:"models"`
}

type modelsDevReasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Min    int      `json:"min"`
	Max    int      `json:"max"`
}

type modelsDevEntry struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Attachment       bool                       `json:"attachment"`
	Reasoning        bool                       `json:"reasoning"`
	ReasoningOptions []modelsDevReasoningOption `json:"reasoning_options"`
	ToolCall         bool                       `json:"tool_call"`
	Modalities       struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Cost struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
}

func parseModelsDevReasoningLevels(opts []modelsDevReasoningOption) []string {
	var effortVals []string
	var hasToggle bool
	var hasBudget bool
	var budgetMin int

	for _, opt := range opts {
		switch strings.ToLower(strings.TrimSpace(opt.Type)) {
		case "effort":
			if len(effortVals) == 0 && len(opt.Values) > 0 {
				effortVals = opt.Values
			}
		case "toggle":
			hasToggle = true
		case "budget_tokens":
			hasBudget = true
			budgetMin = opt.Min
		}
	}

	if len(effortVals) > 0 {
		seen := make(map[string]bool, len(effortVals)+1)
		out := make([]string, 0, len(effortVals)+1)
		add := func(v string) {
			clean := strings.ToLower(strings.TrimSpace(v))
			if clean == "" {
				return
			}
			if clean == "none" || clean == "disabled" {
				clean = "off"
			}
			if !seen[clean] {
				seen[clean] = true
				out = append(out, clean)
			}
		}
		if hasToggle || (hasBudget && budgetMin == 0) {
			add("off")
		}
		for _, v := range effortVals {
			add(v)
		}
		return out
	}

	if hasToggle && !hasBudget {
		return []string{"off", "on"}
	}
	return nil
}

// FetchModelsDevRaw downloads the upstream models.dev JSON payload bytes.
func FetchModelsDevRaw(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if endpoint == "" {
		endpoint = DefaultModelsDevURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ai: fetch models.dev HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
}

// ParseModelsDevJSON parses the upstream https://models.dev/api.json registry payload.
func ParseModelsDevJSON(raw []byte) ([]ModelMeta, error) {
	var providers map[string]modelsDevProvider
	if err := json.Unmarshal(raw, &providers); err != nil {
		return nil, fmt.Errorf("ai: unmarshal models.dev json: %w", err)
	}

	var result []ModelMeta
	for provKey, prov := range providers {
		provID := prov.ID
		if provID == "" {
			provID = provKey
		}
		for modelKey, m := range prov.Models {
			mID := m.ID
			if mID == "" {
				mID = modelKey
			}
			disp := m.Name
			if disp == "" {
				disp = mID
			}
			inMods := m.Modalities.Input
			hasVision := slices.Contains(inMods, "image")
			hasPDF := slices.Contains(inMods, "pdf") || m.Attachment
			hasAudio := slices.Contains(inMods, "audio")
			hasVideo := slices.Contains(inMods, "video")
			ctxWin := m.Limit.Context
			if ctxWin <= 0 {
				ctxWin = 128000
			}
			maxOut := m.Limit.Output
			if maxOut <= 0 {
				maxOut = 16384
			}

			parsedLevels := parseModelsDevReasoningLevels(m.ReasoningOptions)
			meta := ModelMeta{
				ID:                mID,
				ProviderID:        provID,
				DisplayName:       disp,
				Source:            "models.dev",
				ContextWindow:     ctxWin,
				MaxOutputTokens:   maxOut,
				SupportsVision:    hasVision,
				SupportsPDF:       hasPDF,
				SupportsAudio:     hasAudio,
				SupportsVideo:     hasVideo,
				SupportsTools:     m.ToolCall,
				SupportsReasoning: m.Reasoning || len(parsedLevels) > 0,
				SupportsCaching:   m.Cost.CacheRead > 0 || m.Cost.CacheWrite > 0,
				ReasoningLevels:   parsedLevels,
				Pricing: ModelPricing{
					InputPer1M:      m.Cost.Input,
					OutputPer1M:     m.Cost.Output,
					CacheReadPer1M:  m.Cost.CacheRead,
					CacheWritePer1M: m.Cost.CacheWrite,
				},
			}
			if len(meta.ReasoningLevels) == 0 {
				meta.ReasoningLevels = DefaultReasoningLevels(meta)
			}
			result = append(result, meta)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].ProviderID != result[j].ProviderID {
			return result[i].ProviderID < result[j].ProviderID
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

// FetchModelsDevCatalog downloads and parses the live models.dev catalog.
func FetchModelsDevCatalog(ctx context.Context, client *http.Client, endpoint string) ([]ModelMeta, error) {
	raw, err := FetchModelsDevRaw(ctx, client, endpoint)
	if err != nil {
		return nil, err
	}
	return ParseModelsDevJSON(raw)
}

func providerMatchPriority(providerID, preferredProviderID string) int {
	p := strings.ToLower(strings.TrimSpace(providerID))
	pref := strings.ToLower(strings.TrimSpace(preferredProviderID))
	if pref != "" && p == pref {
		return 0
	}
	switch p {
	case "openai", "anthropic", "google", "deepseek", "zai", "xai", "mistral", "qwen", "moonshotai", "minimax", "cohere", "meta":
		return 1
	case "google-vertex", "google-vertex-anthropic", "amazon-bedrock", "azure", "openrouter":
		return 2
	case "github-copilot", "cloudflare-ai-gateway", "vercel", "fireworks-ai", "togetherai", "groq":
		return 3
	default:
		return 10
	}
}

// LookupModelMeta finds a model in catalog using exact match first, then
// progressive slash-prefix backoff (e.g. "xxx/xx/xx/glm-5.3" -> "glm-5.3").
// Official upstream providers are prioritized over third-party gateways.
func LookupModelMeta(catalog []ModelMeta, modelID string) (ModelMeta, bool) {
	return LookupModelMetaForProvider(catalog, modelID, "")
}

// LookupModelMetaForProvider finds a model in catalog, preferring preferredProviderID
// first, then official upstream providers, then third-party gateways.
func LookupModelMetaForProvider(catalog []ModelMeta, modelID, preferredProviderID string) (ModelMeta, bool) {
	target := strings.TrimSpace(modelID)
	if target == "" {
		return ModelMeta{}, false
	}

	candidates := []string{target}
	cur := target
	for {
		idx := strings.IndexByte(cur, '/')
		if idx < 0 || idx+1 >= len(cur) {
			break
		}
		cur = cur[idx+1:]
		candidates = append(candidates, cur)
	}

	for _, cand := range candidates {
		candLower := strings.ToLower(cand)
		var best ModelMeta
		bestRank := 999
		found := false
		for _, m := range catalog {
			if strings.ToLower(m.ID) == candLower {
				rank := providerMatchPriority(m.ProviderID, preferredProviderID)
				// Prefer entries that actually carry reasoning_levels when both are at the same tier
				if !found || rank < bestRank || (rank == bestRank && len(best.ReasoningLevels) == 0 && len(m.ReasoningLevels) > 0) {
					best = m
					bestRank = rank
					found = true
				}
			}
		}
		if found {
			return best, true
		}
	}
	return ModelMeta{}, false
}
