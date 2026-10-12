package webauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// crosscheckToken/crosscheckFingerprint/crosscheckSessionID/crosscheckIDHash are a fixture
// produced by the *TypeScript* implementation, not by this code:
//
//	npx tsx -e 'import {createHash,randomBytes} from "node:crypto";
//	  const token="crosscheck-token";
//	  console.log(createHash("sha256").update(`nex-web-session:${token}`).digest("hex"))'
//
// They are the cross-implementation contract from step 2.6: Go and Node must agree on the
// fingerprint and on sha256(sessionId) byte for byte, otherwise a session minted by one is
// invisible to the other. scripts/go-rewrite/crosscheck-webauth-sessions.mjs regenerates the
// pair live against both implementations.
const (
	crosscheckToken       = "crosscheck-token"
	crosscheckFingerprint = "766722e2489ca480ee11281d6e213417562a238274f83e057096a545257325d3"
	crosscheckSessionID   = "qcTFXWV6S07fkqI6NbqHhSoaNBtnU1nIEACtADzH4GM"
	crosscheckIDHash      = "08c38f9abf2d0cf86352a8cdecaf58dc26842cef408b2a274e90fe902a4bd6d5"
)

// TestTokenFingerprintMatchesTypeScript pins the on-disk fingerprint contract. If this drifts,
// every existing session is silently invalidated on the next load.
func TestTokenFingerprintMatchesTypeScript(t *testing.T) {
	if got := tokenFingerprint(crosscheckToken); got != crosscheckFingerprint {
		t.Errorf("tokenFingerprint(%q) = %q, want the TypeScript value %q", crosscheckToken, got, crosscheckFingerprint)
	}
}

// TestSHA256HexMatchesTypeScript pins the session-id hash contract.
func TestSHA256HexMatchesTypeScript(t *testing.T) {
	if got := sha256Hex(crosscheckSessionID); got != crosscheckIDHash {
		t.Errorf("sha256Hex(%q) = %q, want the TypeScript value %q", crosscheckSessionID, got, crosscheckIDHash)
	}
}

func fixedClock(ms int64) func() time.Time {
	return func() time.Time { return time.UnixMilli(ms).UTC() }
}

// writeSessionsFile writes a sessions file with the exact bytes JSON.stringify(payload, null, 2)
// + "\n" produces, so the Go reader is tested against Node's formatting rather than Go's.
func writeSessionsFile(t *testing.T, path string, file sessionsFile) {
	t.Helper()
	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestValidateAcceptsSessionWrittenByNode is the Node -> Go direction of the cross-compat
// requirement: a web-sessions.json with a known session id + token fingerprint, in the exact
// JSON shape the TypeScript store persists, must validate in Go.
func TestValidateAcceptsSessionWrittenByNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionsFileName)
	now := int64(1_800_000_000_000)
	writeSessionsFile(t, path, sessionsFile{
		Version:          1,
		TokenFingerprint: crosscheckFingerprint,
		Sessions: []storedSession{{
			IDHash:    crosscheckIDHash,
			CreatedAt: now,
			ExpiresAt: now + SessionTTL,
		}},
	})

	store := NewSessionStore(crosscheckToken, path, fixedClock(now+1000), nil)
	if !store.Validate(crosscheckSessionID) {
		t.Fatal("a session created by Node did not validate in Go")
	}
	// The id itself must never be in the file.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), crosscheckSessionID) {
		t.Fatal("the session id leaked into web-sessions.json; only its hash may be stored")
	}
}

// TestSessionWrittenByGoIsReadableByNode is the Go -> Node direction, asserted on the file bytes:
// what this store writes must satisfy the checks the TypeScript store performs when loading
// (version, fingerprint match, and the idHash of the id it minted). The live cross-check against
// the real Node code is scripts/go-rewrite/crosscheck-webauth-sessions.mjs.
func TestSessionWrittenByGoIsReadableByNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionsFileName)
	now := int64(1_800_000_000_000)
	store := NewSessionStore(crosscheckToken, path, fixedClock(now), nil)

	id, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Byte-level shape: indent = 2, trailing newline, no tabs.
	if !strings.HasSuffix(string(raw), "}\n") {
		t.Errorf("file does not end with a newline: %q", tail(string(raw)))
	}
	if strings.Contains(string(raw), "\t") {
		t.Error("file contains tabs; JSON.stringify(payload, null, 2) uses spaces")
	}

	// Structural shape, re-read through encoding/json the way the TypeScript re-parses it.
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["version"] != float64(1) {
		t.Errorf("version = %v, want 1", parsed["version"])
	}
	if parsed["tokenFingerprint"] != crosscheckFingerprint {
		t.Errorf("tokenFingerprint = %v, want %q", parsed["tokenFingerprint"], crosscheckFingerprint)
	}
	sessions, ok := parsed["sessions"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("sessions = %v, want one entry", parsed["sessions"])
	}
	entry, ok := sessions[0].(map[string]any)
	if !ok {
		t.Fatalf("session entry is not an object: %T", sessions[0])
	}
	// The TypeScript's isStoredSession requires all three fields to be numbers/strings.
	if _, ok := entry["idHash"].(string); !ok {
		t.Errorf("idHash is not a string: %T", entry["idHash"])
	}
	if entry["createdAt"] != float64(now) {
		t.Errorf("createdAt = %v, want %d", entry["createdAt"], now)
	}
	if entry["expiresAt"] != float64(now+SessionTTL) {
		t.Errorf("expiresAt = %v, want %d", entry["expiresAt"], now+SessionTTL)
	}
	// And the hash must be the one Node computes for the id it would mint.
	if entry["idHash"] != sha256Hex(id) {
		t.Errorf("idHash = %v, want sha256(%q) = %q", entry["idHash"], id, sha256Hex(id))
	}

	// Reopening a fresh store (the "restart" case in the spec's acceptance list 2) must accept it.
	if !NewSessionStore(crosscheckToken, path, fixedClock(now+1000), nil).Validate(id) {
		t.Fatal("a session written by Go did not validate after reopening the file")
	}
}

