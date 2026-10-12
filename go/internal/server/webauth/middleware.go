package webauth

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Action is what the middleware decided for one request. It is a small enum so the decision
// table can be unit-tested without a ResponseWriter or a live store.
type Action int

const (
	// ActionPass hands the request to the next handler unchanged.
	ActionPass Action = iota
	// ActionMintSession passes the request and, after the handler runs, sets a fresh session
	// cookie (the `?token=` on a page path).
	ActionMintSession
	// ActionUnauthorized is the 401 JSON the TypeScript answers for a protected path without
	// credentials, or with a wrong token.
	ActionUnauthorized
	// ActionTooManyAttempts is the 429 JSON, with Retry-After when the caller wants it.
	ActionTooManyAttempts
)

// Decision is the pure result of decide(). It carries only what the middleware needs to act;
// nothing here touches the network or the filesystem.
type Decision struct {
	Action Action
	// SetCookie is the Set-Cookie header value to attach on ActionPass/ActionMintSession,
	// or "" when none is to be set.
	SetCookie string
	// RetryAfterSeconds is written on ActionTooManyAttempts for /api/auth/login. The middleware's
	// own 429 (a `?token=` query while locked) does not set it in the TypeScript, so the field
	// is filled by the route, not by decide().
	RetryAfterSeconds int
}

// Request is the input to decide(): everything the decision depends on, already extracted from
// the *http.Request. Keeping it a plain struct — with the configured token included — is what
// makes the table testable in isolation; decide performs no I/O.
type Request struct {
	// Enabled mirrors webAuth.enabled: false means no token configured, so nothing is protected
	// and every request passes.
	Enabled bool
	// Token is the configured access token (NEX_SERVER_AUTH_TOKEN). decide compares the query
	// value against it in constant time.
	Token string
	// Pathname is `new URL(c.req.url).pathname` — the escaped, dot-segment-resolved path.
	Pathname string
	// QueryToken is `url.searchParams.get("token")`, with HasQueryToken distinguishing an absent
	// parameter from an empty one: the TypeScript branches on `queryToken !== null`, so a present
	// but empty `?token=` takes the "wrong token" path.
	QueryToken    string
	HasQueryToken bool
	// HasValidSession is the already-resolved store.validate(cookie) result.
	HasValidSession bool
	// Locked is "the address is currently rate-limited" (limiter.lockedForMs > 0).
	Locked bool
	// Secure is isSecureRequest(r), used only to build the cookie attributes.
	Secure bool
}

// decide is the pure core of the auth middleware, ported from the `middleware()` closure in
// packages/server/src/webAuth.ts. It reproduces that control flow exactly, in order:
//
//	no token configured            -> pass everything
//	?token= present:
//	    address locked             -> 429
//	    token correct              -> pass; mint a session cookie only for non-protected,
//	                                 non-WebSocket pages; record success
//	    token wrong                -> record failure; 401 if protected, else pass (the login
//	                                 page still renders)
//	no ?token=:
//	    non-protected path         -> pass
//	protected path + valid cookie  -> pass
//	protected path otherwise       -> 401
//
// The session-store side effects (create the session, record a limiter outcome) happen in the
// caller, which is why they are described on Decision.Action rather than performed here.
func decide(req Request) Decision {
	if !req.Enabled {
		return Decision{Action: ActionPass}
	}

	protected := IsTokenProtectedPath(req.Pathname)
	wsPath := isWebSocketPath(req.Pathname)

	if req.HasQueryToken {
		if req.Locked {
			return Decision{Action: ActionTooManyAttempts}
		}
		if tokensEqual(req.Token, req.QueryToken) {
			// Mint only for page-like requests: programmatic clients put ?token= on /ws and on
			// /api/* on every call, and handing each of those a fresh session would grow the
			// store and set a cookie nobody reads.
			if !protected && !wsPath {
				return Decision{Action: ActionMintSession}
			}
			return Decision{Action: ActionPass}
		}
		// Wrong token: a protected path is refused outright, but a page still renders so the
		// user lands on the login form (docs/specs/web-token-login.md acceptance 1).
		if protected {
			return Decision{Action: ActionUnauthorized}
		}
		return Decision{Action: ActionPass}
	}

	if !protected {
		return Decision{Action: ActionPass}
	}
	if req.HasValidSession {
		return Decision{Action: ActionPass}
	}
	return Decision{Action: ActionUnauthorized}
}

