package runner

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

type stubTextProvider struct {
	text string
}

func (p *stubTextProvider) Driver() ai.Driver { return ai.DriverOpenAIChat }
func (p *stubTextProvider) Stream(_ context.Context, _ *ai.StreamRequest) (<-chan ai.StreamEvent, error) {
	ch := make(chan ai.StreamEvent, 2)
	ch <- ai.StreamEvent{Type: ai.StreamEventTextDelta, TextDelta: p.text}
	ch <- ai.StreamEvent{Type: ai.StreamEventDone}
	close(ch)
	return ch, nil
}

func TestFormatMemoryPromptLinesProgressiveDisclosure(t *testing.T) {
	small := []store.MemoryRecord{
		{ID: "mem_1", Scope: "global", Category: "preference", Title: "Static Go", Content: "Use CGO_ENABLED=0"},
		{ID: "mem_2", Scope: "project", Category: "convention", Title: "Tests", Content: "Run go test -race"},
	}
	linesSmall := formatMemoryPromptLines(small)
	if len(linesSmall) != 2 || !strings.Contains(linesSmall[0], "Use CGO_ENABLED=0") {
		t.Fatalf("expected inline mode for small memory bank, got %+v", linesSmall)
	}

	large := make([]store.MemoryRecord, 0, 8)
	for i := 0; i < 8; i++ {
		large = append(large, store.MemoryRecord{
			ID:       fmt.Sprintf("mem_%d", i),
			Scope:    "project",
			Category: "fact",
			Title:    fmt.Sprintf("Fact %d", i),
			Content:  "Detailed explanation that should only load via memory_read",
		})
	}
	linesLarge := formatMemoryPromptLines(large)
	if len(linesLarge) != 8 {
		t.Fatalf("expected 8 compact index lines, got %d", len(linesLarge))
	}
	if strings.Contains(linesLarge[0], "Detailed explanation") || !strings.Contains(linesLarge[0], "[mem_0] (project/fact) Fact 0") {
		t.Fatalf("expected compact index without full body, got %q", linesLarge[0])
	}
}

func TestExtractMemoriesAsyncAndSecretFilter(t *testing.T) {
	mockJSON := "```json\n" + `[
		{"scope":"global","category":"preference","title":"Commit Often","content":"Always commit after each feature batch"},
		{"scope":"global","category":"fact","title":"Leaked Key","content":"Use key sk-proj-abcdefghijklmnopqrstuvwxyz999999"}
	]` + "\n```"

	node, err := NewNode(Config{
		RunnerID:         "run_mem_test",
		DataDir:          t.TempDir(),
		ProviderOverride: &stubTextProvider{text: mockJSON},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	node.ApplyConfigSnapshot(context.Background(), &protocol.ConfigSnapshot{
		MemoriesSynced: true,
		MemorySettings: &protocol.MemoryConfigSync{AutoExtract: true},
	})

	node.extractMemoriesAsync("/tmp/ws", "Please remember to commit promptly after every batch of features.", "Understood, I will commit after each batch.", "openai", "gpt-5")

	mems, err := node.store.ListMemories(context.Background(), "/tmp/ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 || mems[0].Title != "Commit Often" {
		t.Fatalf("expected exactly 1 clean memory (secret filtered out), got %+v", mems)
	}
}
