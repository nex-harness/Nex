package modelclient

import "strings"

func isContextExceededFailure(attrs ErrorAttrs) bool {
	cur := attrs
	seen := map[error]bool{}
	for {
		body := cur.ResponseBody
		if isContextExceededCode(cur.Code) ||
			isContextExceededCode(cur.ProviderCode) ||
			isContextExceededCode(standardResponseBodyCode(body)) ||
			isContextExceededMessage(cur.Message) ||
			isContextExceededMessage(standardResponseBodyMessage(body)) {
			return true
		}
		if cur.Cause == nil || seen[cur.Cause] {
			return false
		}
		seen[cur.Cause] = true
		cur = attrsOf(cur.Cause)
	}
}

func isContextExceededCode(value string) bool {
	switch strings.ToLower(value) {
	case "context_length_exceeded", "context_window_exceeded", "model_context_exceeded", "model_context_window_exceeded":
		return true
	}
	return false
}

func isContextExceededMessage(value string) bool {
	message := strings.ToLower(value)
	if message == "" {
		return false
	}
	return message == "model_context_window_exceeded" ||
		(strings.Contains(message, "context") && strings.Contains(message, "exceed")) ||
		(strings.Contains(message, "maximum context length") &&
			strings.Contains(message, "tokens") &&
			(strings.Contains(message, "requested") || strings.Contains(message, "resulted"))) ||
		strings.Contains(message, "prompt is too long") ||
		(strings.Contains(message, "input token count") &&
			strings.Contains(message, "exceed") &&
			strings.Contains(message, "maximum number of tokens allowed")) ||
		strings.Contains(message, "range of input length should be") ||
		(strings.Contains(message, "total message token length") &&
			strings.Contains(message, "exceed") &&
			strings.Contains(message, "model limit"))
}

func standardResponseBodyCode(body any) string {
	rec, ok := asRecord(body)
	if !ok {
		return ""
	}
	nested, _ := asRecord(rec["error"])
	if c := stringProperty(nested, "code"); c != "" {
		return c
	}
	return stringProperty(rec, "code")
}

func standardResponseBodyMessage(body any) string {
	rec, ok := asRecord(body)
	if !ok {
		return ""
	}
	nested, _ := asRecord(rec["error"])
	if m := stringProperty(nested, "message"); m != "" {
		return m
	}
	return stringProperty(rec, "message")
}

// ParseRetryAfterMs ports parseRetryAfterMs. now is injected for the timestamp
// form of Retry-After (HTTP date); pass time.Now().UTC() in production.