// Middleware ports `createWebAuth().middleware()` as net/http middleware. It wires decide() to
// the store and the limiter, then renders the possible responses.
type Middleware struct {
	token   string
	store   *SessionStore
	limiter *loginRateLimiter
	onWarn  func(string, error)
}

// NewMiddleware builds the middleware for token. A blank token yields Enabled()==false, which
// is what `createWebAuth` reports and what packages/server/src/http.ts branches on before
// registering the middleware at all. A nil store or limiter is filled with an in-memory pair.
func NewMiddleware(token string, store *SessionStore, limiter *loginRateLimiter, onWarn func(string, error)) *Middleware {
	token = strings.TrimSpace(token)
	if store == nil {
		store = NewSessionStore(token, "", nil, nil)
	}
	if limiter == nil {
		limiter = newLoginRateLimiter(nil)
	}
	return &Middleware{token: token, store: store, limiter: limiter, onWarn: onWarn}
}

// Enabled mirrors webAuth.enabled.
func (m *Middleware) Enabled() bool { return m.token != "" }

// Wrap returns the handler that runs the auth decision and, on a pass, calls next. This mirrors
// `app.use("*", webAuth.middleware())` sitting in front of every route: packages/server/src/http.ts
// registers it before /api/server-info, the static handler and the WebSocket endpoints.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.serve(next, w, r)
	})
}

func (m *Middleware) serve(next http.Handler, w http.ResponseWriter, r *http.Request) {
	if !m.Enabled() {
		next.ServeHTTP(w, r)
		return
	}
	req := m.buildRequest(r)
	switch decide(req).Action {
	case ActionTooManyAttempts:
		// The middleware's own 429 carries no Retry-After in the TypeScript (only the login
		// route sets it), so this response matches exactly.
		writeJSONError(w, http.StatusTooManyRequests, "Too many attempts")
		return
	case ActionUnauthorized:
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	case ActionMintSession:
		m.mintSession(next, w, r, req.Secure)
	default:
		next.ServeHTTP(w, r)
	}
}

// mintSession passes the request through and then attaches a freshly created session cookie. The
// limiter records the success and the store writes web-sessions.json, both ported from the
// TypeScript branch; a failed write is logged and the page is still served (the TypeScript
// catches and warns here too).
func (m *Middleware) mintSession(next http.Handler, w http.ResponseWriter, r *http.Request, secure bool) {
	m.limiter.recordSuccess(remoteAddressKey(r))
	// The response header has to be in place before the body is flushed, so the session is
	// created *before* next() runs. The observable order is identical (the TypeScript also
	// produces one response with one Set-Cookie), and it avoids buffering the page body.
	id, err := m.store.Create()
	if err != nil {
		m.warn("failed to create session from token query", err)
		next.ServeHTTP(w, r)
		return
	}
	w.Header().Add("Set-Cookie", sessionCookie(secure, id))
	next.ServeHTTP(w, r)
}

// warn forwards a non-fatal diagnostic to the logger supplied at construction.
func (m *Middleware) warn(message string, err error) {
	if m.onWarn != nil {
		m.onWarn("[web-auth] "+message, err)
	}
}

// buildRequest extracts the fields decide() reads, in the same order the TypeScript reads them.
func (m *Middleware) buildRequest(r *http.Request) Request {
	address := remoteAddressKey(r)
	queryToken, hasQueryToken := queryParamToken(r.URL)
	return Request{
		Enabled:         true,
		Token:           m.token,
		Pathname:        requestPathname(r),
		QueryToken:      queryToken,
		HasQueryToken:   hasQueryToken,
		HasValidSession: m.store.Validate(cookieValue(r.Header.Get("Cookie"), CookieName)),
		Locked:          m.limiter.lockedFor(address) > 0,
		Secure:          isSecureRequest(r),
	}
}

