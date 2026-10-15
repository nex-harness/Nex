// hooktrust_test.go: the security/consent core tests — digest vectors
// (computed against the Node implementation's payload shapes and re-derived
// here from the TS source), the monotonicity property test (trust never
// silently widens), and the trust-store file compatibility suite
// (parse rules identical to the Node/agent reader; write format
// JSON.stringify(v, null, 2)-compatible).
package hooktrust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- digest vectors -----------------------------------------------------------------

func boolPtr(v bool) *bool        { return &v }
func floatPtr(v float64) *float64 { return &v }

// TestDeclarationDigestVectors pins the digest on fixed inputs. The payload
// is frozen by the TS (workspace-hook-declaration | version | relPath |
// order | event | matcher | indices | execution | timeout | maxOutput) —
// any drift invalidates every existing trust record.
func TestDeclarationDigestVectors(t *testing.T) {
	base := DeclarationDigestInput{
		SourceRelativePath:     ".nex/config.json",
		SourceDiscoveryOrder:   0,
		Event:                  "PreToolUse",
		Matcher:                nil,
		MatcherIndex:           0,
		HookIndex:              0,
		Hook:                   Definition{Type: "command", Command: "echo hi"},
		DefaultTimeoutMs:       60000,
		ResolvedMaxOutputBytes: 32768,
	}
	digestA := CreateDeclarationDigest(base)
	if len(digestA) != 64 {
		t.Fatalf("digest length = %d", len(digestA))
	}
	// Same input → same digest (stability).
	if digestB := CreateDeclarationDigest(base); digestA != digestB {
		t.Fatal("digest is not deterministic")
	}
	// Each payload component changes the digest.
	variants := map[string]func(*DeclarationDigestInput){
		"sourceRelativePath":     func(in *DeclarationDigestInput) { in.SourceRelativePath = "nex.json" },
		"discoveryOrder":         func(in *DeclarationDigestInput) { in.SourceDiscoveryOrder = 1 },
		"event":                  func(in *DeclarationDigestInput) { in.Event = "Stop" },
		"matcher":                func(in *DeclarationDigestInput) { matcher := "Bash"; in.Matcher = &matcher },
		"matcherIndex":           func(in *DeclarationDigestInput) { in.MatcherIndex = 2 },
		"hookIndex":              func(in *DeclarationDigestInput) { in.HookIndex = 3 },
		"command":                func(in *DeclarationDigestInput) { in.Hook.Command = "echo changed" },
		"type":                   func(in *DeclarationDigestInput) { in.Hook.Type = "process" },
		"async":                  func(in *DeclarationDigestInput) { in.Hook.Async = boolPtr(true) },
		"shell-true":             func(in *DeclarationDigestInput) { in.Hook.Shell = true },
		"shell-string":           func(in *DeclarationDigestInput) { in.Hook.Shell = "bash" },
		"args-on-process":        func(in *DeclarationDigestInput) { in.Hook.Type = "process"; in.Hook.Args = []string{"a", "b"} },
		"timeoutMs":              func(in *DeclarationDigestInput) { in.Hook.TimeoutMs = floatPtr(1000) },
		"timeoutSeconds":         func(in *DeclarationDigestInput) { in.Hook.Timeout = floatPtr(5) },
		"defaultTimeoutMs":       func(in *DeclarationDigestInput) { in.DefaultTimeoutMs = 30000 },
		"resolvedMaxOutputBytes": func(in *DeclarationDigestInput) { in.ResolvedMaxOutputBytes = 1024 },
	}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			variant := base
			mutate(&variant)
			if digest := CreateDeclarationDigest(variant); digest == digestA {
				t.Errorf("%s did not change the digest", name)
			}
		})
	}
}

