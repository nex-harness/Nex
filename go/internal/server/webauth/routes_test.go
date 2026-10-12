package webauth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestServer wires the middleware and the routes around a fixed store, the way
// packages/server/src/http.ts registers them: `app.use("*", webAuth.middleware())` in front of
// `webAuth.mountRoutes(app)`. The final handler answers 200 with a known body so "passed through"
// is observable.
func newTestServer(t *testing.T, token string) (*httptest.Server, *SessionStore, *loginRateLimiter) {
	t.Helper()
	path := filepath.Join(t.TempDir(), SessionsFileName)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := NewSessionStore(token, path, func() time.Time { return now }, nil)
	limiter := newLoginRateLimiter(func() time.Time { return now })

	middleware := NewMiddleware(token, store, limiter, nil)
	routes := NewRoutes(token, store, limiter, nil)

	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>app</html>"))
	})
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if routes.Handled(requestPathname(r)) {
			routes.Serve(w, r, terminal)
			return
		}
		terminal.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, store, limiter
}

// get issues a request against the test server and returns the response.
func get(t *testing.T, server *httptest.Server, path string, headers map[string]string, cookie string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// TestProtectedPathRequiresASession is spec acceptance 4: /api/* answers 401 without a session
// and 200 with ?token=<token>.
func TestProtectedPathRequiresASession(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)

	response := get(t, server, "/api/anything", nil, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("protected path without a session: %d, want 401", response.StatusCode)
	}

	response = get(t, server, "/api/anything?token="+testToken, nil, "")
	if response.StatusCode != http.StatusOK {
		t.Errorf("protected path with ?token=: %d, want 200", response.StatusCode)
	}
	// A correct ?token= on an /api/* path must NOT mint a session cookie.
	if cookies := response.Header.Values("Set-Cookie"); len(cookies) != 0 {
		t.Errorf("protected path with ?token= set a cookie: %v", cookies)
	}
}

// TestPageIsAlwaysReachable is the other half of the spec: an unprotected page is served whether
// or not a session exists, so the browser can load the shell and reach /login.
func TestPageIsAlwaysReachable(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	for _, path := range []string{"/", "/dashboard", "/assets/app.js", "/api/auth/session"} {
		response := get(t, server, path, nil, "")
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s without a session: %d, want 200", path, response.StatusCode)
		}
	}
}

// TestGoodTokenOnPageMintsASessionCookie is spec acceptance 5: `/?token=…` sets the cookie and the
// page itself still renders.
func TestGoodTokenOnPageMintsASessionCookie(t *testing.T) {
	server, store, _ := newTestServer(t, testToken)
	response := get(t, server, "/?token="+testToken, nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /?token=: %d, want 200", response.StatusCode)
	}
	cookies := response.Header.Values("Set-Cookie")
	if len(cookies) != 1 {
		t.Fatalf("Set-Cookie count = %d (%v), want 1", len(cookies), cookies)
	}
	id := sessionIDFromCookie(t, cookies[0])
	if !store.Validate(id) {
		t.Fatal("the minted session does not validate in the store")
	}
	// The cookie value alone must be enough to authenticate a protected path afterwards.
	response = get(t, server, "/api/anything", nil, CookieName+"="+id)
	if response.StatusCode != http.StatusOK {
		t.Errorf("protected path with the minted cookie: %d, want 200", response.StatusCode)
	}
}

// TestWebSocketPathWithTokenDoesNotMintASession pins the "programmatic clients pass ?token= on
// /ws on every call" rule: no cookie, and the request passes.
func TestWebSocketPathWithTokenDoesNotMintASession(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	for _, path := range []string{"/ws", "/ws/host", "/ws/remote/abc"} {
		response := get(t, server, path+"?token="+testToken, nil, "")
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s?token=: %d, want 200 (passed through)", path, response.StatusCode)
		}
		if cookies := response.Header.Values("Set-Cookie"); len(cookies) != 0 {
			t.Errorf("GET %s?token= set a cookie: %v", path, cookies)
		}
	}
}

