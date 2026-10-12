package store_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/tylinux/nex/go/internal/adapters/store"
)

// This file is the Go half of scripts/go-rewrite/crosscheck-sessions.mjs. The
// script sets CROSSCHECK_MODE and CROSSCHECK_DB, builds (or reuses) a database
// with the other engine, and this test reads or writes it. It is skipped in
// normal runs because the env vars are absent.

const (
	envMode = "CROSSCHECK_MODE"
	envDB   = "CROSSCHECK_DB"
)

func openAt(t *testing.T, path string) *store.SessionStore {
	t.Helper()
	db, err := store.Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return store.NewSessionStore(db, store.SessionStoreOptions{Clock: fixedClock{vectorNow}})
}

// TestCrosscheckGoReadsNode: Node wrote a transcript; Go reads it back and
// dumps messages+parts as JSON to stdout for the script to compare.
func TestCrosscheckGoReadsNode(t *testing.T) {
	if os.Getenv(envMode) != "go-reads-node" {
		t.Skip("crosscheck mode not set")
	}
	s := openAt(t, os.Getenv(envDB))
	msgs, err := s.ListMessages(vecSess)
	if err != nil {
		t.Fatal(err)
	}
	type partOut struct {
		ID   string          `json:"id"`
		Seq  *int64          `json:"sequence"`
		Data json.RawMessage `json:"data"`
	}
	type msgOut struct {
		ID    string          `json:"id"`
		Seq   *int64          `json:"sequence"`
		Data  json.RawMessage `json:"data"`
		Parts []partOut       `json:"parts"`
	}
	out := make([]msgOut, 0, len(msgs))
	for _, m := range msgs {
		mo := msgOut{ID: m.MessageID, Seq: m.Sequence, Data: m.Data}
		for _, p := range m.Parts {
			mo.Parts = append(mo.Parts, partOut{ID: p.ID, Seq: p.Sequence, Data: p.Data})
		}
		out = append(out, mo)
	}
	writeJSON(t, os.Getenv("CROSSCHECK_OUT"), out)
}

// TestCrosscheckGoWritesNode: Go writes the text-turn scenario; the script then
// reads it with Node's real SqliteSessionStore.
func TestCrosscheckGoWritesNode(t *testing.T) {
	if os.Getenv(envMode) != "go-writes-node" {
		t.Skip("crosscheck mode not set")
	}
	s := openAt(t, os.Getenv(envDB))
	scenarioRunner("text-turn")(t, s)
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if path == "" {
		t.Fatal("CROSSCHECK_OUT not set")
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