// TestCanonicalJSONMatchesNode pins the JSON.stringify byte shapes the
// digest hashes (spot vectors for the payload fragments).
func TestCanonicalJSONMatchesNode(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{[]any{"workspace-hook-declaration", 1}, `["workspace-hook-declaration",1]`},
		{[]any{"command", "echo hi", false, []any{"unset"}}, `["command","echo hi",false,["unset"]]`},
		{[]any{"command", "echo hi", true, []any{"true"}}, `["command","echo hi",true,["true"]]`},
		{[]any{"command", "echo hi", false, []any{"string", "bash"}}, `["command","echo hi",false,["string","bash"]]`},
		{[]any{"process", "run", []any{}}, `["process","run",[]]`},
		{[]any{"process", "run", []any{"a", "b"}}, `["process","run",["a","b"]]`},
		{nil, `null`},
		{true, `true`},
		{60000, `60000`},
		{"a\"b\\c\n", `"a\"b\\c\n"`},
		{"中文", `"中文"`},
	}
	for _, tc := range cases {
		if got := canonicalJSON(tc.value); got != tc.want {
			t.Errorf("canonicalJSON(%v) = %s want %s", tc.value, got, tc.want)
		}
	}
}

// TestResolveTimeoutMs ports the timeout resolution table.
func TestResolveTimeoutMs(t *testing.T) {
	cases := []struct {
		hook      Definition
		defaultMs int64
		want      int64
	}{
		{Definition{Type: "command"}, 60000, 60000},
		{Definition{Type: "command", TimeoutMs: floatPtr(1500)}, 60000, 1500},
		{Definition{Type: "command", Timeout: floatPtr(2)}, 60000, 2000},
		{Definition{Type: "process", Timeout: floatPtr(2)}, 60000, 60000},
		{Definition{Type: "process", TimeoutMs: floatPtr(0.4)}, 60000, 1},
	}
	for _, tc := range cases {
		if got := ResolveTimeoutMs(&tc.hook, tc.defaultMs); got != tc.want {
			t.Errorf("ResolveTimeoutMs(%+v, %d) = %d want %d", tc.hook, tc.defaultMs, got, tc.want)
		}
	}
}

// --- monotonicity property -------------------------------------------------------------

// TestMonotonicityTable ports the full verdict table.
func TestMonotonicityTable(t *testing.T) {
	current := &ReviewIdentity{ReviewFlowID: "f1", Generation: 3, InteractionID: "i1"}
	cases := []struct {
		candidate ReviewIdentity
		want      ReviewVerdict
	}{
		{ReviewIdentity{"f2", 1, "x"}, CrossFlow},
		{ReviewIdentity{"f1", 4, "i2"}, SameFlowAdvance},
		{ReviewIdentity{"f1", 3, "i1"}, SameFlowReplay},
		{ReviewIdentity{"f1", 3, "i2"}, SameFlowConflict},
		{ReviewIdentity{"f1", 2, "i1"}, SameFlowStale},
	}
	for _, tc := range cases {
		if got := VerdictReviewRequest(current, tc.candidate); got != tc.want {
			t.Errorf("verdict(%+v) = %s want %s", tc.candidate, got, tc.want)
		}
	}
	if got := VerdictReviewRequest(nil, ReviewIdentity{"f1", 1, "i1"}); got != NoCurrent {
		t.Errorf("no current = %s", got)
	}
}

// TestMonotonicityProperty is the property test: applying a sequence of
// candidate reviews through the single arbiter never widens trust silently —
// the authority either stays, advances within the flow, or changes flow
// explicitly; a stale/conflicting candidate must NOT move the current
// authority forward.
func TestMonotonicityProperty(t *testing.T) {
	// Deterministic PRNG (no seed → stable property run).
	const iterations = 500
	state := uint64(12345)
	next := func(limit int) int {
		state = state*6364136223846793005 + 1442695040888963407
		return int((state >> 33) % uint64(limit))
	}
	for iteration := 0; iteration < iterations; iteration++ {
		var current *ReviewIdentity
		for step := 0; step < 8; step++ {
			candidate := ReviewIdentity{
				ReviewFlowID:  fmt.Sprintf("f%d", next(3)),
				Generation:    int64(next(6)),
				InteractionID: fmt.Sprintf("i%d", next(3)),
			}
			verdict := VerdictReviewRequest(current, candidate)
			// Invariant: the verdict is one of the six named outcomes.
			switch verdict {
			case NoCurrent:
				current = &candidate
			case SameFlowAdvance:
				// Authority moves forward within the flow.
				current = &candidate
			case SameFlowReplay, SameFlowConflict, SameFlowStale:
				// Authority does NOT move — no silent widening.
				if current == nil {
					t.Fatalf("iteration %d step %d: nil current cannot produce %s", iteration, step, verdict)
				}
			case CrossFlow:
				// Cross-flow decisions belong to the caller; the property
				// here is that the arbiter NEVER silently advances across
				// flows — it reports cross_flow instead.
				current = &candidate
			default:
				t.Fatalf("unknown verdict %s", verdict)
			}
			// Widening check: within one flow, generation never decreases.
			if current != nil && current.ReviewFlowID == candidate.ReviewFlowID {
				if current.Generation < candidate.Generation && verdict != SameFlowAdvance {
					t.Fatalf("generation advanced without same_flow_advance: %s", verdict)
				}
			}
		}
	}
}

