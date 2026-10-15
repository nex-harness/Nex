// Package feedbackprivacy ports packages/shared/src/feedbackPrivacy.ts
// (step 3.8): the redaction used for user feedback diagnostics ONLY — it must
// never be applied to model requests or authentication payloads.
//
// The redaction has to handle three input shapes at once (the TS comment on
// redactText): whole JSON documents, mixed log lines with embedded JSON
// fragments, and plain key:value text. The Go port keeps the same layered
// structure:
//
//	values   — the regex sweeps (auth headers, private keys, bearer tokens,
//	           key=value pairs, URLs, user paths, emails)
//	plain    — per-line field checks that drop unsafe lines whole
//	fragment — JSON fragment scanning inside mixed text
//	scrub    — recursive JSON object/array scrubbing by key name
package feedbackprivacy

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
)

// Redacted is the replacement marker.
const Redacted = "[REDACTED]"

var (
	lineSplitRe    = regexp.MustCompile(`\r?\n`)
	sensitiveKeyRe = regexp.MustCompile(`(?i)(?:password|passwd|passphrase|secret|token|apikey|accesskey|privatekey|authorization|cookie|credential)`)
	// diagnosticBodyKeyRe matches the whole key (already normalized).
	diagnosticBodyKeyRe = regexp.MustCompile(`(?i)^(?:content|messages?|prompt|systemprompt|request|response|body|payload|input|output|toolinput|tooloutput|arguments|args|env|environment|headers|text|completion|result|stdout|stderr|data|params)$`)
	keyNormalizeRe      = regexp.MustCompile(`(?i)[^a-z0-9]`)
	fieldKeysRe         = regexp.MustCompile(`(?:"((?:\\.|[^"\\])*)"|'([^']*)'|([\w.-]+))\s*[:=]\s*`)
	urlRe               = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>\\]+`)
	userPathRe          = regexp.MustCompile(`(?i)(?:/(?:Users|home)/|[a-z]:\\Users\\)[^\s"'<>]+`)
	emailRe             = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	authHeaderRe        = regexp.MustCompile(`(?i)\b((?:Proxy-)?Authorization|Cookie|Set-Cookie)\s*:\s*[^\r\n]+`)
	privateKeyRe        = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`)
	bearerRe            = regexp.MustCompile(`(?i)\bBearer\s+[^\s"'\\,;]+`)
	keyValueRe          = regexp.MustCompile(`([\w.-]+)(\s*[=:]\s*)(?:"(?:\\.|[^"\\])*"|'[^']*'|[^\s,;"'<>]+)`)
	// URL-path segments that carry credentials or signed links.
	urlPathSegmentRe = regexp.MustCompile(`(?i)^(?:webhook\w*|(?:password)?reset(?:password)?|invites?|invitations?|callback|downloads?|signed|verify|verification|activate|magiclink)$`)
	urlQuerySigRe    = regexp.MustCompile(`(?i)^(?:.*signature|sig|code)$`)
)

// normalizeKey strips non-alphanumerics, exactly like the TS.
func normalizeKey(key string) string {
	return keyNormalizeRe.ReplaceAllString(strings.ToLower(key), "")
}

func shouldRedactKey(key string, diagnostic bool) bool {
	normalized := normalizeKey(key)
	if sensitiveKeyRe.MatchString(normalized) {
		return true
	}
	return diagnostic && diagnosticBodyKeyRe.MatchString(normalized)
}

// fieldKey mirrors one fieldKeys() match.
type fieldKey struct {
	key    string
	quoted bool
	end    int
}

// fieldKeys finds `key:` / `key=` occurrences in a line (JSON-quoted, single
// quoted, or bare).
func fieldKeys(text string) []fieldKey {
	var fields []fieldKey
	for _, loc := range fieldKeysRe.FindAllStringSubmatchIndex(text, -1) {
		key := ""
		quoted := false
		switch {
		case loc[2] >= 0: // double-quoted (may contain escapes)
			quoted = true
			raw := text[loc[2]:loc[3]]
			var decoded string
			if err := json.Unmarshal([]byte(`"`+raw+`"`), &decoded); err == nil {
				key = decoded
			} else {
				key = raw
			}
		case loc[4] >= 0: // single-quoted
			quoted = true
			key = text[loc[4]:loc[5]]
		default:
			key = text[loc[6]:loc[7]]
		}
		fields = append(fields, fieldKey{key: key, quoted: quoted, end: loc[1]})
	}
	return fields
}

// shouldRedactURLPath ports shouldRedactUrlPath: userinfo, known webhook
// hosts, credential-looking query keys, and credential-looking path segments.
func shouldRedactURLPath(u *url.URL, diagnostic bool) bool {
	if diagnostic || u.User != nil {
		return true
	}
	if u.User.String() != "" {
		return true
	}
	if u.Host == "hooks.slack.com" {
		return true
	}
	for key := range u.Query() {
		if shouldRedactKey(key, false) || urlQuerySigRe.MatchString(normalizeKey(key)) {
			return true
		}
	}
	decoded, err := url.PathUnescape(u.Path)
	if err != nil {
		// 无法解码的路径不能证明安全，避免编码形式绕过识别。
		return true
	}
	for _, segment := range strings.Split(decoded, "/") {
		normalized := normalizeKey(segment)
		if sensitiveKeyRe.MatchString(normalized) ||
			urlPathSegmentRe.MatchString(strings.ToLower(normalized)) {
			return true
		}
	}
	return false
}

// redactValues ports redactValues: the regex sweeps over the text.
func redactValues(text string, diagnostic bool) string {
	result := authHeaderRe.ReplaceAllString(text, `$1: `+Redacted)
	result = privateKeyRe.ReplaceAllString(result, Redacted)
	result = bearerRe.ReplaceAllString(result, "Bearer "+Redacted)
	result = keyValueRe.ReplaceAllStringFunc(result, func(match string) string {
		sub := keyValueRe.FindStringSubmatch(match)
		if shouldRedactKey(sub[1], false) {
			return sub[1] + sub[2] + Redacted
		}
		return match
	})
	result = urlRe.ReplaceAllStringFunc(result, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return Redacted
		}
		// URL 凭据并不只在 userinfo/query 中，webhook 和重置链接常将秘密放在路径里。
		if shouldRedactURLPath(u, diagnostic) && u.Path != "" && u.Path != "/" {
			// u.String() percent-encodes Path through EscapedPath; the TS
			// assigns the raw string and serializes it verbatim, so set the
			// escaped form directly to keep the brackets literal.
			u.RawPath = "/" + Redacted
			u.Path = "/" + Redacted
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		u.RawFragment = ""
		return u.String()
	})
	result = userPathRe.ReplaceAllString(result, "[USER_PATH]")
	if diagnostic {
		result = emailRe.ReplaceAllString(result, "[EMAIL]")
	}
	return result
}

