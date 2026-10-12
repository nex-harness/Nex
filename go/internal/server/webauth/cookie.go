// Package webauth ports the web token login of packages/server/src/webAuth.ts.
//
// Scope is step 2.6 of docs/specs/go-rewrite/phase-2-go-server.md. The rules, state owner and
// failure semantics are docs/specs/web-token-login.md: web-sessions.json under the app config
// dir is the single owner of "who is logged in", the browser only ever holds a random session
// id in the nex_session cookie, and the token itself is never written to disk or to a cookie.
//
// Files, mirroring the TypeScript split:
//   - cookie.go     cookie attributes and the request-parsing helpers
//   - limiter.go    the per-address login rate limiter (in-memory, injectable clock)
//   - store.go      web-sessions.json: read, validate, atomic write
//   - middleware.go the pure decide(req) Decision table
//   - routes.go     GET /api/auth/session, POST /api/auth/login, POST /api/auth/logout
//
// The middleware runs before every other route, including the static shell, exactly like
// `app.use("*", webAuth.middleware())` in packages/server/src/http.ts. Protected paths are
// /ws, /ws/* and /api/* except /api/auth/*.
package webauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CookieName is SESSION_COOKIE_NAME in packages/server/src/webAuth.ts. The old
// `nex_lite_token` cookie carried the raw token and is deliberately no longer accepted
// (docs/specs/web-token-login.md, "The old nex_lite_token cookie is no longer accepted").
const CookieName = "nex_session"

// SessionTTL is SESSION_TTL_MS (30 days). Session lifetime is absolute: no sliding renewal.
// The cookie's Max-Age is SessionTTL/1000, the number the TypeScript prints.
const SessionTTL = 30 * 24 * 60 * 60 * 1000

// MaxTokenLength is MAX_TOKEN_LENGTH in packages/server/src/webAuth.ts. A longer `token`
// in the login body is rejected without a hash comparison.
const MaxTokenLength = 1024

// maxStoredSessions is MAX_STORED_SESSIONS: after pruning, at most this many sessions are kept.
const maxStoredSessions = 100

// sessionsFileVersion is SESSIONS_FILE_VERSION. A file with any other version is discarded.
const sessionsFileVersion = 1

// tokensEqual ports `tokensEqual`: both sides are hashed with SHA-256 and compared with a
// constant-time comparison, so neither the length nor the content of the expected token leaks.
// The digests are always 32 bytes, which is what makes subtle.ConstantTimeCompare safe here —
// it panics on a length mismatch, and hashing first removes the possibility of one.
func tokensEqual(expected, provided string) bool {
	a := sha256.Sum256([]byte(expected))
	b := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// tokenFingerprint ports `tokenFingerprint`: sha256("nex-web-session:" + token), hex. It is
// stored in web-sessions.json so a rotated token invalidates every stored session.
func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte("nex-web-session:" + token))
	return hexLower(sum[:])
}

// sessionIDLength is the 32 random bytes behind a session id before base64url encoding.
const sessionIDLength = 32

// newSessionID ports `randomBytes(32).toString("base64url")`. The id is only ever returned to
// the client; what lands on disk is its SHA-256.
func newSessionID() (string, error) {
	raw := make([]byte, sessionIDLength)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// isWebSocketPath ports `isWebSocketPath`.
func isWebSocketPath(pathname string) bool {
	return pathname == "/ws" || strings.HasPrefix(pathname, "/ws/")
}

// isAuthRoute ports `isAuthRoute`: /api/auth and everything under it.
func isAuthRoute(pathname string) bool {
	return pathname == "/api/auth" || strings.HasPrefix(pathname, "/api/auth/")
}

// IsTokenProtectedPath ports `isTokenProtectedPath`, which is also what gates the SPA fallback
// in packages/server/src/http.ts (isStaticFallbackAllowed). WebSocket and /api/* paths need a
// session; /api/auth/* is the login surface itself and stays reachable without one.
func IsTokenProtectedPath(pathname string) bool {
	if isAuthRoute(pathname) {
		return false
	}
	return isWebSocketPath(pathname) || strings.HasPrefix(pathname, "/api/")
}

// isSecureRequest ports `isSecureRequest`: the request arrived over TLS, or a proxy in front
// of the server said so with X-Forwarded-Proto. Only then does the cookie get `Secure`.
//
// c.req.url's protocol in Node is the socket scheme (http: unless the socket is encrypted),
// so the Go equivalent is TLS state, not r.TLS != nil on the request: the URL is rebuilt from
// the socket in @hono/node-server's newRequest. A reverse proxy terminating TLS and forwarding
// plain HTTP is the X-Forwarded-Proto case.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		return false
	}
	first, _, _ := strings.Cut(proto, ",")
	return strings.TrimSpace(first) == "https"
}

