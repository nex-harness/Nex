package modelclient

import "strings"

func readNestedProviderCode(value any) string {
	records := collectNestedRecords(value)
	if code := firstNormalizedRecordValue(records, "providerCode"); code != "" {
		return code
	}
	if code := firstNormalizedRecordValue(records, "error_code"); code != "" {
		return code
	}
	if code := firstNormalizedRecordValue(records, "code"); code != "" {
		return code
	}
	return firstBracketedProviderCode(records)
}

func readNestedStatusCode(value any) (*int, bool) {
	records := collectNestedRecords(value)
	for _, key := range []string{"statusCode", "status", "responseStatus"} {
		for _, rec := range records {
			if n, ok := numberProperty(rec, key); ok && normalizeHTTPFailureStatus(&n) != nil {
				return &n, true
			}
		}
	}
	return nil, false
}

func firstNormalizedRecordValue(records []map[string]any, key string) string {
	for _, rec := range records {
		if code := normalizeProviderCodeForError(rec[key]); code != "" {
			return code
		}
	}
	return ""
}

func firstBracketedProviderCode(records []map[string]any) string {
	for _, rec := range records {
		if code := readBigModelBracketedProviderCode(rec["message"]); code != "" {
			return code
		}
		if code := readBigModelBracketedProviderCode(rec["providerMessage"]); code != "" {
			return code
		}
	}
	return ""
}

// collectNestedRecords performs a breadth-first scan of a JSON value, returning
// every object record reachable from it. Values come from json.Unmarshal, so the
// graph is a tree (no cycles); depth is bounded anyway.
func collectNestedRecords(value any) []map[string]any {
	var result []map[string]any
	const maxDepth = 64
	type item struct {
		value any
		depth int
	}
	queue := []item{{value, 0}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.value == nil || current.depth > maxDepth {
			continue
		}
		if arr, ok := current.value.([]any); ok {
			for _, el := range arr {
				queue = append(queue, item{el, current.depth + 1})
			}
			continue
		}
		rec, ok := current.value.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, rec)
		for _, nested := range rec {
			switch nested.(type) {
			case map[string]any, []any:
				queue = append(queue, item{nested, current.depth + 1})
			}
		}
	}
	return result
}

func numberProperty(rec map[string]any, key string) (int, bool) {
	switch v := rec[key].(type) {
	case float64:
		return int(v), true
	case int64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

func readBigModelBracketedProviderCode(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return bigModelBracketedCode(s)
}

// bigModelBracketedCode matches /^\[(\d{4})\](?=\[)/ on the trimmed string.
func bigModelBracketedCode(value string) string {
	s := strings.TrimSpace(value)
	if len(s) < 7 || s[0] != '[' || s[5] != ']' || s[6] != '[' {
		return ""
	}
	for i := 1; i < 5; i++ {
		if s[i] < '0' || s[i] > '9' {
			return ""
		}
	}
	return s[1:5]
}