func tail(s string) string {
	if len(s) > 20 {
		return s[len(s)-20:]
	}
	return s
}

// TestExpiredSessionIsPurged covers spec acceptance: an expired session is not accepted and is
// dropped from the file, so the store cannot grow without bound.
func TestExpiredSessionIsPurged(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionsFileName)
	start := int64(1_800_000_000_000)
	store := NewSessionStore(crosscheckToken, path, fixedClock(start), nil)
	id, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !store.Validate(id) {
		t.Fatal("session should be valid before expiry")
	}

	// One millisecond before the TTL boundary it is still valid: the TS compares `expiresAt > now`.
	before := NewSessionStore(crosscheckToken, path, fixedClock(start+SessionTTL-1), nil)
	if !before.Validate(id) {
		t.Fatal("session expired one millisecond early")
	}
	// At expiresAt it is not: the comparison is strict.
	at := NewSessionStore(crosscheckToken, path, fixedClock(start+SessionTTL), nil)
	if at.Validate(id) {
		t.Fatal("session is still valid at expiresAt; the comparison must be strict")
	}

	// A write after expiry rewrites the file without it.
	after := NewSessionStore(crosscheckToken, path, fixedClock(start+SessionTTL+1), nil)
	if _, err := after.Create(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sha256Hex(id)) {
		t.Error("the expired session is still in web-sessions.json after a write")
	}
}

// TestTokenRotationInvalidatesSessions is spec acceptance 3: changing the token must reject
// every session minted with the old one, because the fingerprint no longer matches.
func TestTokenRotationInvalidatesSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionsFileName)
	start := int64(1_800_000_000_000)
	old := NewSessionStore("old-token", path, fixedClock(start), nil)
	id, err := old.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !NewSessionStore("old-token", path, fixedClock(start), nil).Validate(id) {
		t.Fatal("the session should be valid for the token that created it")
	}

	rotated := NewSessionStore("new-token", path, fixedClock(start), nil)
	if rotated.Validate(id) {
		t.Fatal("a session survived a token rotation")
	}
	// The rotated store overwrites the file with its own fingerprint, discarding the old entries.
	if _, err := rotated.Create(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sha256Hex(id)) {
		t.Error("the old session is still stored after the token rotated")
	}
	if !strings.Contains(string(raw), tokenFingerprint("new-token")) {
		t.Error("the file does not carry the new token's fingerprint")
	}
}

// TestMissingOrCorruptFileIsFailClosed covers docs/specs/web-token-login.md: a missing,
// unreadable or corrupt file means "no sessions", never "auth disabled" and never an error.
func TestMissingOrCorruptFileIsFailClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SessionsFileName)
	store := NewSessionStore(crosscheckToken, path, fixedClock(1_800_000_000_000), nil)

	// Missing file.
	if store.Validate(crosscheckSessionID) {
		t.Fatal("a session validated with no file at all")
	}
	if _, err := store.Create(); err != nil {
		t.Fatalf("Create with no existing file: %v", err)
	}

	corrupt := []string{
		"",
		"not json",
		`{"version":1,"tokenFingerprint":"wrong"}`,
		`{"version":2,"tokenFingerprint":"` + crosscheckFingerprint + `","sessions":[]}`,
		`{"version":1,"tokenFingerprint":"` + crosscheckFingerprint + `","sessions":[{"idHash":42}]}`,
		`{"version":1,"tokenFingerprint":"` + crosscheckFingerprint + `","sessions":"nope"}`,
	}
	for _, content := range corrupt {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		fresh := NewSessionStore(crosscheckToken, path, fixedClock(1_800_000_000_000), nil)
		if fresh.Validate(crosscheckSessionID) {
			t.Errorf("a session validated against corrupt content %q", content)
		}
	}
}

