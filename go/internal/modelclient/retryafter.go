package modelclient

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

func ParseRetryAfterMs(headers map[string]string) *int64 {
	if headers == nil {
		return nil
	}
	return ParseRetryAfterMsAt(headers, time.Now().UTC())
}

// ParseRetryAfterMsAt is ParseRetryAfterMs with an injected clock.
func ParseRetryAfterMsAt(headers map[string]string, now time.Time) *int64 {
	if headers == nil {
		return nil
	}
	if isShouldRetryHeaderFalse(headers) {
		return nil
	}
	if v, ok := findHeaderValue(headers, "retry-after-ms"); ok {
		if ms, ok2 := parseNumericHeaderMs(v); ok2 {
			return ms
		}
	}
	value, ok := findHeaderValue(headers, "retry-after")
	if !ok {
		return nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		ms := int64(round(seconds * 1000))
		if ms < 0 {
			ms = 0
		}
		return &ms
	}
	if ts, err := parseHTTPDate(value); err == nil {
		ms := ts.Sub(now).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		return &ms
	}
	return nil
}

func parseNumericHeaderMs(value string) (*int64, bool) {
	if value == "" {
		return nil, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return nil, false
	}
	ms := int64(round(f))
	if ms < 0 {
		ms = 0
	}
	return &ms, true
}

func parseHTTPDate(value string) (time.Time, error) {
	// JS Date.parse accepts RFC 1123 and a few other HTTP-date formats.
	for _, layout := range []string{
		time.RFC1123,
		time.RFC1123Z,
		time.RFC850,
		time.ANSIC,
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("unparsable date")
}

func findHeaderValue(headers map[string]string, name string) (string, bool) {
	lower := strings.ToLower(name)
	for k, v := range headers {
		if strings.ToLower(k) == lower {
			return v, true
		}
	}
	return "", false
}

func isShouldRetryHeaderFalse(headers map[string]string) bool {
	v, ok := findHeaderValue(headers, "x-should-retry")
	if !ok {
		return false
	}
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "false" || v == "0"
}

func asRecord(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}

func stringProperty(rec map[string]any, key string) string {
	if rec == nil {
		return ""
	}
	if s, ok := rec[key].(string); ok && len(s) > 0 {
		return s
	}
	return ""
}

func round(f float64) float64 {
	if f < 0 {
		return -floor(-f)
	}
	return floor(f + 0.5)
}

func floor(f float64) float64 {
	i := int64(f)
	if float64(i) > f {
		i--
	}
	return float64(i)
}

// IsModelStreamIdleTimeoutError ports isModelStreamIdleTimeoutError: it matches
// both the typed error and any object with the stable code/name.
