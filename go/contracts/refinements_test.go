package contracts

import (
	"sort"
	"testing"
)

// TestRefinementRegistryMatchesGeneratedList requires RefinedSchemas (emitted from the
// TypeScript schemas that carry `.refine`/`.superRefine`) and the hand-written registry
// in refinements.go to name exactly the same set. A new refine in TypeScript fails here
// until it is ported, instead of silently accepting invalid values.
func TestRefinementRegistryMatchesGeneratedList(t *testing.T) {
	want := append([]string(nil), RefinedSchemas...)
	sort.Strings(want)
	got := RefinementIDs()

	if len(want) != len(got) {
		t.Fatalf("refine count mismatch: generated %d %v, implemented %d %v", len(want), want, len(got), got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("refine id mismatch at %d: generated %q, implemented %q", i, want[i], got[i])
		}
	}
	if len(got) == 0 {
		t.Fatal("no refinements implemented; the corpus would then hide all cross-field rules")
	}
}

// TestUnsupportedNodeTypesEmpty fails if the emitter could not describe a zod node type
// structurally. A non-empty list means part of the surface is not really validated.
func TestUnsupportedNodeTypesEmpty(t *testing.T) {
	if len(UnsupportedNodeTypes) != 0 {
		t.Fatalf("emitter could not describe node types: %v", UnsupportedNodeTypes)
	}
}
