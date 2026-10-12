package modelclient

import "strings"

// businessMapping mirrors ProviderBusinessCodeMapping.
type businessMapping struct {
	code        string
	message     string
	reason      string
	retryReason string
	retryable   bool
}

var (
	terminalRateLimitMapping = businessMapping{
		code: CodeModelRateLimited, reason: ReasonRateLimited, retryReason: ReasonRateLimited, retryable: false,
	}
	terminalBusinessMapping = businessMapping{
		code: CodeModelRequestFailed, reason: ReasonUnknown, retryReason: ReasonNetworkError, retryable: false,
	}
	retryableRateLimitMapping = businessMapping{
		code: CodeModelRateLimited, reason: ReasonRateLimited, retryReason: ReasonRateLimited, retryable: true,
	}
	retryableOverloadMapping = businessMapping{
		code: CodeModelRequestFailed, reason: ReasonProviderOverloaded, retryReason: ReasonProviderOverloaded, retryable: true,
	}
)

// providerBusinessMappings mirrors PROVIDER_BUSINESS_CODE_MAPPINGS exactly,
// including the programmatic additions.
var providerBusinessMappings = func() map[string]businessMapping {
	m := map[string]businessMapping{
		"500":  {code: CodeModelRequestFailed, reason: ReasonServerError, retryReason: ReasonServerError, retryable: true},
		"1006": {code: CodeProviderNotConfigured, reason: ReasonAuthFailed, retryReason: ReasonAuthRefresh, retryable: false},
		"1005": {code: CodeModelRequestFailed, reason: ReasonInvalidRequest, retryReason: ReasonNetworkError, retryable: false},
		"3006": {code: CodeModelNotFound, reason: ReasonInvalidRequest, retryReason: ReasonNetworkError, retryable: false},
		"3001": {code: CodeInvalidModelRequest, reason: ReasonInvalidRequest, retryReason: ReasonNetworkError, retryable: false},
		"3007": {code: CodeInvalidModelRequest, reason: ReasonAuthFailed, retryReason: ReasonAuthRefresh, retryable: false},
		"3008": terminalRateLimitMapping,
		"3009": terminalRateLimitMapping,
		"3010": terminalRateLimitMapping,
		"1120": {code: CodeModelRequestFailed, reason: ReasonServerError, retryReason: ReasonServerError, retryable: true},
		"1230": {code: CodeModelRequestFailed, reason: ReasonServerError, retryReason: ReasonServerError, retryable: true},
		"1234": {code: CodeModelRequestFailed, reason: ReasonNetworkError, retryReason: ReasonNetworkError, retryable: true},
		"1261": {code: CodeModelContextExceeded, reason: ReasonContextExceeded, retryReason: ReasonNetworkError, retryable: false},
		"1113": {code: CodeModelRequestFailed, reason: ReasonUnknown, retryReason: ReasonNetworkError, retryable: false},
		"1302": retryableRateLimitMapping,
		"1303": retryableRateLimitMapping,
		"1305": retryableRateLimitMapping,
		"1304": terminalRateLimitMapping,
		"1308": terminalRateLimitMapping,
		"1309": {code: CodeModelRequestFailed, reason: ReasonUnknown, retryReason: ReasonNetworkError, retryable: false},
		"1310": terminalRateLimitMapping,
		"1311": {code: CodeModelRequestFailed, reason: ReasonUnknown, retryReason: ReasonNetworkError, retryable: false},
		"1312": retryableOverloadMapping,
		"1313": terminalRateLimitMapping,
		"3002": retryableRateLimitMapping,
		"2007": {code: CodeModelRequestFailed, reason: ReasonServerError, retryReason: ReasonServerError, retryable: true},
	}
	for _, code := range []string{
		"insufficient_quota", "credit_balance_exhausted", "organization_spend_limit_exceeded",
		"project_spend_limit_exceeded", "organization_usage_limit_exceeded", "exceeded_current_quota_error",
		"2056", "20097", "1316", "1317", "1318", "1319", "1320", "1321",
	} {
		m[code] = terminalRateLimitMapping
	}
	for _, code := range []string{"1008", "1314", "1315"} {
		m[code] = terminalBusinessMapping
	}
	for _, code := range []string{"rate_limit_reached_error", "rate_limit_error"} {
		m[code] = retryableRateLimitMapping
	}
	for _, code := range []string{"engine_overloaded_error", "overloaded_error"} {
		m[code] = retryableOverloadMapping
	}
	return m
}()

func providerBusinessCodeMapping(providerCode string) (businessMapping, bool) {
	m, ok := providerBusinessMappings[providerCode]
	return m, ok
}

// providerNetworkBusinessCodes mirrors PROVIDER_NETWORK_BUSINESS_CODES.
var providerNetworkBusinessCodes = map[string]bool{"1234": true}

// providerInternalNetworkMessages mirrors PROVIDER_INTERNAL_NETWORK_MESSAGES.
var providerInternalNetworkMessages = map[string]bool{
	"500 internal network error": true,
	"internal network error":     true,
	"internal network failure":   true,
}

func isRetryableProviderBusinessNetworkFailure(attrs ErrorAttrs, providerCode string) bool {
	if providerCode != "" {
		if isNetworkFailure(providerCode) {
			return true
		}
		if providerNetworkBusinessCodes[providerCode] {
			return true
		}
		normalized := strings.ToLower(providerCode)
		if normalized == "network_error" || normalized == "network_error_retryable" {
			return true
		}
	}
	return isProviderBusinessInternalNetworkFailure(attrs)
}

func isRetryableProviderBusinessTimeoutFailure(attrs ErrorAttrs, providerCode string, statusCode *int) bool {
	effective := statusCode
	if effective == nil {
		effective = attrs.StatusCode
	}
	if effective == nil {
		effective = attrs.ResponseStatus
	}
	return isTimeoutFailure(ErrorAttrs{Name: attrs.Name, Code: providerCode}, providerCode, effective)
}

func isProviderBusinessInternalNetworkFailure(attrs ErrorAttrs) bool {
	if readResponseBodyErrorType(attrs.ResponseBodySummary) != "api_error" {
		return false
	}
	normalized := normalizeProviderMessage(attrs.ProviderMessage)
	if normalized == "" {
		normalized = normalizeProviderMessage(attrs.Message)
	}
	if normalized == "" {
		normalized = normalizeProviderMessage(readResponseBodyErrorMessage(attrs.ResponseBodySummary))
	}
	return normalized != "" && providerInternalNetworkMessages[normalized]
}

func readResponseBodyErrorType(summary any) string {
	outer, ok := asRecord(summary)
	if !ok {
		return ""
	}
	inner, ok := asRecord(outer["error"])
	if !ok {
		return ""
	}
	return stringTrimmed(inner["type"])
}

func readResponseBodyErrorMessage(summary any) string {
	outer, ok := asRecord(summary)
	if !ok {
		return ""
	}
	inner, ok := asRecord(outer["error"])
	if !ok {
		return ""
	}
	s, _ := inner["message"].(string)
	return s
}

func normalizeProviderMessage(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(s))
}

func stringTrimmed(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}
