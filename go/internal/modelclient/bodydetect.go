package modelclient

import (
	"encoding/json"
	"strconv"
	"strings"
)

const maxProviderMessageChars = 1000

// DetectedBusinessFailure mirrors the return of readProviderBusinessFailureFromBody.
type DetectedBusinessFailure struct {
	Found               bool
	ProviderCode        string
	ProviderMessage     string
	ProviderRequestID   string
	ResponseBodySummary map[string]any
	StatusCode          *int
}

// ReadProviderBusinessFailureFromBody ports readProviderBusinessFailureFromBody.
// A body is a business failure when `success === false` or the provider code is a
// non-zero code.
func ReadProviderBusinessFailureFromBody(body any) DetectedBusinessFailure {
	record, ok := asRecordLoose(body)
	if !ok {
		return DetectedBusinessFailure{}
	}
	errorRecord, _ := asRecordLoose(record["error"])

	providerCode := firstNonEmptyCode(
		toProviderCode(record["providerCode"]),
		toProviderCode(stringTrimmedKey(errorRecord, "providerCode")),
		toProviderCode(record["error_code"]),
		toProviderCode(stringTrimmedKey(errorRecord, "error_code")),
		toProviderCode(record["code"]),
		toProviderCode(stringTrimmedKey(errorRecord, "code")),
	)
	providerMessage := readProviderMessageFromRecord(record)
	failedBySuccess, _ := record["success"].(bool)
	failedBySuccess = record["success"] == false
	failedByCode := isNonZeroProviderCode(providerCode)

	if !failedBySuccess && !failedByCode {
		return DetectedBusinessFailure{}
	}
	return DetectedBusinessFailure{
		Found:               true,
		ProviderCode:        providerCode,
		ProviderMessage:     providerMessage,
		ProviderRequestID:   readProviderRequestIDFromRecord(record),
		ResponseBodySummary: summarizeProviderBusinessBody(record),
		StatusCode:          providerCodeToStatusCode(providerCode),
	}
}

func firstNonEmptyCode(codes ...string) string {
	for _, c := range codes {
		if c != "" {
			return c
		}
	}
	return ""
}

func stringTrimmedKey(rec map[string]any, key string) any {
	if rec == nil {
		return nil
	}
	return rec[key]
}

func isNonZeroProviderCode(value string) bool {
	if value == "" {
		return false
	}
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return false
	}
	if n, err := strconv.ParseFloat(normalized, 64); err == nil {
		return n != 0
	}
	return true
}

func providerCodeToStatusCode(value string) *int {
	if value == "" {
		return nil
	}
	normalized := strings.TrimSpace(value)
	if !isAllDigits(normalized) {
		return nil
	}
	n, err := strconv.Atoi(normalized)
	if err != nil {
		return nil
	}
	if n >= 100 && n <= 599 {
		return &n
	}
	return nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func toProviderCode(value any) string {
	switch v := value.(type) {
	case string:
		normalized := strings.TrimSpace(v)
		if normalized == "" {
			return ""
		}
		if strings.ToUpper(normalized) == providerBusinessErrorWrapperCode {
			return ""
		}
		return normalized
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

func readProviderMessageFromRecord(record map[string]any) string {
	errorRecord, _ := asRecordLoose(record["error"])
	return firstNonEmpty(
		normalizeProviderMessageField(record["msg"]),
		normalizeProviderMessageField(record["message"]),
		normalizeProviderMessageField(errorRecord["msg"]),
		normalizeProviderMessageField(errorRecord["message"]),
		normalizeProviderMessageField(record["error"]),
	)
}

func readProviderRequestIDFromRecord(record map[string]any) string {
	errorRecord, _ := asRecordLoose(record["error"])
	return firstNonEmpty(
		normalizeProviderID(record["request_id"]),
		normalizeProviderID(record["requestId"]),
		normalizeProviderID(record["id"]),
		normalizeProviderID(errorRecord["request_id"]),
		normalizeProviderID(errorRecord["requestId"]),
		normalizeProviderID(errorRecord["id"]),
	)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func normalizeProviderMessageField(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	normalized := collapseSpaces(strings.TrimSpace(s))
	if normalized == "" {
		return ""
	}
	if len(normalized) > maxProviderMessageChars {
		return normalized[:maxProviderMessageChars] + "..."
	}
	return normalized
}

func normalizeProviderID(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	normalized := strings.TrimSpace(s)
	if normalized == "" {
		return ""
	}
	if len(normalized) > maxProviderMessageChars {
		return normalized[:maxProviderMessageChars] + "..."
	}
	return normalized
}

func collapseSpaces(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

func summarizeProviderBusinessBody(record map[string]any) map[string]any {
	summary := map[string]any{"keys": objectKeys(record, 20)}
	copyScalarLoose(record, summary, "success")
	copyScalarLoose(record, summary, "code")
	copyScalarLoose(record, summary, "error_code")
	copyScalarLoose(record, summary, "msg")
	copyScalarLoose(record, summary, "message")
	copyScalarLoose(record, summary, "request_id")
	copyScalarLoose(record, summary, "requestId")

	if errorRecord, ok := asRecordLoose(record["error"]); ok {
		errorSummary := map[string]any{"keys": objectKeys(errorRecord, 20)}
		copyScalarLoose(errorRecord, errorSummary, "code")
		copyScalarLoose(errorRecord, errorSummary, "error_code")
		copyScalarLoose(errorRecord, errorSummary, "msg")
		copyScalarLoose(errorRecord, errorSummary, "message")
		copyScalarLoose(errorRecord, errorSummary, "request_id")
		copyScalarLoose(errorRecord, errorSummary, "requestId")
		copyScalarLoose(errorRecord, errorSummary, "type")
		summary["error"] = errorSummary
	}
	return summary
}

func copyScalarLoose(source, target map[string]any, key string) {
	v, ok := source[key]
	if !ok {
		return
	}
	switch v.(type) {
	case nil, string, float64, bool, int64, int, json.Number:
		target[key] = v
	}
}

func objectKeys(rec map[string]any, limit int) []any {
	keys := make([]string, 0, len(rec))
	for k := range rec {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out
}

// asRecordLoose is like asRecord but allows arrays to be rejected and returns the
// map for any object value.
func asRecordLoose(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}