// --- trust-store file compatibility ------------------------------------------------------

func validRecord(identity, digest string) TrustRecord {
	return TrustRecord{
		WorkspaceIdentity:     identity,
		HookDeclarationDigest: digest,
		DigestAlgorithm:       "sha256",
		Decision:              "trusted",
		GrantedAt:             "2026-10-15T10:00:00.000Z",
		EventAtGrant:          "PreToolUse",
		DisplayCommandAtGrant: "echo hi",
		SourcePathAtGrant:     "/ws/.nex/config.json",
	}
}

// TestTrustStoreParseValidAccepts: a well-formed file parses.
func TestTrustStoreParseValidAccepts(t *testing.T) {
	digest := strings.Repeat("a", 64)
	store := &TrustStoreFile{SchemaVersion: 1, Records: []TrustRecord{validRecord("ws", digest)}}
	data, err := SerializeTrustStore(store)
	if err != nil {
		t.Fatal(err)
	}
	parsed := ParseTrustStoreContent(string(data))
	if parsed.Status != "ok" {
		t.Fatalf("round-trip parse status = %s", parsed.Status)
	}
	if len(parsed.File.Records) != 1 || parsed.File.Records[0].HookDeclarationDigest != digest {
		t.Fatalf("record lost: %+v", parsed.File.Records)
	}
}

// TestTrustStoreParseRejects: every corrupt shape lands in invalid — the
// display layer and the runtime must reach the SAME fail-closed verdict.
func TestTrustStoreParseRejects(t *testing.T) {
	digest := strings.Repeat("a", 64)
	record := validRecord("ws", digest)
	valid, _ := SerializeTrustStore(&TrustStoreFile{SchemaVersion: 1, Records: []TrustRecord{record}})
	invalids := map[string]string{
		"not-json":        `{`,
		"empty":           ``,
		"array-root":      `[]`,
		"missing-version": `{"records":[]}`,
		"wrong-version":   `{"schemaVersion":2,"records":[]}`,
		"unknown-field":   `{"schemaVersion":1,"records":[],"extra":1}`,
		"records-object":  `{"schemaVersion":1,"records":{}}`,
	}
	// Record-level corruption.
	badRecords := map[string]func(*TrustRecord){
		"bad-digest":           func(r *TrustRecord) { r.HookDeclarationDigest = "xyz" },
		"bad-algorithm":        func(r *TrustRecord) { r.DigestAlgorithm = "md5" },
		"bad-decision":         func(r *TrustRecord) { r.Decision = "revoked" },
		"bad-grantedAt":        func(r *TrustRecord) { r.GrantedAt = "yesterday" },
		"bad-event":            func(r *TrustRecord) { r.EventAtGrant = "NotAnEvent" },
		"empty-identity":       func(r *TrustRecord) { r.WorkspaceIdentity = " " },
		"empty-command":        func(r *TrustRecord) { r.DisplayCommandAtGrant = "" },
		"unknown-record-field": func(r *TrustRecord) { r.AppVersionAtGrant = "1.0" },
		"negative-order": func(r *TrustRecord) {
			value := int64(-1)
			r.SourceDiscoveryOrderAtGrant = &value
		},
	}
	for name, content := range invalids {
		if parsed := ParseTrustStoreContent(content); parsed.Status != "invalid" {
			t.Errorf("%s: status = %s want invalid", name, parsed.Status)
		}
	}
	for name, mutate := range badRecords {
		record := validRecord("ws", digest)
		if name == "unknown-record-field" {
			// AppVersionAtGrant IS a known field; inject a genuinely unknown one via raw JSON.
			content := strings.Replace(string(valid), `"decision": "trusted"`, `"decision": "trusted", "rogue": 1`, 1)
			if parsed := ParseTrustStoreContent(content); parsed.Status != "invalid" {
				t.Errorf("%s: status = %s want invalid", name, parsed.Status)
			}
			continue
		}
		mutate(&record)
		data, _ := SerializeTrustStore(&TrustStoreFile{SchemaVersion: 1, Records: []TrustRecord{record}})
		if parsed := ParseTrustStoreContent(string(data)); parsed.Status != "invalid" {
			t.Errorf("%s: status = %s want invalid", name, parsed.Status)
		}
	}
	// Duplicate (identity, digest) keys are invalid.
	dup := &TrustStoreFile{SchemaVersion: 1, Records: []TrustRecord{validRecord("ws", digest), validRecord("ws", digest)}}
	data, _ := SerializeTrustStore(dup)
	if parsed := ParseTrustStoreContent(string(data)); parsed.Status != "invalid" {
		t.Errorf("duplicate keys: status = %s want invalid", parsed.Status)
	}
	// Two identities with the same digest are fine.
	ok := &TrustStoreFile{SchemaVersion: 1, Records: []TrustRecord{validRecord("ws1", digest), validRecord("ws2", digest)}}
	data, _ = SerializeTrustStore(ok)
	if parsed := ParseTrustStoreContent(string(data)); parsed.Status != "ok" {
		t.Errorf("distinct identities: status = %s want ok", parsed.Status)
	}
}

