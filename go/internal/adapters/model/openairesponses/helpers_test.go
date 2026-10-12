package openairesponses_test

// Shared helpers for the transport tests. The four symbols the test file uses
// but doesn't define — readVector, statusSink, ProviderKindResponses, the time
// import — live here so the test file reads like a list of cases.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/modelclient"
)

// ProviderKindResponses is the ProviderKind value this adapter answers to. It
// mirrors what cmd/nex-agent/runtime.go writes into Provider.ProviderKind when
// the configured provider's api.type is "openai-responses".
const ProviderKindResponses = "openai-responses"

// statusSink is a StatusSink that records every event so tests can assert on
// the publish sequence.
type statusSink struct {
	events []modelclient.StatusEvent
}

func (s *statusSink) Publish(ev modelclient.StatusEvent) {
	s.events = append(s.events, ev)
}

// statuses returns the publish sequence as the Status strings, in order. It is
// the shape the transport tests assert on.
func (s *statusSink) statuses() []string {
	out := make([]string, 0, len(s.events))
	for _, ev := range s.events {
		out = append(out, ev.Status)
	}
	return out
}

// responsesVector mirrors the shape scripts/go-rewrite/gen-openai-responses-vectors.mts
// writes. Only the fields the transport tests consult are decoded here.
type responsesVector struct {
	// Request is the raw request body the TS SDK produced for the same input.
	Request json.RawMessage `json:"request"`
	// Events is the expected event sequence for the SSE fixtures (consumed by
	// the vectors test).
	Events []map[string]any `json:"events"`
}

// readVector loads the recorded OpenAI Responses vector file produced by
// scripts/go-rewrite/gen-openai-responses-vectors.mts. Tests that depend on it
// skip when the file is absent so the package still builds before the generator
// has been run once.
func readVector(t testing.TB) responsesVector {
	t.Helper()
	root := moduleRoot(t)
	path := filepath.Join(root, "testdata", "vectors", "model-openai-responses.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("vectors not generated yet: %v", err)
	}
	var v responsesVector
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return v
}

// moduleRoot walks up from the test's working directory to the directory that
// contains go.mod so the test can address testdata regardless of where go ran
// it from.
func moduleRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
