package server

import (
	"context"
	"testing"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/store"
)

type stubConsolidateProvider struct {
	text string
}

func (p *stubConsolidateProvider) Driver() ai.Driver { return ai.DriverOpenAIChat }
func (p *stubConsolidateProvider) Stream(_ context.Context, _ *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
	ch := make(chan ai.StreamEvent, 2)
	ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: p.text}
	ch <- ai.StreamEvent{Type: ai.StreamEventDone}
	close(ch)
	return ch, nil
}

func TestRunMemoryConsolidation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}

	m1, _ := st.UpsertMemory(ctx, store.MemoryRecord{
		ID:       "mem_a",
		Scope:    "global",
		Category: "preference",
		Title:    "No CGO",
		Content:  "Always build with CGO_ENABLED=0",
	})
	m2, _ := st.UpsertMemory(ctx, store.MemoryRecord{
		ID:       "mem_b",
		Scope:    "global",
		Category: "preference",
		Title:    "Pure Go builds",
		Content:  "Disable CGO when building Go binaries",
	})
	existing := []store.MemoryRecord{m1, m2}

	mockResp := `{
		"summary": "Merged 2 duplicate CGO build preferences into 1 entry.",
		"memories": [
			{"id":"mem_a","scope":"global","category":"preference","title":"Pure-Go static builds","content":"Always build Go binaries with CGO_ENABLED=0."}
		]
	}`

	res, err := srv.runMemoryConsolidation(ctx, &stubConsolidateProvider{text: mockResp}, "gpt-5", existing)
	if err != nil {
		t.Fatalf("runMemoryConsolidation failed: %v", err)
	}
	if res.BeforeCount != 2 || res.AfterCount != 1 || len(res.Memories) != 1 || res.Memories[0].ID != "mem_a" {
		t.Fatalf("unexpected consolidation result: %+v", res)
	}

	stored, err := st.ListMemories(ctx, "")
	if err != nil || len(stored) != 1 || stored[0].Title != "Pure-Go static builds" {
		t.Fatalf("unexpected stored memories after consolidation: %+v err=%v", stored, err)
	}

	// Safety guard: empty result aborts without wiping existing memories
	if _, err := srv.runMemoryConsolidation(ctx, &stubConsolidateProvider{text: `{"summary":"oops","memories":[]}`}, "gpt-5", stored); err == nil {
		t.Fatalf("expected safety error when consolidation returns empty array")
	}
	stillThere, _ := st.ListMemories(ctx, "")
	if len(stillThere) != 1 {
		t.Fatalf("existing memory should remain intact after aborted consolidation, got %+v", stillThere)
	}
}
