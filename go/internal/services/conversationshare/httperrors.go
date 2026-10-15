// httperrors.go holds the response-error classification and the retry-after
// parsing. Split out of httpclient.go so the endpoint methods stay short;
// symbols are unchanged.
package conversationshare

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// known/unknown code, and the HTML-page fallbacks (5xx → network, 429 →
// rate_limited).
func (c *Client) responseError(path, method string, response *http.Response, text, responseRequestID string) error {
	status := int64(response.StatusCode)
	if response.StatusCode == 401 && strings.TrimSpace(string(text)) == "" {
		return &clientError{kind: KindAuthenticationRequired, message: "Conversation share authentication required", status: status, requestID: responseRequestID}
	}
	if trimmed := strings.TrimSpace(string(text)); trimmed != "" {
		var envelope struct {
			Code int64  `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(trimmed), &envelope); err == nil && envelope.Msg != "" {
			// 未知业务码保留服务端 msg 与 status（TS：认不得的落 unknown）。
			kind := KindUnknown
			clientErr := &clientError{kind: kind, message: envelope.Msg, status: status, requestID: responseRequestID}
			if mapped, known := errorKindByCode[envelope.Code]; known {
				clientErr.kind = mapped
				clientErr.code = envelope.Code
				clientErr.hasCode = true
				if envelope.Code == 3215 {
					if retry, ok := parseRetryAfterMs(response.Header.Get("retry-after"), c.now()); ok {
						clientErr.retryAfterMs = retry
						clientErr.hasRetry = true
					}
				}
			}
			return clientErr
		}
		// JSON 解析失败：5xx 的非 JSON 响应归为 network（网关 HTML 错误页）。
		if response.StatusCode >= 500 {
			return &clientError{
				kind: KindNetwork, message: fmt.Sprintf("Conversation share API upstream failed with HTTP %d", response.StatusCode),
				status: status, requestID: responseRequestID,
			}
		}
		return &clientError{
			kind: KindInvalidContract, message: "Conversation share API returned invalid JSON",
			status: status, requestID: responseRequestID,
		}
	}
	kind := KindUnknown
	if response.StatusCode >= 500 {
		kind = KindNetwork
	} else if response.StatusCode == 429 {
		kind = KindRateLimited
	}
	return &clientError{
		kind: kind, message: fmt.Sprintf("Conversation share API failed with HTTP %d", response.StatusCode),
		status: status, requestID: responseRequestID,
	}
}

// assertSupportedSchemaVersion ports assertSupportedSchemaVersion for the
// shapes that carry one: 版本高于本端时不猜语义，用户该看到「请升级」。
func assertSupportedSchemaVersion(out any) error {
	var version int
	switch typed := out.(type) {
	case *PreviewData:
		version = typed.SchemaVersion
	case *Continuation:
		version = typed.SchemaVersion
	default:
		return nil
	}
	if version > SchemaVersion {
		return &clientError{kind: KindUnsupportedSchema, message: "Conversation share payload requires a newer Nex version"}
	}
	return nil
}

// normalizeRequestID ports normalizeRequestId.
func normalizeRequestID(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > 128 || !safeRequestIDRe.MatchString(trimmed) {
		return ""
	}
	return trimmed
}

// networkMessage keeps transport error text generic (no URL/host leakage).
func networkMessage(err error) string {
	message := err.Error()
	if message == "" {
		return "Conversation share network error"
	}
	return "Conversation share network error"
}

var (
	imfFixdateRe  = regexp.MustCompile(`^(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun), (?:0[1-9]|[12]\d|3[01]) (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) \d{4} (?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d GMT$`)
	rfc850DateRe  = regexp.MustCompile(`^(?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday), (?:0[1-9]|[12]\d|3[01])-(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)-\d{2} (?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d GMT$`)
	asctimeDateRe = regexp.MustCompile(`^(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun) (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) (?: [1-9]|0[1-9]|[12]\d|3[01]) (?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d \d{4}$`)
	digitsOnlyRe  = regexp.MustCompile(`^\d+$`)
)

// parseRetryAfterMs ports parseRetryAfterMs: delta-seconds or one of the
// three HTTP-date formats, strictly validated before parsing.
func parseRetryAfterMs(value string, now time.Time) (int64, bool) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return 0, false
	}
	if digitsOnlyRe.MatchString(normalized) {
		seconds, err := strconv.ParseInt(normalized, 10, 64)
		if err != nil {
			return 0, false
		}
		delayMs := seconds * 1000
		if delayMs > 0 && delayMs <= math.MaxInt64/2 {
			return delayMs, true
		}
		return 0, false
	}
	// HTTP-date 三种格式；Go 的 time 解析对 IMF-fixdate 足够，其余两种先由正则
	// 严格校验再规范化转换。
	layouts := []string{time.RFC1123, time.RFC850, time.ANSIC}
	for index, layout := range layouts {
		matched := index == 0 && imfFixdateRe.MatchString(normalized) ||
			index == 1 && rfc850DateRe.MatchString(normalized) ||
			index == 2 && asctimeDateRe.MatchString(normalized)
		if !matched {
			continue
		}
		parsed, err := time.Parse(layout, normalized)
		if err != nil {
			continue
		}
		if delayMs := parsed.Sub(now).Milliseconds(); delayMs > 0 {
			return delayMs, true
		}
		return 0, false
	}
	return 0, false
}
