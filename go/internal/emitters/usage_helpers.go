package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/v4pub"
)

// querySourceOpt returns the querySource string, "" when absent (models the TS
// `optionalString(payload.querySource)`).
func querySourceOpt(m map[string]json.RawMessage) string {
	s, ok := nonEmpty(m, "querySource")
	if !ok {
		return ""
	}
	return s
}

func hasNum(m map[string]json.RawMessage, key string) bool {
	_, ok := num(m, key)
	return ok
}

func mustNumber(m map[string]json.RawMessage, key string) float64 {
	f, _ := num(m, key)
	return f
}

func mustInt(m map[string]json.RawMessage, key string) int64 {
	n, _ := integer(m, key)
	return n
}

func mustBool(m map[string]json.RawMessage, key string) bool {
	b, _ := boolField(m, key)
	return b
}

func nonNegOr0(m map[string]json.RawMessage, key string) float64 {
	f, ok := nonNeg(m, key)
	if !ok {
		return 0
	}
	return f
}

func numJSON(f float64) v4pub.JSONValue { return v4pub.Float(f) }

// totalTokensOf ports totalTokensOf(usage): getModelUsageTotalTokens with the
// nonNegative() guards. Each field is optional (nil = undefined).
func totalTokensOf(usage map[string]json.RawMessage) float64 {
	total := optNumPtr(usage, "totalTokens")
	input := optNumPtr(usage, "inputTokens")
	output := optNumPtr(usage, "outputTokens")
	cacheRead := firstPresentPtr(optNumPtr(usage, "cacheReadTokens"), optNumPtr(usage, "cacheTokens"))
	cacheWrite := optNumPtr(usage, "cacheWriteTokens")
	return getModelUsageTotalTokens(total, input, output, cacheRead, cacheWrite)
}

func cacheReadTokensOf(usage map[string]json.RawMessage) float64 {
	v := firstPresentPtr(optNumPtr(usage, "cacheReadTokens"), optNumPtr(usage, "cacheTokens"))
	return derefOr(v, 0)
}

func optNumPtr(m map[string]json.RawMessage, key string) *float64 {
	f, ok := nonNeg(m, key)
	if !ok {
		return nil
	}
	return &f
}

func firstPresentPtr(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

func derefOr(p *float64, d float64) float64 {
	if p == nil {
		return d
	}
	return *p
}

// getModelUsageTotalTokens ports getModelUsageTotalTokens (@nex/contracts).
//
//	inputTokens = usage.inputTokens ?? (cacheReadTokens ?? 0) + (cacheWriteTokens ?? 0)
//	result      = usage.totalTokens ?? inputTokens + (usage.outputTokens ?? 0)
func getModelUsageTotalTokens(total, input, output, cacheRead, cacheWrite *float64) float64 {
	if total != nil {
		return *total
	}
	in := derefOr(input, derefOr(cacheRead, 0)+derefOr(cacheWrite, 0))
	return in + derefOr(output, 0)
}
