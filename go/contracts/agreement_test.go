package contracts

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

// corpusEntry is one line of go/testdata/schema-corpus/corpus.jsonl.
//
// `valid` is the verdict produced by the real zod schema (via `safeParse`) in
// scripts/go-rewrite/make-schema-corpus.mts. The Go validator must agree with it for
// every entry; a disagreement is a Go bug, never a reason to edit the corpus.
type corpusEntry struct {
	Schema   string          `json:"schema"`
	Instance json.RawMessage `json:"instance"`
	Valid    bool            `json:"valid"`
	Why      string          `json:"why"`
}

// TestGoValidatorsAgreeWithZod is the G0.6 agreement test: every recorded instance, every
// structurally generated boundary case, and every mutation must be accepted/rejected
// exactly as zod does.
func TestGoValidatorsAgreeWithZod(t *testing.T) {
	file, err := os.Open("../testdata/schema-corpus/corpus.jsonl")
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	agreed, disagreed := 0, 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry corpusEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode corpus line: %v", err)
		}
		node, ok := Registry.Lookup(entry.Schema)
		if !ok {
			t.Fatalf("corpus references unknown schema %q", entry.Schema)
		}
		decoded, err := decodeInstance(entry.Instance)
		if err != nil {
			t.Fatalf("schema %s: decode instance: %v", entry.Schema, err)
		}
		goValid := Validate(node, decoded) == nil
		if goValid == entry.Valid {
			agreed++
			continue
		}
		disagreed++
		if disagreed <= 40 {
			t.Errorf(
				"schema %s (%s): Go valid=%v, zod valid=%v; instance=%s",
				entry.Schema, entry.Why, goValid, entry.Valid, string(entry.Instance),
			)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if agreed+disagreed < 500 {
		t.Fatalf("corpus looks truncated: %d entries", agreed+disagreed)
	}
	t.Logf("agreement: %d/%d", agreed, agreed+disagreed)
}

// decodeInstance decodes a JSON value the same way a protocol frame is decoded.
func decodeInstance(raw []byte) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}