// scrubValue ports scrubValue: recursive JSON value scrub.
func scrubValue(value any, diagnostic bool, depth int) any {
	if depth > 32 {
		return Redacted
	}
	switch v := value.(type) {
	case string:
		// 字符串也可能包含日志前缀或多个 JSON 片段，必须走同一条完整脱敏路径。
		return redactText(v, diagnostic, depth+1)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, scrubValue(item, diagnostic, depth+1))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if shouldRedactKey(key, diagnostic) {
				out[key] = Redacted
			} else {
				out[key] = scrubValue(item, diagnostic, depth+1)
			}
		}
		return out
	default:
		return value
	}
}

// redactPlainText ports redactPlainText: value sweeps + per-line unsafe-line
// dropping.
func redactPlainText(text string, diagnostic bool) string {
	swept := redactValues(text, diagnostic)
	lines := lineSplitRe.Split(swept, -1)
	for i, line := range lines {
		unsafe := false
		for _, field := range fieldKeys(line) {
			if !shouldRedactKey(field.key, diagnostic) {
				continue
			}
			// 非结构化正文的长度和换行边界不可靠；已知敏感字段无法安全解析时舍弃该行。
			if field.quoted || diagnostic || !strings.HasPrefix(line[field.end:], Redacted) {
				unsafe = true
				break
			}
		}
		if unsafe {
			lines[i] = Redacted
		}
	}
	return strings.Join(lines, "\n")
}