// requestPathname is `new URL(c.req.url).pathname`: the request target with WHATWG dot-segment
// resolution applied, and still percent-encoded (the pathname is *not* decoded; the TypeScript
// reads searchParams and the pathname separately and only decodes inside the static handler).
//
// The resolver is duplicated from internal/server/urlpath.go rather than imported, because
// webauth is a leaf package that internal/server will wrap — importing the parent would create
// a cycle the moment the shell registers the auth middleware.
func requestPathname(r *http.Request) string {
	escaped := r.URL.EscapedPath()
	if escaped == "" {
		return "/"
	}
	return removeDotSegments(escaped)
}

// queryParamToken is `url.searchParams.get("token")` plus the `!== null` distinction. Go's
// url.Values has the same "absent vs present-empty" split through the second return value.
func queryParamToken(u *url.URL) (string, bool) {
	if u == nil {
		return "", false
	}
	values, ok := u.Query()["token"]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}

// remoteAddressKey ports `remoteAddressKey`: the connection's remote address, never a
// forwarded header. getConnInfo reads socket.remoteAddress and falls back to "unknown" when
// there is no socket (which is the app.request test case). X-Forwarded-For is deliberately
// ignored so a client cannot rotate its rate-limit bucket by sending a header.
func remoteAddressKey(r *http.Request) string {
	if r.RemoteAddr == "" {
		return "unknown"
	}
	// r.RemoteAddr is "host:port" (or a bare path for a unix socket); the limiter key is the
	// host, matching connInfo.remote.address.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

// removeDotSegments is the WHATWG URL path dot-segment removal, duplicated from
// internal/server/urlpath.go so webauth stays a leaf (see requestPathname). It keeps the path
// percent-encoded and compares segments after percent-decoding, matching the parser.
func removeDotSegments(escaped string) string {
	// Fast path: nothing to collapse.
	if !strings.ContainsAny(escaped, "%") && !strings.Contains(escaped, ".") {
		return escaped
	}
	trailingSlash := strings.HasSuffix(escaped, "/")
	segments := strings.Split(escaped, "/")
	out := make([]string, 0, len(segments))
	out = append(out, segments[0])
	for _, segment := range segments[1:] {
		switch {
		case isSingleDotSegment(segment):
			trailingSlash = true
		case isDoubleDotSegment(segment):
			if len(out) > 1 {
				out = out[:len(out)-1]
			}
			trailingSlash = true
		default:
			out = append(out, segment)
			trailingSlash = false
		}
	}
	normalized := strings.Join(out, "/")
	if trailingSlash && !strings.HasSuffix(normalized, "/") {
		normalized += "/"
	}
	return normalized
}

func isSingleDotSegment(segment string) bool {
	return segment == "." || isEncodedDot(segment) || isMixedDot(segment)
}

func isDoubleDotSegment(segment string) bool {
	if segment == ".." {
		return true
	}
	if isEncodedDot(segment) {
		return false
	}
	if isEncodedDotPair(segment) {
		return true
	}
	return isMixedDot(segment)
}

func isEncodedDot(segment string) bool {
	return len(segment) == 3 && segment[0] == '%' && segment[1] == '2' &&
		(segment[2] == 'e' || segment[2] == 'E')
}

func isEncodedDotPair(segment string) bool {
	return len(segment) == 6 && isEncodedDot(segment[:3]) && isEncodedDot(segment[3:])
}

func isMixedDot(segment string) bool {
	if len(segment) != 4 {
		return false
	}
	if segment[0] == '.' {
		return isEncodedDot(segment[1:])
	}
	return segment[0] == '%' && segment[1] == '2' &&
		(segment[2] == 'e' || segment[2] == 'E') && segment[3] == '.'
}
