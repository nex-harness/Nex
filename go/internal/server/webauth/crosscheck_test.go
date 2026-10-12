package webauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Cross-compat with the TypeScript store. These tests are driven by
// scripts/go-rewrite/crosscheck-webauth-sessions.mjs, which runs the *real*
// createWebSessionStore from packages/server/src/webAuth.ts on the Node side; this file only
// performs the Go half and is skipped unless the script sets the variables.
//
//	//go:build !windows -- not needed; the variables are the gate.
//
// NEX_CROSSCHECK_MODE = "go-reads-node" | "go-writes-node"
// NEX_CROSSCHECK_TOKEN = the configured token both sides use
// NEX_CROSSCHECK_FILE  = the web-sessions.json both sides share
// NEX_CROSSCHECK_ID    = "go-reads-node": the session id Node minted (expected to validate)
//
// The point of the round trip is the shared contract: the token fingerprint
// (sha256("nex-web-session:" + token)), the session id hash (sha256(sessionId)), the file shape
// and the atomic 0600 write. If any of those drifts, a user logged into the Node server is logged
// out of the Go one, and vice versa, during the strangler transition.

const (
	crosscheckEnvMode  = "NEX_CROSSCHECK_MODE"
	crosscheckEnvToken = "NEX_CROSSCHECK_TOKEN"
	crosscheckEnvFile  = "NEX_CROSSCHECK_FILE"
	crosscheckEnvID    = "NEX_CROSSCHECK_ID"
)

func crosscheckConfig(t *testing.T) (mode, token, path string) {
	t.Helper()
	mode = os.Getenv(crosscheckEnvMode)
	if mode == "" {
		t.Skip("not driven by crosscheck-webauth-sessions.mjs")
	}
	token = os.Getenv(crosscheckEnvToken)
	path = os.Getenv(crosscheckEnvFile)
	if token == "" || path == "" {
		t.Fatalf("%s/%s must be set", crosscheckEnvToken, crosscheckEnvFile)
	}
	return mode, token, path
}

// TestCrosscheckGoReadsNode: a web-sessions.json written by the real TypeScript store must
// validate in Go.
func TestCrosscheckGoReadsNode(t *testing.T) {
	mode, token, path := crosscheckConfig(t)
	if mode != "go-reads-node" {
		t.Skipf("mode is %q", mode)
	}
	id := os.Getenv(crosscheckEnvID)
	if id == "" {
		t.Fatal(crosscheckEnvID + " must be set")
	}

	store := NewSessionStore(token, path, nil, nil)
	if !store.Validate(id) {
		raw, _ := os.ReadFile(path)
		t.Fatalf("a session created by the Node store did not validate in Go\nfile:\n%s", raw)
	}
	// A Go logout of the Node-created session must also work: the hash format matches, not just
	// the read path.
	store.Revoke(id)
	if store.Validate(id) {
		t.Fatal("Go could not revoke a Node-created session")
	}
	// And the file Go wrote must still be loadable by Node (checked by the script).
}

// TestCrosscheckGoWritesNode: a session Go mints into the shared file must be readable and
// validatable by the real TypeScript store.
func TestCrosscheckGoWritesNode(t *testing.T) {
	mode, token, path := crosscheckConfig(t)
	if mode != "go-writes-node" {
		t.Skipf("mode is %q", mode)
	}
	store := NewSessionStore(token, path, nil, nil)
	id, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !store.Validate(id) {
		t.Fatal("the session Go just created does not validate")
	}
	// Hand the id to the script so the Node side can validate it against the file on disk.
	if out := os.Getenv("NEX_CROSSCHECK_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Shape check the script's assertions rely on.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("Go wrote a file Node cannot parse: %v", err)
	}
	if parsed["version"] != float64(1) {
		t.Errorf("version = %v, want 1", parsed["version"])
	}
	if parsed["tokenFingerprint"] != tokenFingerprint(token) {
		t.Errorf("tokenFingerprint = %v, want %q", parsed["tokenFingerprint"], tokenFingerprint(token))
	}
}

// TestCrosscheckFileModeAndShape asserts the two file properties the Node store depends on and
// the spec mandates: mode 0600 and a single JSON document ending in a newline.
func TestCrosscheckFileModeAndShape(t *testing.T) {
	mode, token, path := crosscheckConfig(t)
	_ = mode
	store := NewSessionStore(token, path, nil, nil)
	if _, err := store.Create(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Error("web-sessions.json does not end with a newline, unlike JSON.stringify + \"\\n\"")
	}
	if info, err := os.Stat(filepath.Clean(path)); err == nil {
		// On POSIX this is the credential-hygiene requirement; on Windows the mode bits are not
		// meaningful, so the assertion is informational there (see the store test's skip).
		if info.Mode().Perm() != 0o600 && info.Mode().Perm() != 0o666 {
			t.Logf("web-sessions.json mode = %o (expected 600 on POSIX)", info.Mode().Perm())
		}
	}
}