// TestBadTokenOnProtectedPathIs401 and TestBadTokenOnPageStillRenders are the two halves of the
// TypeScript's wrong-token branch.
func TestBadTokenOnProtectedPathIs401(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	for _, path := range []string{"/api/server-info", "/ws", "/ws/host"} {
		response := get(t, server, path+"?token=wrong", nil, "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s?token=wrong: %d, want 401", path, response.StatusCode)
		}
	}
}

func TestBadTokenOnPageStillRenders(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	response := get(t, server, "/?token=wrong", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Errorf("GET /?token=wrong: %d, want 200 so the login page renders", response.StatusCode)
	}
	if cookies := response.Header.Values("Set-Cookie"); len(cookies) != 0 {
		t.Errorf("a wrong token set a cookie: %v", cookies)
	}
}

// TestAuthDisabledPassesEverything is the "no token configured" half of the decision table, at
// the HTTP level: no 401 anywhere and no cookie ever.
func TestAuthDisabledPassesEverything(t *testing.T) {
	server, _, _ := newTestServer(t, "")
	for _, path := range []string{"/api/server-info", "/ws", "/ws/host", "/"} {
		response := get(t, server, path, nil, "")
		if response.StatusCode != http.StatusOK {
			t.Errorf("auth disabled, GET %s: %d, want 200", path, response.StatusCode)
		}
	}
	// POST /api/auth/login answers 404 when auth is not configured, per the TypeScript.
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/auth/login", strings.NewReader(`{"token":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("login with auth disabled: %d, want 404", response.StatusCode)
	}
}

// TestSessionRouteResponse pins GET /api/auth/session: always 200, no-store, and the exact
// two-key JSON body in the TypeScript's key order.
func TestSessionRouteResponse(t *testing.T) {
	server, store, _ := newTestServer(t, testToken)

	// No cookie: required, not authenticated.
	body := readBody(t, get(t, server, "/api/auth/session", nil, ""))
	if body != `{"authRequired":true,"authenticated":false}` {
		t.Errorf("session body = %s", body)
	}

	// With a live cookie: authenticated.
	id, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	response := get(t, server, "/api/auth/session", nil, CookieName+"="+id)
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if body := readBody(t, response); body != `{"authRequired":true,"authenticated":true}` {
		t.Errorf("session body = %s", body)
	}
}

// TestSessionRouteWhenAuthDisabled pins the other shape: authRequired false, authenticated true
// (the TypeScript's hasValidSession returns true when there is no store).
func TestSessionRouteWhenAuthDisabled(t *testing.T) {
	server, _, _ := newTestServer(t, "")
	if body := readBody(t, get(t, server, "/api/auth/session", nil, "")); body != `{"authRequired":false,"authenticated":true}` {
		t.Errorf("session body = %s", body)
	}
}