// TestUnreadableFileDoesNotDisableAuth checks the fail-closed direction with a *real* IO error: a
// path that is a directory makes every read fail, and Validate must answer false rather than
// panic or report authenticated.
func TestUnreadableFileDoesNotDisableAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-sessions.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(crosscheckToken, path, fixedClock(1_800_000_000_000), nil)
	if store.Validate(crosscheckSessionID) {
		t.Fatal("an unreadable sessions file let a session through")
	}
}

// TestSessionsFileIsCreatedPrivateAndAtomically checks the 0600 mode and that no temp file is
// left behind. The atomic temp+rename itself is verified by crosscheck-webauth-sessions.mjs
// (it observes the file from a second process while a write is in flight).
func TestSessionsFileIsCreatedPrivateAndAtomically(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", SessionsFileName)
	store := NewSessionStore(crosscheckToken, path, fixedClock(1_800_000_000_000), nil)
	if _, err := store.Create(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("web-sessions.json mode = %o, want 600", perm)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory contains %v, want only %s (a leftover temp file means the rename failed)", names, SessionsFileName)
	}
}

// TestCreateFailsWhenPersistFails pins "落盘失败则会话不算创建成功": a login must not hand out a
// cookie for a session that was never written.
func TestCreateFailsWhenPersistFails(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be makes WriteAtomic's rename fail.
	path := filepath.Join(dir, SessionsFileName)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(crosscheckToken, path, fixedClock(1_800_000_000_000), nil)
	id, err := store.Create()
	if err == nil {
		t.Fatalf("Create returned %q with no error, but nothing could be persisted", id)
	}
	if id != "" {
		t.Errorf("Create returned a session id (%q) alongside an error", id)
	}
}

// TestRevokeRemovesTheSession covers logout: after Revoke the cookie is dead, and a fresh store
// (a restart) agrees.
func TestRevokeRemovesTheSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionsFileName)
	start := int64(1_800_000_000_000)
	store := NewSessionStore(crosscheckToken, path, fixedClock(start), nil)
	id, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	store.Revoke(id)
	if store.Validate(id) {
		t.Fatal("the session survived Revoke")
	}
	if NewSessionStore(crosscheckToken, path, fixedClock(start), nil).Validate(id) {
		t.Fatal("a revoked session survived a restart")
	}
	// Revoking an unknown id is a no-op, not an error.
	store.Revoke("some-other-id")
	store.Revoke("")
}

// TestPruneKeepsNewestSessions covers MAX_STORED_SESSIONS: after the cap is exceeded, the oldest
// sessions are dropped and the newest survive.
func TestPruneKeepsNewestSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionsFileName)
	start := int64(1_800_000_000_000)
	store := NewSessionStore(crosscheckToken, path, func() time.Time {
		return time.UnixMilli(start).UTC()
	}, nil)
	for i := 0; i < maxStoredSessions+10; i++ {
		if _, err := store.Create(); err != nil {
			t.Fatal(err)
		}
		// Space the creations out so createdAt ordering is unambiguous.
		start += 1000
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed sessionsFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != maxStoredSessions {
		t.Errorf("stored %d sessions, want the cap of %d", len(parsed.Sessions), maxStoredSessions)
	}
}

// TestMemoryOnlyStoreNeedsNoFile mirrors the TypeScript's "no filePath means in-memory only".
func TestMemoryOnlyStoreNeedsNoFile(t *testing.T) {
	store := NewSessionStore(crosscheckToken, "", nil, nil)
	id, err := store.Create()
	if err != nil {
		t.Fatalf("memory-only Create: %v", err)
	}
	if !store.Validate(id) {
		t.Error("a memory-only session did not validate")
	}
}

// TestConcurrentCreateAndValidate runs the race detector over the store: it is shared by every
// connection, so concurrent logins, validations and logouts must be safe, and no session that
// was successfully created may be lost — which is what "the file is the authority" buys over an
// in-memory model.
func TestConcurrentCreateAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), SessionsFileName)
	store := NewSessionStore(crosscheckToken, path, nil, nil)

	const workers = 16
	ids := make([]string, workers)
	var create sync.WaitGroup
	for i := range workers {
		create.Add(1)
		go func(index int) {
			defer create.Done()
			id, err := store.Create()
			if err != nil {
				t.Errorf("Create: %v", err)
				return
			}
			ids[index] = id
		}(i)
	}
	create.Wait()

	for _, id := range ids {
		if id == "" {
			continue
		}
		if !store.Validate(id) {
			t.Errorf("a concurrently created session (%q) was lost", id)
		}
	}

	// Concurrent validation and revocation over the same set.
	var wait sync.WaitGroup
	for _, id := range ids {
		if id == "" {
			continue
		}
		wait.Add(2)
		go func(id string) { defer wait.Done(); store.Validate(id) }(id)
		go func(id string) { defer wait.Done(); store.Revoke(id) }(id)
	}
	wait.Wait()

	for _, id := range ids {
		if id == "" {
			continue
		}
		if store.Validate(id) {
			t.Errorf("session %q survived concurrent revocation", id)
		}
	}
}
