package server

// sessionpersistence_test.go is the regression test for a bug the acceptance list caught, not a
// package that already needed one.
//
// THE BUG: NewWebSocket built its session store with an EMPTY file path, so web-sessions.json was
// never written or read by the Go front. A session therefore lived only in the process that minted
// it, and every one of these was broken:
//
//   - a server restart logged every user out (docker-single-image.md acceptance item 3);
//   - the Node upstream, which DOES persist to <data base>/.nex/v2/web-sessions.json, opened the
//     same file with its own generated token. webAuth discards sessions whose fingerprint differs
//     from its own — the correct behaviour for a token rotation — so the upstream's next write
//     erased every session the Go front had handed out.
//
// The second point is why this is not a "restart is a bit rude" bug: with the strangler in front of
// Node, two processes with two different tokens writing one sessions file make login
// non-deterministic. The fix has two halves, and this file pins the half in this package:
//
//	1. NewWebSocket defaults the store to webauth.DefaultSessionsFilePath() when a token is set;
//	2. cmd/nex-server runs the upstream with its OWN data base (see cmd/nex-server/serve.go).
//
// Half 1 is what this file can observe: with NEX_DATA_BASE_DIR pointing at a temp dir, a session
// minted through the handler is on disk afterwards, and a SECOND handler built from the same
// directory recognises it. The second handler stands in for the restarted process, and for the
// upstream — both are "another process reading the same file".

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylinux/nex/go/internal/server/webauth"
)

const persistenceToken = "persistence-test-token"

// newPersistenceHandler builds the real route table against the temp data base the test installed
// with t.Setenv. It goes through NewHandler rather than NewWebSocket so the test also covers the
// wiring between them — the bug was that NewHandler passed no path down at all.
func newPersistenceHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, _, err := NewHandler(Options{AuthToken: persistenceToken, WSConfig: &WSConfig{
		SessionsFilePath: sessionsPathIn(t),
	}})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

func sessionsPathIn(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("NEX_DATA_BASE_DIR"), ".nex", "v2", webauth.SessionsFileName)
}

// login mints a session the way the UI does and returns the cookie value.
func login(t *testing.T, handler http.Handler) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"token":"`+persistenceToken+`"}`))
	request.Header.Set("content-type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "nex_session" && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatal("login returned no nex_session cookie")
	return ""
}

func sessionAuthenticated(t *testing.T, handler http.Handler, cookie string) bool {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	request.AddCookie(&http.Cookie{Name: "nex_session", Value: cookie})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/session = %d, want 200", recorder.Code)
	}
	var body struct {
		AuthRequired  bool `json:"authRequired"`
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("session body %q: %v", recorder.Body.String(), err)
	}
	return body.Authenticated
}

// TestSessionSurvivesANewHandler is acceptance item 3 in miniature: a session minted by one process
// is recognised by a later process reading the same data base.
func TestSessionSurvivesANewHandler(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEX_DATA_BASE_DIR", dir)

	first := newPersistenceHandler(t)
	cookie := login(t, first)
	if !sessionAuthenticated(t, first, cookie) {
		t.Fatal("the session that was just minted is not authenticated on its own process")
	}

	// A second handler is a restarted process: new store, new in-memory state, same file.
	second := newPersistenceHandler(t)
	if !sessionAuthenticated(t, second, cookie) {
		t.Fatal("the session did not survive the restart: a new handler reading the same data " +
			"base rejected the cookie, which is acceptance item 3 failing")
	}
}

// TestSessionIsWrittenToTheDataBase pins the file itself rather than only the behaviour. A store
// that somehow validated the cookie from memory would pass the test above while still losing the
// session on restart, so the on-disk shape is asserted directly: version 1, the token fingerprint,
// and one hashed session id.
func TestSessionIsWrittenToTheDataBase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEX_DATA_BASE_DIR", dir)

	handler := newPersistenceHandler(t)
	login(t, handler)

	raw, err := os.ReadFile(sessionsPathIn(t))
	if err != nil {
		t.Fatalf("web-sessions.json was not written: %v", err)
	}
	var stored struct {
		Version  int `json:"version"`
		Sessions []struct {
			IDHash    string `json:"idHash"`
			CreatedAt int64  `json:"createdAt"`
			ExpiresAt int64  `json:"expiresAt"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("web-sessions.json is not the documented shape: %v (%s)", err, raw)
	}
	if stored.Version != 1 {
		t.Errorf("version = %d, want 1", stored.Version)
	}
	if len(stored.Sessions) != 1 {
		t.Fatalf("sessions = %d, want exactly 1", len(stored.Sessions))
	}
	// The id is stored hashed, never in the clear: the file is mode 0600, but a leaked copy must
	// not be replayable as a cookie.
	if len(stored.Sessions[0].IDHash) != 64 {
		t.Errorf("idHash = %q, want a 64-character sha256 hex digest", stored.Sessions[0].IDHash)
	}
	if stored.Sessions[0].ExpiresAt <= stored.Sessions[0].CreatedAt {
		t.Errorf("expiresAt (%d) is not after createdAt (%d)", stored.Sessions[0].ExpiresAt, stored.Sessions[0].CreatedAt)
	}
}

// TestNoTokenMeansNoSessionsFile: a server started without NEX_SERVER_AUTH_TOKEN has no sessions
// to persist, and writing an empty web-sessions.json into the operator's config directory on every
// start would be a side effect of a deployment that never uses sessions. NewWebSocket therefore
// only defaults the path when a token is present.
func TestNoTokenMeansNoSessionsFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEX_DATA_BASE_DIR", dir)

	endpoints := NewWebSocket("", WSConfig{})
	store := endpoints.AuthMiddleware()
	if store.Enabled() {
		t.Fatal("an empty token produced an enabled auth middleware")
	}
	path := filepath.Join(dir, ".nex", "v2", webauth.SessionsFileName)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("web-sessions.json exists at %s without a configured token (stat err %v)", path, err)
	}
}

// TestDefaultSessionsPathIsDerivedFromTheDataBase pins where NewWebSocket's default lands. It is
// webauth.DefaultSessionsFilePath's own rule, restated here because the DEFAULT is what production
// uses and the tests above override it: if that resolution changed, every existing deployment's
// sessions would move.
func TestDefaultSessionsPathIsDerivedFromTheDataBase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEX_DATA_BASE_DIR", dir)
	if got, want := webauth.DefaultSessionsFilePath(), sessionsPathIn(t); got != want {
		t.Errorf("DefaultSessionsFilePath() = %q, want %q", got, want)
	}
}