// jsonFragmentEnd ports jsonFragmentEnd: the end index of the balanced
// fragment starting at start, or len(text).
func jsonFragmentEnd(text string, start int) int {
	nesting := 0
	quoted := false
	escaped := false
	for index := start; index < len(text); index++ {
		character := text[index]
		if quoted {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				quoted = false
			}
		} else if character == '"' {
			quoted = true
		} else if character == '{' || character == '[' {
			nesting++
		} else if (character == '}' || character == ']') && nesting > 0 {
			nesting--
			if nesting == 0 {
				return index + 1
			}
		}
	}
	return len(text)
}

// jsonRoundTrip re-serializes a decoded value the way JSON.stringify would
// (used both to detect whole-JSON inputs and to re-encode scrubbed ones).
func jsonRoundTrip(value any) (string, bool) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// decodeJSON decodes with one rule the TS gets for free: json.Unmarshal into
// any produces float64 for numbers — NaN/Infinity never parse, matching the
// finite check.
func decodeJSON(text string) (any, bool) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, false
	}
	return value, true
}

// isJSONContainer reports whether the value is the object/array shape the TS
// branches on.
func isJSONContainer(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// redactText ports redactText: whole JSON → scrubbed re-serialization;
// otherwise mixed-text fragment scanning.
func redactText(text string, diagnostic bool, depth int) string {
	if depth > 32 {
		return Redacted
	}
	if parsed, ok := decodeJSON(text); ok && isJSONContainer(parsed) {
		if out, ok := jsonRoundTrip(scrubValue(parsed, diagnostic, depth+1)); ok {
			return out
		}
	}
	// 私钥和普通凭据可能跨行或含括号，先按整体清洗，避免片段切分后残留值的后半段。
	source := redactValues(text, diagnostic)
	var chunks []string
	cursor := 0
	for index := 0; index < len(source); index++ {
		if strings.HasPrefix(source[index:], Redacted) {
			index += len(Redacted) - 1
			continue
		}
		if source[index] != '{' && source[index] != '[' {
			continue
		}
		prefix := source[cursor:index]
		end := jsonFragmentEnd(source, index)
		linePrefix := prefix[strings.LastIndex(prefix, "\n")+1:]
		sensitiveValue := false
		for _, field := range fieldKeys(linePrefix) {
			if shouldRedactKey(field.key, diagnostic) {
				sensitiveValue = true
				break
			}
		}
		if sensitiveValue {
			// 普通字段值可能由文本和对象混合组成；不能只删对象而留下同行尾部。
			lineEnd := strings.Index(source[end:], "\n")
			if lineEnd < 0 {
				end = len(source)
			} else {
				end += lineEnd
			}
		}
		fragment := source[index:end]
		chunks = append(chunks, redactPlainText(prefix, diagnostic))
		if !sensitiveValue {
			if parsed, ok := decodeJSON(fragment); ok {
				if out, ok := jsonRoundTrip(scrubValue(parsed, diagnostic, depth+1)); ok {
					chunks = append(chunks, out)
					cursor = end
					index = end - 1
					continue
				}
			}
		}
		// 多行或截断对象不能只丢弃键名所在行，否则下一行的值仍会泄露。
		unsafe := sensitiveValue
		if !unsafe {
			for _, field := range fieldKeys(fragment) {
				if shouldRedactKey(field.key, diagnostic) {
					unsafe = true
					break
				}
			}
		}
		if unsafe {
			chunks = append(chunks, Redacted)
		} else {
			chunks = append(chunks, redactPlainText(fragment, diagnostic))
		}
		cursor = end
		index = end - 1
	}
	chunks = append(chunks, redactPlainText(source[cursor:], diagnostic))
	return strings.Join(chunks, "")
}

// RedactFeedbackText ports redactFeedbackText.
func RedactFeedbackText(text string, diagnostic bool) string {
	return redactText(text, diagnostic, 0)
}

// isValidUTF16JS checks for lone surrogates the way the TS assertValidUnicode
// does (Go strings are UTF-8; a lone surrogate round-trips through
// utf8.RuneError from DecodeRune, which we approximate by re-encoding).
func isValidUTF16JS(s string) bool {
	for _, r := range s {
		if utf16.IsSurrogate(r) {
			return false
		}
	}
	return true
}

// sortedKeys is a test/export helper for deterministic map iteration.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var _ = sortedKeys
