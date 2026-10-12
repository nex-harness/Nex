package webauth

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Routes are the three auth endpoints of packages/server/src/webAuth.ts mountRoutes(). All of
// them answer Cache-Control: no-store, because the session state must never be cached by a
// browser or a proxy, and all three live under /api/auth/*, which IsTokenProtectedPath exempts
// from the middleware — otherwise the login page itself could not reach them.
const (
	sessionPath = "/api/auth/session"
	loginPath   = "/api/auth/login"
	logoutPath  = "/api/auth/logout"
)

// Routes mounts the three endpoints on a mux-like dispatcher. It is deliberately not an
// http.ServeMux: the /api/* namespace is dispatched by exact path inside internal/server so a
// method mismatch answers Hono's 404 rather than Go's 405, and so the auth routes share that
// dispatcher with every other /api/* route.
type Routes struct {
	token   string
	store   *SessionStore
	limiter *loginRateLimiter
	enabled bool
	onWarn  func(string, error)
}

// NewRoutes builds the route set for token. A blank token still mounts them (the TypeScript
// calls mountRoutes unconditionally); GET /api/auth/session reports authRequired:false and
// POST /api/auth/login answers 404 "Authentication is not enabled".
func NewRoutes(token string, store *SessionStore, limiter *loginRateLimiter, onWarn func(string, error)) *Routes {
	if store == nil {
		store = NewSessionStore(token, "", nil, nil)
	}
	if limiter == nil {
		limiter = newLoginRateLimiter(nil)
	}
	token = strings.TrimSpace(token)
	return &Routes{token: token, store: store, limiter: limiter, enabled: token != "", onWarn: onWarn}
}

// Handled reports whether path is one of the three auth routes. internal/server calls this
// before routing an /api/* request, so an unowned /api/* path still reaches the upstream.
func (r *Routes) Handled(pathname string) bool {
	return pathname == sessionPath || pathname == loginPath || pathname == logoutPath
}

// Serve dispatches one auth route. next is called for any path/method the routes do not own,
// which keeps the dispatcher's fall-through identical to Hono's.
func (r *Routes) Serve(w http.ResponseWriter, request *http.Request, next http.Handler) {
	switch requestPathname(request) {
	case sessionPath:
		if request.Method != http.MethodGet {
			next.ServeHTTP(w, request)
			return
		}
		r.session(w, request)
	case loginPath:
		if request.Method != http.MethodPost {
			next.ServeHTTP(w, request)
			return
		}
		r.login(w, request)
	case logoutPath:
		if request.Method != http.MethodPost {
			next.ServeHTTP(w, request)
			return
		}
		r.logout(w, request)
	default:
		next.ServeHTTP(w, request)
	}
}

// session ports GET /api/auth/session: always 200, reporting whether auth is configured and
// whether the caller's cookie is a live session. The web client uses it to decide between the
// app and the login page (packages/web/src/webAuthSession.ts).
func (r *Routes) session(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, sessionState{
		AuthRequired:  r.enabled,
		Authenticated: r.hasValidSession(request),
	})
}

// sessionState is the GET /api/auth/session body. The field order is the TypeScript object
// literal's, and it is a struct rather than a map because Go randomises map key order while the
// client parses by name — a stable body keeps the recorded responses byte-identical.
type sessionState struct {
	AuthRequired  bool `json:"authRequired"`
	Authenticated bool `json:"authenticated"`
}

// login ports POST /api/auth/login. Order of checks is the TypeScript's and matters: the rate
// limit is consulted before the body is even read, so a locked address cannot use login as an
// oracle, and a success clears the counter.
//
//	auth not configured -> 404
//	address locked      -> 429 + Retry-After (seconds, rounded up)
//	body not JSON       -> 400
//	token not a string or empty -> 400
//	token too long or wrong    -> 401 (+ record failure)
//	correct                   -> 200 + Set-Cookie (+ clear failures)
func (r *Routes) login(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !r.enabled {
		writeJSONError(w, http.StatusNotFound, "Authentication is not enabled")
		return
	}
	address := remoteAddressKey(request)
	if locked := r.limiter.lockedFor(address); locked > 0 {
		// Math.ceil(lockedMs / 1000): round the remaining lock up to whole seconds, which is how
		// long the client is expected to wait before retrying.
		retryAfter := int64((locked + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
		writeJSONError(w, http.StatusTooManyRequests, "Too many attempts")
		return
	}
	provided, ok := loginTokenFromBody(request)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	// The length check comes first so an oversized value is rejected without a hash comparison,
	// then the comparison is constant time and on the trimmed value.
	if len(provided) > MaxTokenLength || !tokensEqual(r.configuredToken(), strings.TrimSpace(provided)) {
		r.limiter.recordFailure(address)
		writeJSONError(w, http.StatusUnauthorized, "Invalid token")
		return
	}
	r.limiter.recordSuccess(address)
	sessionID, err := r.store.Create()
	if err != nil {
		// The TypeScript answers 500 here: the session is only created once it is persisted, so
		// a failed write must not hand out a cookie that dies on restart.
		if r.onWarn != nil {
			r.onWarn("[web-auth] failed to create session", err)
		}
		writeJSONError(w, http.StatusInternalServerError, "Failed to create session")
		return
	}
	w.Header().Set("Set-Cookie", sessionCookie(isSecureRequest(request), sessionID))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// logout ports POST /api/auth/logout: drop the session, clear the cookie, always 200 — even
// when auth is not configured or the store cannot be written (the TypeScript logs the persist
// failure and still answers ok). Revoking a session that is already gone is not an error.
func (r *Routes) logout(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.store.Revoke(cookieValue(request.Header.Get("Cookie"), CookieName))
	w.Header().Set("Set-Cookie", clearedSessionCookie(isSecureRequest(request)))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// hasValidSession is `hasValidSession`: when auth is off every request counts as authenticated.
func (r *Routes) hasValidSession(request *http.Request) bool {
	if !r.enabled {
		return true
	}
	return r.store.Validate(cookieValue(request.Header.Get("Cookie"), CookieName))
}

// configuredToken is the trimmed NEX_SERVER_AUTH_TOKEN this Routes compares against.
func (r *Routes) configuredToken() string { return r.token }

// loginTokenFromBody parses the JSON body `{ token }`. It returns ok=false for a body that is
// not JSON, or JSON whose `token` is absent, not a string, or empty — all the TypeScript's 400
// cases (a thrown JSON.parse, or a typeof/length check on the value). The body is bounded so a
// huge Content-Length cannot be buffered.
func loginTokenFromBody(request *http.Request) (string, bool) {
	const maxBodyBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBodyBytes))
	if err != nil {
		return "", false
	}
	var payload struct {
		Token *string `json:"token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	if payload.Token == nil || *payload.Token == "" {
		return "", false
	}
	return *payload.Token, true
}

// writeJSONError writes the `{"error":"..."}` body Hono's c.json({ error }) produces.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeJSON emits a JSON body with the Content-Type and Content-Length Hono sets.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
