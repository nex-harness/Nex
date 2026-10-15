// modelsdevmatch.go: the pure halves of modelsDevCatalog.ts — namespace-aware
// lookup and the cross-provider majority aggregation.
package modelprovider

import (
	"sort"
	"strings"
)

// FindModelsDevModels ports findModelsDevModels: 命名空间感知查找 — full ID
// plus every suffix after each "/" separator, case-insensitive.
func FindModelsDevModels(catalog ModelsDevCatalog, modelID string) []ModelsDevMatch {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	if normalized == "" {
		return nil
	}
	candidates := map[string]bool{normalized: true}
	// 命名空间可能有多层（"cst/claude/claude-sonnet-5-5"），目录键本身也可能带斜杠
	// （"anthropic/claude-..."），所以逐层剥掉前缀，把每个后缀都作为候选.
	for i := 0; i < len(normalized); i++ {
		if normalized[i] == '/' && i+1 < len(normalized) {
			candidates[normalized[i+1:]] = true
		}
	}
	providerIDs := make([]string, 0, len(catalog))
	for id := range catalog {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	var matches []ModelsDevMatch
	for _, providerID := range providerIDs {
		modelKeys := make([]string, 0, len(catalog[providerID].Models))
		for key := range catalog[providerID].Models {
			modelKeys = append(modelKeys, key)
		}
		sort.Strings(modelKeys)
		for _, key := range modelKeys {
			if candidates[strings.ToLower(strings.TrimSpace(key))] {
				matches = append(matches, ModelsDevMatch{ProviderID: providerID, Model: catalog[providerID].Models[key]})
			}
		}
	}
	return matches
}

// majorityInt / majorityBool / majorityStrings port majority(): 众数聚合，平票
// 时数值取最大，布尔取 true.
func majorityInt(values []*int64) *int64 {
	counts := map[int64]int{}
	var best *int64
	bestCount := 0
	for _, v := range values {
		if v == nil {
			continue
		}
		counts[*v]++
		if counts[*v] > bestCount || (counts[*v] == bestCount && best != nil && *v > *best) {
			best, bestCount = v, counts[*v]
		}
	}
	if len(counts) == 0 {
		return nil
	}
	if best == nil {
		// first entry wins ties when nothing set yet
		for _, v := range values {
			if v != nil {
				best = v
				break
			}
		}
	}
	return best
}

func majorityBool(values []*bool) *bool {
	var trueCount, falseCount int
	for _, v := range values {
		if v == nil {
			continue
		}
		if *v {
			trueCount++
		} else {
			falseCount++
		}
	}
	if trueCount == 0 && falseCount == 0 {
		return nil
	}
	result := trueCount >= falseCount // ties prefer true (TS prefers(candidate===true))
	return &result
}

func effortLevels(model modelsDevModel) []string {
	for _, option := range model.ReasoningOptions {
		if option.Type != "effort" {
			continue
		}
		values := option.Values
		if len(values) == 0 {
			return nil
		}
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				return nil
			}
		}
		if len(stringSetOf(values)) != len(values) {
			return nil
		}
		return values
	}
	return nil
}

// ModelConfigFromMatches ports modelConfigFromMatches: 跨供应商聚合同一
// 模型的条目，产出单一稀疏 ModelConfig (object form for the RPC result).
func ModelConfigFromMatches(matches []ModelsDevMatch) map[string]any {
	var contextWindows []*int64
	var maxOutputs []*int64
	var toolCalls []*bool
	var jsonOutputs []*bool
	var levelLists [][]string
	modalityLists := [][]string{}
	for _, match := range matches {
		m := match.Model
		if m.Limit != nil {
			contextWindows = append(contextWindows, m.Limit.Context)
			maxOutputs = append(maxOutputs, m.Limit.Output)
		}
		toolCalls = append(toolCalls, m.ToolCall)
		jsonOutputs = append(jsonOutputs, m.StructuredOutput)
		if levels := effortLevels(m); levels != nil {
			levelLists = append(levelLists, levels)
		}
		if m.Modalities != nil {
			modalityLists = append(modalityLists, m.Modalities.Input)
		}
	}
	properties := map[string]any{}
	if contextWindow := majorityInt(contextWindows); contextWindow != nil {
		properties["contextWindow"] = *contextWindow
	}
	if supportsToolCall := majorityBool(toolCalls); supportsToolCall != nil {
		properties["supportsToolCall"] = *supportsToolCall
	}
	if supportsJsonSchemaOutput := majorityBool(jsonOutputs); supportsJsonSchemaOutput != nil {
		properties["supportsJsonSchemaOutput"] = *supportsJsonSchemaOutput
	}
	// 输入模态：任一条目声明支持即视为支持；有模态数据时未声明的类型按不支持.
	if len(modalityLists) > 0 {
		declared := map[string]bool{}
		for _, list := range modalityLists {
			for _, modality := range list {
				declared[strings.ToLower(strings.TrimSpace(modality))] = true
			}
		}
		inputFormat := map[string]bool{
			"supportsText":  declared["text"],
			"supportsImage": declared["image"],
			"supportsVideo": declared["video"],
			"supportsAudio": declared["audio"],
			"supportsPdf":   declared["pdf"],
		}
		properties["inputFormat"] = inputFormat
		properties["outputFormat"] = map[string]bool{"supportsText": true}
	}
	optionSpecs := map[string]any{}
	if maxOutput := majorityInt(maxOutputs); maxOutput != nil {
		optionSpecs["maxOutputTokens"] = map[string]any{"max": *maxOutput}
	}
	if levels := majorityLevelList(levelLists); levels != nil {
		optionSpecs["reasoningLevel"] = map[string]any{"values": levels}
	}
	result := map[string]any{}
	if len(properties) > 0 {
		result["properties"] = properties
	}
	if len(optionSpecs) > 0 {
		result["optionSpecs"] = optionSpecs
	}
	return result
}

func majorityLevelList(lists [][]string) []string {
	if len(lists) == 0 {
		return nil
	}
	counts := map[string]int{}
	var best []string
	bestCount := 0
	for _, list := range lists {
		key := strings.Join(list, "\x00")
		counts[key]++
		if counts[key] > bestCount {
			best, bestCount = list, counts[key]
		}
	}
	return best
}