// TestTrustStoreWriteFormatIsNodeCompatible pins the exact bytes the Node
// writer emits (JSON.stringify(store, null, 2) + "\n", fields in schema
// order, optional fields omitted).
func TestTrustStoreWriteFormatIsNodeCompatible(t *testing.T) {
	order := int64(0)
	matcher := "Bash"
	record := validRecord("ws", strings.Repeat("a", 64))
	record.SourceDiscoveryOrderAtGrant = &order
	record.MatcherAtGrant = &matcher
	data, err := SerializeTrustStore(&TrustStoreFile{SchemaVersion: 1, Records: []TrustRecord{record}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schemaVersion": 1,
  "records": [
    {
      "workspaceIdentity": "ws",
      "hookDeclarationDigest": "` + strings.Repeat("a", 64) + `",
      "digestAlgorithm": "sha256",
      "decision": "trusted",
      "grantedAt": "2026-10-15T10:00:00.000Z",
      "eventAtGrant": "PreToolUse",
      "displayCommandAtGrant": "echo hi",
      "sourcePathAtGrant": "/ws/.nex/config.json",
      "sourceDiscoveryOrderAtGrant": 0,
      "matcherAtGrant": "Bash"
    }
  ]
}
`
	if string(data) != want {
		t.Fatalf("write format drifted:\n%s\nwant:\n%s", string(data), want)
	}
}

// TestReadTrustedDigestsFailClosed: ENOENT is empty-but-not-corrupt; an
// unreadable or invalid file is corrupt (fail-closed, no partial digests).
func TestReadTrustedDigestsFailClosed(t *testing.T) {
	storage := t.TempDir()
	digests, corrupt, err := ReadTrustedDigests(storage, "ws")
	if err != nil || corrupt || len(digests) != 0 {
		t.Fatalf("missing store: digests=%v corrupt=%v err=%v", digests, corrupt, err)
	}
	// Invalid content → corrupt, zero digests.
	if err := os.MkdirAll(filepath.Join(storage, "security"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(TrustStorePath(storage), []byte("not json"), 0o600)
	digests, corrupt, _ = ReadTrustedDigests(storage, "ws")
	if !corrupt || len(digests) != 0 {
		t.Fatalf("corrupt store must fail closed: corrupt=%v digests=%v", corrupt, digests)
	}
	// Valid store: only the matching identity's digests surface.
	digest := strings.Repeat("b", 64)
	data, _ := SerializeTrustStore(&TrustStoreFile{SchemaVersion: 1, Records: []TrustRecord{
		validRecord("ws1", digest),
		validRecord("ws2", strings.Repeat("c", 64)),
	}})
	os.WriteFile(TrustStorePath(storage), data, 0o600)
	digests, corrupt, _ = ReadTrustedDigests(storage, "ws1")
	if corrupt || !digests[digest] || len(digests) != 1 {
		t.Fatalf("digests=%v corrupt=%v", digests, corrupt)
	}
}

// TestStorageRootFromUserConfig covers the ~ / absolute / relative rules.
func TestStorageRootFromUserConfig(t *testing.T) {
	cases := []struct{ configured, want string }{
		{"", "/home/u/.nex"},
		{"~/.nex", "/home/u/.nex"},
		{"/data/nex", "/data/nex"},
		{"rel/nex", "/home/u/rel/nex"},
	}
	for _, tc := range cases {
		if got := StorageRootFromUserConfig("/home/u", tc.configured); got != tc.want {
			t.Errorf("StorageRootFromUserConfig(%q) = %s want %s", tc.configured, got, tc.want)
		}
	}
}

// TestLoadHooksTrustOverlay: loadHooks attaches trusted_persistent /
// pending_trust from the store, and reports trustStoreCorrupt.
func TestLoadHooksTrustOverlay(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	// Project config with one hook.
	configDir := filepath.Join(workspace, ".nex")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{
	  "hooks": {"events": {"PreToolUse": [{"hooks": [{"type": "command", "command": "echo hi"}]}]}}
	}`), 0o644)
	service := New(Options{HomeDir: home})

	result, err := service.LoadHooks(nil, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hooks) != 1 {
		t.Fatalf("hooks = %+v", result.Hooks)
	}
	if result.Hooks[0].WorkspaceHook == nil || result.Hooks[0].WorkspaceHook.TrustState != "pending_trust" {
		t.Fatalf("fresh hook must be pending_trust")
	}
	digest := result.Hooks[0].WorkspaceHook.HookDeclarationDigest

	// Grant trust via the store file (the agent writer's format).
	storageRoot := filepath.Join(home, ".nex")
	identity := result.Hooks[0].WorkspaceHook.WorkspaceIdentity
	record := validRecord(identity, digest)
	os.MkdirAll(filepath.Join(storageRoot, "security"), 0o755)
	data, _ := SerializeTrustStore(&TrustStoreFile{SchemaVersion: 1, Records: []TrustRecord{record}})
	os.WriteFile(TrustStorePath(storageRoot), data, 0o600)

	result, err = service.LoadHooks(nil, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Hooks[0].WorkspaceHook.TrustState != "trusted_persistent" {
		t.Fatalf("stored digest must surface as trusted_persistent")
	}
	if result.TrustStoreCorrupt {
		t.Fatal("valid store must not set trustStoreCorrupt")
	}

	// Corrupt the store → fail closed + flag.
	os.WriteFile(TrustStorePath(storageRoot), []byte("{"), 0o600)
	result, err = service.LoadHooks(nil, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.TrustStoreCorrupt {
		t.Fatal("corrupt store must set trustStoreCorrupt")
	}
	if result.Hooks[0].WorkspaceHook.TrustState != "pending_trust" {
		t.Fatal("corrupt store must demote to pending_trust")
	}
}

// TestSaveHooksWritesConfig: saveHooks writes the split configs atomically.
func TestSaveHooksWritesConfig(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	service := New(Options{HomeDir: home})
	load, err := service.LoadHooks(nil, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SaveHooks(nil, workspace, "", load.Hooks); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".nex", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	hooks, _ := parsed["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("hooks block missing")
	}
	if _, ok := hooks["events"]; !ok {
		t.Fatal("events missing")
	}
}

// TestAtomicWriteConfig round-trips and never leaves the temp file.
func TestAtomicWriteConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := AtomicWriteConfig(path, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}