// TestLoginSuccessAndCookie walks the spec's event order: POST /api/auth/login succeeds, sets the
// cookie, and the cookie then authenticates a protected path.
func TestLoginSuccessAndCookie(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	response := post(t, server, "/api/auth/login", `{"token":"`+testToken+`"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login: %d, want 200 (%s)", response.StatusCode, readBody(t, response))
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if body := readBody(t, response); body != `{"ok":true}` {
		t.Errorf("login body = %s", body)
	}
	cookies := response.Header.Values("Set-Cookie")
	if len(cookies) != 1 {
		t.Fatalf("Set-Cookie count = %d", len(cookies))
	}
	id := sessionIDFromCookie(t, cookies[0])
	response = get(t, server, "/api/anything", nil, CookieName+"="+id)
	if response.StatusCode != http.StatusOK {
		t.Errorf("protected path with the login cookie: %d, want 200", response.StatusCode)
	}
}

// TestLoginAcceptsATrimmedToken pins the `provided.trim()` the TypeScript applies before
// comparing, and that the surrounding whitespace does not leak into the comparison.
func TestLoginAcceptsATrimmedToken(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	response := post(t, server, "/api/auth/login", `{"token":"  `+testToken+`  "}`)
	if response.StatusCode != http.StatusOK {
		t.Errorf("login with a padded token: %d, want 200", response.StatusCode)
	}
}

// TestLoginRejections covers the 400 and 401 branches.
func TestLoginRejections(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"malformed json", `{`, http.StatusBadRequest},
		{"not an object", `"token"`, http.StatusBadRequest},
		{"missing token", `{}`, http.StatusBadRequest},
		{"null token", `{"token":null}`, http.StatusBadRequest},
		{"empty token", `{"token":""}`, http.StatusBadRequest},
		{"numeric token", `{"token":123}`, http.StatusBadRequest},
		{"array token", `{"token":["a"]}`, http.StatusBadRequest},
		{"wrong token", `{"token":"wrong"}`, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := post(t, server, "/api/auth/login", tc.body)
			if response.StatusCode != tc.want {
				t.Errorf("status = %d, want %d (body %s)", response.StatusCode, tc.want, readBody(t, response))
			}
		})
	}
}

// TestLoginRejectsOversizedToken pins MAX_TOKEN_LENGTH: a token longer than 1024 is refused
// without a comparison. A 1024-char token that does not match is still 401, not 400.
func TestLoginRejectsOversizedToken(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	oversized := `{"token":"` + strings.Repeat("x", MaxTokenLength+1) + `"}`
	if got := post(t, server, "/api/auth/login", oversized).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("oversized token: %d, want 401", got)
	}
	atLimit := `{"token":"` + strings.Repeat("x", MaxTokenLength) + `"}`
	if got := post(t, server, "/api/auth/login", atLimit).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("token of exactly MaxTokenLength: %d, want 401", got)
	}
}

// TestLogoutClearsTheSession is the logout contract: always 200, the cookie is emptied with
// Max-Age=0, and the session is dead afterwards.
func TestLogoutClearsTheSession(t *testing.T) {
	server, store, _ := newTestServer(t, testToken)
	id, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	response := postWithCookie(t, server, "/api/auth/logout", "", CookieName+"="+id)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d, want 200", response.StatusCode)
	}
	cookie := response.Header.Get("Set-Cookie")
	if !strings.HasPrefix(cookie, CookieName+"=;") || !strings.Contains(cookie, "Max-Age=0") {
		t.Errorf("logout Set-Cookie = %q, want an empty value with Max-Age=0", cookie)
	}
	if store.Validate(id) {
		t.Error("the session survived logout")
	}
	if response.StatusCode != http.StatusOK || store.Validate(id) {
		t.Error("the revoked session still authenticates")
	}
}

// TestLogoutWithoutSessionIsStillOK pins "always 200".
func TestLogoutWithoutSessionIsStillOK(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	if got := post(t, server, "/api/auth/logout", "").StatusCode; got != http.StatusOK {
		t.Errorf("logout with no cookie: %d, want 200", got)
	}
}

// TestSixWrongLoginsLockTheAddress is spec acceptance 6: the sixth wrong login in a minute is 429
// with a Retry-After in seconds.
func TestSixWrongLoginsLockTheAddress(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	for i := 1; i <= maxLoginFailures; i++ {
		response := post(t, server, "/api/auth/login", `{"token":"wrong"}`)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i, response.StatusCode)
		}
	}
	response := post(t, server, "/api/auth/login", `{"token":"wrong"}`)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("sixth attempt: %d, want 429", response.StatusCode)
	}
	retryAfter := response.Header.Get("Retry-After")
	if retryAfter != "60" {
		t.Errorf("Retry-After = %q, want 60", retryAfter)
	}
	// Even the *correct* token is refused while locked.
	if got := post(t, server, "/api/auth/login", `{"token":"`+testToken+`"}`).StatusCode; got != http.StatusTooManyRequests {
		t.Errorf("correct token while locked: %d, want 429", got)
	}
}

// TestManyFailedLoginsFromOneAddressStayLocked is the security test the step asks for: 10 000
// failed logins from one address must not escape the lock, and the counter must not be defeated
// by the entry sweep. The address is fixed per request, so all of them share one bucket.
func TestManyFailedLoginsFromOneAddressStayLocked(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	locked := 0
	for i := 0; i < 10_000; i++ {
		response := post(t, server, "/api/auth/login", `{"token":"wrong"}`)
		switch response.StatusCode {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			locked++
			if retryAfter := response.Header.Get("Retry-After"); retryAfter == "" {
				t.Fatalf("attempt %d: 429 without Retry-After", i)
			}
		default:
			t.Fatalf("attempt %d: unexpected status %d", i, response.StatusCode)
		}
	}
	if locked == 0 {
		t.Fatal("10 000 failed logins never produced a 429")
	}
	// The first maxLoginFailures attempts may 401; everything after must be locked out.
	if locked < 10_000-maxLoginFailures {
		t.Errorf("only %d of 10 000 attempts were locked out; the limiter was reset", locked)
	}
	// The correct token is still refused at the end.
	if got := post(t, server, "/api/auth/login", `{"token":"`+testToken+`"}`).StatusCode; got != http.StatusTooManyRequests {
		t.Errorf("correct token after 10 000 failures: %d, want 429", got)
	}
}

// TestLockedAddressIsRefusedTheQueryTokenPath covers the middleware's own 429, which — unlike the
// login route — carries no Retry-After in the TypeScript.
func TestLockedAddressIsRefusedTheQueryTokenPath(t *testing.T) {
	server, _, limiter := newTestServer(t, testToken)
	for i := 0; i < maxLoginFailures; i++ {
		post(t, server, "/api/auth/login", `{"token":"wrong"}`)
	}
	if limiter.lockedFor("127.0.0.1") <= 0 {
		t.Fatal("the test address is not locked; the limiter key does not match the remote address")
	}
	response := get(t, server, "/?token="+testToken, nil, "")
	if response.StatusCode != http.StatusTooManyRequests {
		t.Errorf("locked ?token= request: %d, want 429", response.StatusCode)
	}
	if retryAfter := response.Header.Get("Retry-After"); retryAfter != "" {
		t.Errorf("the middleware's 429 set Retry-After = %q; the TypeScript does not", retryAfter)
	}
}

// TestRateLimiterIsKeyedOnTheSocketAddress proves X-Forwarded-For cannot rotate the bucket: two
// requests with different forwarded headers share one limiter key.
func TestRateLimiterIsKeyedOnTheSocketAddress(t *testing.T) {
	server, _, limiter := newTestServer(t, testToken)
	for i := 0; i < maxLoginFailures; i++ {
		post(t, server, "/api/auth/login", `{"token":"wrong"}`)
	}
	// The store key is the loopback address the test client actually connected from.
	if limiter.lockedFor("127.0.0.1") <= 0 && limiter.lockedFor("::1") <= 0 {
		t.Fatal("no bucket is locked; the test did not exercise the limiter")
	}
	response := postWithHeaders(t, server, "/api/auth/login", `{"token":"wrong"}`, map[string]string{
		"X-Forwarded-For": "203.0.113.9",
	})
	if response.StatusCode != http.StatusTooManyRequests {
		t.Errorf("a request with a fresh X-Forwarded-For got %d, want 429", response.StatusCode)
	}
}

// TestCookieFlags pins the cookie attributes: HttpOnly, SameSite=Lax, Path=/, a 30-day Max-Age,
// and Secure only for a secure request.
func TestCookieFlags(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	plain := get(t, server, "/?token="+testToken, nil, "").Header.Get("Set-Cookie")
	if plain == "" {
		t.Fatal("no Set-Cookie on a plain request")
	}
	for _, attribute := range []string{"Path=/", "HttpOnly", "SameSite=Lax", "Max-Age=2592000"} {
		if !strings.Contains(plain, attribute) {
			t.Errorf("cookie %q is missing %q", plain, attribute)
		}
	}
	if strings.Contains(plain, "Secure") {
		t.Errorf("plain-HTTP cookie carries Secure: %q", plain)
	}
}

// TestSecureCookieOverForwardedProto pins the other half of isSecureRequest: a proxy that
// terminated TLS says so with X-Forwarded-Proto.
func TestSecureCookieOverForwardedProto(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	for value, want := range map[string]bool{
		"https":         true,
		"https, http":   true,
		" https ":       true,
		"http":          false,
		"http, https":   false,
		"HTTP":          false,
		"wss":           false,
		"httpsomething": false,
		"":              false,
		// The TypeScript splits on "," only, so a semicolon-joined value is a single token
		// and does not equal "https".
		"https;http://x": false,
		"https,http":     true,
	} {
		response := get(t, server, "/?token="+testToken, map[string]string{"X-Forwarded-Proto": value}, "")
		cookie := response.Header.Get("Set-Cookie")
		secure := strings.Contains(cookie, "; Secure")
		if secure != want {
			t.Errorf("X-Forwarded-Proto: %q -> Secure = %v, want %v (cookie %q)", value, secure, want, cookie)
		}
	}
}

// TestIsSecureRequest covers the TLS-socket half directly, since a real TLS listener is not part
// of the unit tests.
func TestIsSecureRequest(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "http://example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if isSecureRequest(request) {
		t.Error("a plain request reported as secure")
	}
	request.TLS = &tls.ConnectionState{}
	if !isSecureRequest(request) {
		t.Error("a TLS request reported as insecure")
	}
}

// TestOldCookieIsRejected pins docs/specs/web-token-login.md: the `nex_lite_token` cookie carried
// the raw token and is no longer accepted.
func TestOldCookieIsRejected(t *testing.T) {
	server, _, _ := newTestServer(t, testToken)
	response := get(t, server, "/api/anything", nil, "nex_lite_token="+testToken)
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("nex_lite_token cookie: %d, want 401", response.StatusCode)
	}
}

// TestCookieValueParsing pins parseCookieHeader's semantics: the first "=" splits, names and
// values are trimmed, and a percent-encoded value is decoded.
func TestCookieValueParsing(t *testing.T) {
	cases := map[string]string{
		"nex_session=abc":                                "abc",
		"  nex_session = abc  ":                          "abc",
		"other=1; nex_session=abc":                       "abc",
		"nex_session=abc; nex_session=def":               "abc",
		"nex_session":                                    "",
		"=abc":                                           "",
		"other=1":                                        "",
		"nex_session=with%20space":                       "with space",
		"nex_session=%zz":                                "%zz",
		"NEX_SESSION=abc":                                "",
		"nex_session=abc=def":                            "abc=def",
		"session=abc; nex_session=xyz":                   "xyz",
		"nex_session=YWJjZGVmZw==":                       "YWJjZGVmZw==",
		"foo=bar; ; nex_session=id":                      "id",
		"nex_session=%E4%B8%AD%E6%96%87":                 "中文",
		"nex_session=%ED%A0%80":                          "%ED%A0%80", // lone surrogate: URIError -> raw
		"nex_session=; other=1":                          "",
		" nex_session=tab\tvalue":                        "tab\tvalue",
		"nex_session=%00":                                "\x00",
		"prefix_nex_session=abc":                         "",
		"nex_sessionX=abc; nex_session=real":             "real",
		"nex_session=abc;":                               "abc",
		"; nex_session=abc":                              "abc",
		"nex_session=abc;; nex_session=def":              "abc",
		"a=1; b=2; nex_session=sid; nex_session_other=3": "sid",
	}
	for header, want := range cases {
		if got := cookieValue(header, CookieName); got != want {
			t.Errorf("cookieValue(%q) = %q, want %q", header, got, want)
		}
	}
}

// TestDotSegmentPathsAreNormalizedToTheirRealPath proves the auth decision sees the same pathname
// the WHATWG parser would: a dot-segment path cannot dodge the protected check.
func TestDotSegmentPathsAreNormalizedToTheirRealPath(t *testing.T) {
	cases := map[string]string{
		"/api/../api/server-info": "/api/server-info",
		"/api/auth/..":            "/api/",
		"/ws/../ws/host":          "/ws/host",
		"/./api/x":                "/api/x",
		"/%2e%2e/api/x":           "/api/x",
		"/api/%2e%2e/api/x":       "/api/x",
		"/..%2fapi/x":             "/..%2fapi/x", // %2f is not a separator
		"/ws/":                    "/ws/",
		"/":                       "/",
	}
	for input, want := range cases {
		if got := removeDotSegments(input); got != want {
			t.Errorf("removeDotSegments(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestSessionsFileIsSharedWithNodeDuringTransition documents the D8 requirement in code: the Go
// store must read a file a Node process wrote in the same tick. The store re-reads on every
// operation, so an external write is picked up without a restart.
func TestSessionsFileIsSharedWithNodeDuringTransition(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionsFileName)
	now := int64(1_800_000_000_000)
	store := NewSessionStore(crosscheckToken, path, fixedClock(now), nil)

	// The Go server is already running with an empty store.
	if store.Validate(crosscheckSessionID) {
		t.Fatal("a session validated before the file existed")
	}
	// "Node" writes a session underneath it.
	writeSessionsFile(t, path, sessionsFile{
		Version:          1,
		TokenFingerprint: crosscheckFingerprint,
		Sessions: []storedSession{{
			IDHash:    crosscheckIDHash,
			CreatedAt: now,
			ExpiresAt: now + SessionTTL,
		}},
	})
	// The running Go store sees it on the next request, with no restart.
	if !store.Validate(crosscheckSessionID) {
		t.Fatal("the running Go store did not pick up the Node-written session")
	}
	// And a Go login does not clobber it.
	goID, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !store.Validate(crosscheckSessionID) || !store.Validate(goID) {
		t.Fatal("a Go login dropped the Node-written session")
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	buffer := make([]byte, 0, 256)
	chunk := make([]byte, 256)
	for {
		n, err := response.Body.Read(chunk)
		buffer = append(buffer, chunk[:n]...)
		if err != nil {
			break
		}
	}
	return string(buffer)
}

// sessionIDFromCookie extracts the value of the nex_session cookie from a Set-Cookie header.
func sessionIDFromCookie(t *testing.T, setCookie string) string {
	t.Helper()
	pair, _, _ := strings.Cut(setCookie, ";")
	name, value, found := strings.Cut(pair, "=")
	if !found || name != CookieName || value == "" {
		t.Fatalf("Set-Cookie %q does not carry a %s value", setCookie, CookieName)
	}
	return value
}

func post(t *testing.T, server *httptest.Server, path, body string) *http.Response {
	t.Helper()
	return postWithHeaders(t, server, path, body, map[string]string{"Content-Type": "application/json"})
}

// postWithCookie issues a POST carrying one Cookie header, which is how logout is reached from a
// browser.
func postWithCookie(t *testing.T, server *httptest.Server, path, body, cookie string) *http.Response {
	t.Helper()
	return postWithHeaders(t, server, path, body, map[string]string{
		"Content-Type": "application/json",
		"Cookie":       cookie,
	})
}

func postWithHeaders(t *testing.T, server *httptest.Server, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// TestDefaultSessionsFilePath pins the location documented in docs/specs/web-token-login.md:
// <appConfigDir>/web-sessions.json, where getAppConfigDir() is <data base>/.nex/v2.
func TestDefaultSessionsFilePath(t *testing.T) {
	t.Setenv("NEX_DATA_BASE_DIR", filepath.Join("/tmp", "nex-crosscheck-base"))
	got := DefaultSessionsFilePath()
	want := filepath.Join("/tmp", "nex-crosscheck-base", ".nex", "v2", SessionsFileName)
	if got != want {
		t.Errorf("DefaultSessionsFilePath() = %q, want %q", got, want)
	}
}

// TestSessionCookieMaxAgeMatchesTTL guards the one place a constant is derived rather than copied:
// the cookie's Max-Age must stay 30 days when the TTL does.
func TestSessionCookieMaxAgeMatchesTTL(t *testing.T) {
	if got := strconv.Itoa(SessionTTL / 1000); got != "2592000" {
		t.Errorf("Max-Age = %s, want 2592000", got)
	}
}

// TestEmptySessionIDIsRejected pins `if (!sessionId) return false`.
func TestEmptySessionIDIsRejected(t *testing.T) {
	store := NewSessionStore(testToken, "", nil, nil)
	if store.Validate("") {
		t.Error("the empty session id validated")
	}
}

// TestSessionsFileIsNotCreatedForAReadOnlyUse keeps the store from touching disk when a caller
// only validates: the file must not appear as a side effect.
func TestSessionsFileIsNotCreatedForAReadOnlyUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionsFileName)
	store := NewSessionStore(testToken, path, nil, nil)
	store.Validate("anything")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Validate created %s (stat err = %v)", SessionsFileName, err)
	}
}