// sessionCookie ports `sessionCookie`: Path, HttpOnly, SameSite=Lax and a 30-day Max-Age,
// plus Secure for a TLS request. The value is percent-encoded, matching encodeURIComponent.
func sessionCookie(secure bool, sessionID string) string {
	return joinCookie(CookieName+"="+encodeURIComponent(sessionID), SessionTTL/1000, secure)
}

// clearedSessionCookie ports `clearedSessionCookie`: the same attributes with an empty value
// and Max-Age=0, so the browser drops the cookie on logout.
func clearedSessionCookie(secure bool) string {
	return joinCookie(CookieName+"=", 0, secure)
}

// joinCookie builds `nex_session=<value>; Path=/; HttpOnly; SameSite=Lax; Max-Age=<maxAge>` and
// appends Secure when the request was secure. The attribute order is fixed by the TypeScript
// literal and is part of the observable response.
func joinCookie(value string, maxAgeSeconds int, secure bool) string {
	attributes := []string{
		value,
		"Path=/",
		"HttpOnly",
		"SameSite=Lax",
		"Max-Age=" + strconv.Itoa(maxAgeSeconds),
	}
	if secure {
		attributes = append(attributes, "Secure")
	}
	return strings.Join(attributes, "; ")
}

// encodeURIComponent ports encodeURIComponent for the session id. base64url only contains
// [A-Za-z0-9_-], so this is the identity for every value this package produces; it exists so
// the cookie bytes stay correct if that ever changes.
func encodeURIComponent(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case strings.IndexByte("-_.!~*'()", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteString("%")
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&0x0f])
		}
	}
	return b.String()
}

const hexUpper = "0123456789ABCDEF"

// hexLower is Node's createHash().digest("hex"): lowercase, two chars per byte.
func hexLower(sum []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, len(sum)*2)
	for _, b := range sum {
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out)
}

// cookieValue ports `parseCookieHeader` and returns one named cookie.
//
// The TypeScript splits on ";", takes the first "=" in each part, trims the name and value, and
// decodes the value with decodeURIComponent (falling back to the raw value on a bad escape).
// Only the session cookie is ever looked up, so this returns that one value and does not build
// a map — but it keeps the same trim, first-"=" split and decode semantics.
func cookieValue(header, name string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ";") {
		separator := strings.Index(part, "=")
		if separator <= 0 {
			continue
		}
		cookieName := strings.TrimSpace(part[:separator])
		if cookieName != name {
			continue
		}
		value := strings.TrimSpace(part[separator+1:])
		// decodeURIComponent throws on a malformed escape; the TypeScript catches and keeps the
		// raw value, which can then only fail validation, so an invalid escape yields the raw
		// string here.
		decoded, err := decodeURIColponentJS(value)
		if err != nil {
			return value
		}
		return decoded
	}
	return ""
}

// decodeURIColponentJS decodes percent-escapes the way JS's decodeURIComponent does: escapes
// resolve to bytes, the byte run must be valid UTF-8, and anything malformed is an error
// (URIError). Only the cookie value needs it, and only a failure matters, which the caller
// turns back into "use the raw value".
func decodeURIColponentJS(input string) (string, error) {
	if !strings.Contains(input, "%") {
		return input, nil
	}
	var out strings.Builder
	for i := 0; i < len(input); {
		if input[i] != '%' {
			out.WriteByte(input[i])
			i++
			continue
		}
		if i+2 >= len(input) {
			return "", errURIMalformed
		}
		high, ok1 := hexVal(input[i+1])
		low, ok2 := hexVal(input[i+2])
		if !ok1 || !ok2 {
			return "", errURIMalformed
		}
		out.WriteByte(high<<4 | low)
		i += 3
	}
	// decodeURIComponent additionally rejects percent-encoded bytes that are not valid UTF-8
	// (including lone surrogates in the CESU-8 spelling). Since out was written byte by byte
	// from a Go string, the encoded bytes are the string's own bytes; validate them.
	if !utf8.ValidString(out.String()) {
		return "", errURIMalformed
	}
	return out.String(), nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// errURIMalformed stands in for JS's URIError. Its text matches V8 so a log line reads the same
// in both stacks; cookie.go only ever returns it, never prints it.
var errURIMalformed = errors.New("URIError: URI malformed")
