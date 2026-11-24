package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nex-harness/nex/pkg/store"
)

func TestGlobalInstructionsAPIRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}

	type instructionsResp struct {
		Content string `json:"content"`
		Path    string `json:"path"`
	}

	// 1. Empty store returns empty content plus the display path.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instructions/global", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got instructionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if got.Path != "~/.nex/AGENTS.md" {
		t.Fatalf("expected display path ~/.nex/AGENTS.md, got %q", got.Path)
	}

	// 2. PUT persists the content in SQLite and echoes it back.
	const body = "## Preferences\n\n- Answer in Chinese\n- Prefer small diffs\n"
	req = httptest.NewRequest(http.MethodPut, "/api/v1/instructions/global", strings.NewReader(`{"content":`+quoteJSON(body)+`}`))
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var putResp instructionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if putResp.Content != body {
		t.Fatalf("PUT echo mismatch: %q", putResp.Content)
	}
	if putResp.Path != "~/.nex/AGENTS.md" {
		t.Fatalf("PUT path mismatch: %q", putResp.Path)
	}

	prefs, err := st.GetUserPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if prefs.GlobalInstructions != body {
		t.Fatalf("prefs not persisted: %q", prefs.GlobalInstructions)
	}

	// 3. GET returns the stored content.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/instructions/global", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var getResp instructionsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if getResp.Content != body {
		t.Fatalf("GET round-trip mismatch: %q", getResp.Content)
	}

	// 4. The snapshot carries the content down to Runners.
	snap, err := srv.buildConfigSnapshot(ctx, "run_test")
	if err != nil {
		t.Fatal(err)
	}
	if snap.GlobalInstructions == nil || *snap.GlobalInstructions != body {
		t.Fatalf("snapshot missing global instructions: %v", snap.GlobalInstructions)
	}

	// 5. Unsupported methods are rejected.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/instructions/global", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE expected 405, got %d", rec.Code)
	}
}

func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
