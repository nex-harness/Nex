package webauth

import (
	"testing"
	"time"
)

const testToken = "correct-horse-battery-staple"

// TestDecisionTable is the exhaustive table from step 2.6 of the phase-2 spec:
// (auth disabled | enabled) × (protected | page | ws) × (no token | good ?token= | bad ?token=
// | valid cookie | invalid cookie | locked out) → expected action.
//
// It drives the pure decide() function, so nothing here depends on net/http or the store; the
// per-row input is exactly what buildRequest() would produce for that request.
func TestDecisionTable(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want Action
	}{
		// ---- auth disabled: nothing is protected, every request passes ----
		{"disabled/page/no-token", Request{Enabled: false, Pathname: "/"}, ActionPass},
		{"disabled/page/bad-token", Request{Enabled: false, Pathname: "/", HasQueryToken: true, QueryToken: "x"}, ActionPass},
		{"disabled/protected/no-token", Request{Enabled: false, Pathname: "/api/server-info"}, ActionPass},
		{"disabled/protected/bad-token", Request{Enabled: false, Pathname: "/api/server-info", HasQueryToken: true, QueryToken: "x"}, ActionPass},
		{"disabled/protected/locked", Request{Enabled: false, Pathname: "/api/x", Locked: true, HasQueryToken: true, QueryToken: "x"}, ActionPass},
		{"disabled/ws/no-token", Request{Enabled: false, Pathname: "/ws"}, ActionPass},
		{"disabled/ws/good-token", Request{Enabled: false, Pathname: "/ws", HasQueryToken: true, QueryToken: testToken}, ActionPass},

		// ---- auth enabled, ?token= present and correct ----
		{"enabled/page/good-token", Request{Enabled: true, Token: testToken, Pathname: "/", HasQueryToken: true, QueryToken: testToken}, ActionMintSession},
		{"enabled/page/deep/good-token", Request{Enabled: true, Token: testToken, Pathname: "/dashboard", HasQueryToken: true, QueryToken: testToken}, ActionMintSession},
		{"enabled/protected/good-token", Request{Enabled: true, Token: testToken, Pathname: "/api/server-info", HasQueryToken: true, QueryToken: testToken}, ActionPass},
		{"enabled/ws/good-token", Request{Enabled: true, Token: testToken, Pathname: "/ws", HasQueryToken: true, QueryToken: testToken}, ActionPass},
		{"enabled/ws/nested/good-token", Request{Enabled: true, Token: testToken, Pathname: "/ws/host", HasQueryToken: true, QueryToken: testToken}, ActionPass},
		// An auth route is not protected, so ?token= there behaves like a page.
		{"enabled/auth-route/good-token", Request{Enabled: true, Token: testToken, Pathname: "/api/auth/session", HasQueryToken: true, QueryToken: testToken}, ActionMintSession},

		// ---- auth enabled, ?token= present and wrong ----
		{"enabled/page/bad-token", Request{Enabled: true, Token: testToken, Pathname: "/", HasQueryToken: true, QueryToken: "wrong"}, ActionPass},
		{"enabled/page/empty-token", Request{Enabled: true, Token: testToken, Pathname: "/", HasQueryToken: true, QueryToken: ""}, ActionPass},
		{"enabled/protected/bad-token", Request{Enabled: true, Token: testToken, Pathname: "/api/server-info", HasQueryToken: true, QueryToken: "wrong"}, ActionUnauthorized},
		{"enabled/ws/bad-token", Request{Enabled: true, Token: testToken, Pathname: "/ws", HasQueryToken: true, QueryToken: "wrong"}, ActionUnauthorized},
		{"enabled/ws/nested/bad-token", Request{Enabled: true, Token: testToken, Pathname: "/ws/remote/abc", HasQueryToken: true, QueryToken: "wrong"}, ActionUnauthorized},

		// ---- auth enabled, ?token= present while the address is locked: 429 regardless ----
		{"enabled/page/locked-good-token", Request{Enabled: true, Token: testToken, Pathname: "/", HasQueryToken: true, QueryToken: testToken, Locked: true}, ActionTooManyAttempts},
		{"enabled/protected/locked-bad-token", Request{Enabled: true, Token: testToken, Pathname: "/api/x", HasQueryToken: true, QueryToken: "bad", Locked: true}, ActionTooManyAttempts},

		// ---- auth enabled, no ?token= ----
		{"enabled/page/no-token", Request{Enabled: true, Token: testToken, Pathname: "/"}, ActionPass},
		{"enabled/page/invalid-cookie", Request{Enabled: true, Token: testToken, Pathname: "/", HasValidSession: false}, ActionPass},
		{"enabled/page/locked-no-token", Request{Enabled: true, Token: testToken, Pathname: "/", Locked: true}, ActionPass},
		{"enabled/auth-route/no-token", Request{Enabled: true, Token: testToken, Pathname: "/api/auth/login"}, ActionPass},
		{"enabled/protected/no-cookie", Request{Enabled: true, Token: testToken, Pathname: "/api/server-info"}, ActionUnauthorized},
		{"enabled/protected/invalid-cookie", Request{Enabled: true, Token: testToken, Pathname: "/api/server-info", HasValidSession: false}, ActionUnauthorized},
		{"enabled/protected/valid-cookie", Request{Enabled: true, Token: testToken, Pathname: "/api/server-info", HasValidSession: true}, ActionPass},
		{"enabled/ws/no-cookie", Request{Enabled: true, Token: testToken, Pathname: "/ws"}, ActionUnauthorized},
		{"enabled/ws/valid-cookie", Request{Enabled: true, Token: testToken, Pathname: "/ws", HasValidSession: true}, ActionPass},
		{"enabled/ws/locked-no-token", Request{Enabled: true, Token: testToken, Pathname: "/ws", Locked: true}, ActionUnauthorized},
		// A path that merely starts with the same letters is not protected.
		{"enabled/not-protected/apifoo", Request{Enabled: true, Token: testToken, Pathname: "/apifoo"}, ActionPass},
		{"enabled/not-protected/wsx", Request{Enabled: true, Token: testToken, Pathname: "/wsx"}, ActionPass},
		{"enabled/not-protected/api", Request{Enabled: true, Token: testToken, Pathname: "/api"}, ActionPass},
		{"enabled/auth-route/valid-cookie-unused", Request{Enabled: true, Token: testToken, Pathname: "/api/auth/logout", HasValidSession: true}, ActionPass},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decide(tc.req).Action; got != tc.want {
				t.Errorf("decide(%+v).Action = %v, want %v", tc.req, got, tc.want)
			}
		})
	}
}

// TestIsTokenProtectedPath pins the protected-path set from docs/specs/web-token-login.md:
// /ws, /ws/* and /api/* except /api/auth/*.
func TestIsTokenProtectedPath(t *testing.T) {
	cases := map[string]bool{
		"/":                        false,
		"/dashboard":               false,
		"/login":                   false,
		"/assets/app.js":           false,
		"/ws":                      true,
		"/ws/":                     true,
		"/ws/host":                 true,
		"/ws/remote/abc":           true,
		"/api":                     false, // not "/api/" — the TS tests the prefix with the slash
		"/api/server-info":         true,
		"/api/":                    true,
		"/api/rpc-host-capability": true,
		"/api/auth":                false,
		"/api/auth/":               false,
		"/api/auth/session":        false,
		"/api/auth/login":          false,
		"/api/auth/logout":         false,
		"/api/author":              true, // "/api/auth" without the trailing slash is not exempt
		"/wsx":                     false,
	}
	for path, want := range cases {
		if got := IsTokenProtectedPath(path); got != want {
			t.Errorf("IsTokenProtectedPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestDecisionIsPure guards the property the table depends on: decide() reads only its argument,
// so the same input always yields the same output and no hidden state is consulted.
func TestDecisionIsPure(t *testing.T) {
	req := Request{Enabled: true, Token: testToken, Pathname: "/", HasQueryToken: true, QueryToken: testToken}
	first := decide(req)
	for i := 0; i < 100; i++ {
		if got := decide(req); got != first {
			t.Fatalf("decide is not deterministic: got %+v then %+v", first, got)
		}
	}
	if first.Action != ActionMintSession {
		t.Fatalf("good ?token= on a page should mint a session, got %v", first.Action)
	}
}

// TestLimiterLocksAtThreshold pins createLoginRateLimiter's constants: five failures lock the
// key for 60 s, and the lock is reported with the remaining time.
func TestLimiterLocksAtThreshold(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	limiter := newLoginRateLimiter(func() time.Time { return now })

	for i := 1; i <= maxLoginFailures-1; i++ {
		limiter.recordFailure("10.0.0.1")
		if got := limiter.lockedFor("10.0.0.1"); got != 0 {
			t.Fatalf("after %d failures: lockedFor = %v, want 0", i, got)
		}
	}
	limiter.recordFailure("10.0.0.1")
	if got := limiter.lockedFor("10.0.0.1"); got != loginLockDuration {
		t.Fatalf("after %d failures: lockedFor = %v, want %v", maxLoginFailures, got, loginLockDuration)
	}

	// Other keys are unaffected by one address's failures.
	if got := limiter.lockedFor("10.0.0.2"); got != 0 {
		t.Errorf("unrelated address locked for %v", got)
	}
	// The lock expires after exactly 60 s.
	now = base.Add(loginLockDuration)
	if got := limiter.lockedFor("10.0.0.1"); got != 0 {
		t.Errorf("after the lock window: lockedFor = %v, want 0", got)
	}
}

// TestLimiterRecountsAfterLock pins the "锁定期满后重新计数" rule: the first failure after the
// lock expires does not immediately lock again.
func TestLimiterRecountsAfterLock(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	limiter := newLoginRateLimiter(func() time.Time { return now })
	for i := 0; i < maxLoginFailures; i++ {
		limiter.recordFailure("key")
	}
	now = base.Add(loginLockDuration + time.Millisecond)
	limiter.recordFailure("key")
	if got := limiter.lockedFor("key"); got != 0 {
		t.Fatalf("one failure after the lock expired re-locked for %v", got)
	}
	for i := 0; i < maxLoginFailures-1; i++ {
		limiter.recordFailure("key")
	}
	if got := limiter.lockedFor("key"); got == 0 {
		t.Fatal("the counter never re-accumulated to the threshold")
	}
}

// TestLimiterSuccessClears pins recordSuccess: a correct token wipes the failure count, so
// maxLoginFailures-1 more failures must not lock — the count restarted at zero rather than
// carrying over — and the threshold-th one does.
func TestLimiterSuccessClears(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	limiter := newLoginRateLimiter(func() time.Time { return now })
	for i := 0; i < maxLoginFailures-1; i++ {
		limiter.recordFailure("key")
	}
	limiter.recordSuccess("key")
	for i := 0; i < maxLoginFailures-1; i++ {
		limiter.recordFailure("key")
	}
	if got := limiter.lockedFor("key"); got != 0 {
		t.Fatalf("recordSuccess did not clear the counter: lockedFor = %v", got)
	}
	limiter.recordFailure("key")
	if got := limiter.lockedFor("key"); got != loginLockDuration {
		t.Fatalf("the fresh count locked with the wrong duration: %v", got)
	}
}
