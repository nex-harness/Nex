package runner

import (
	"context"
	"testing"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/store"
)

func TestSubagentModelResolution(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	for _, pr := range []store.ProviderRecord{
		{ID: "prov_a", Name: "Alpha", Driver: ai.DriverOpenAIChat, Enabled: true},
		{ID: "prov_main", Name: "Main", Driver: ai.DriverOpenAIChat, Enabled: true},
		{ID: "prov_fast", Name: "Fast", Driver: ai.DriverOpenAIChat, Enabled: true},
		{ID: "prov_disabled", Name: "Disabled", Driver: ai.DriverOpenAIChat, Enabled: false},
	} {
		if err := st.UpsertProvider(ctx, pr); err != nil {
			t.Fatalf("upsert provider %s: %v", pr.ID, err)
		}
	}

	if err := st.UpsertModelsBatch(ctx, []ai.ModelMeta{
		{ID: "alpha-1", ProviderID: "prov_a", DisplayName: "Alpha 1", Source: "custom"},
		{ID: "main-pro", ProviderID: "prov_main", DisplayName: "Main Pro", Source: "custom"},
		{ID: "mini-fast", ProviderID: "prov_fast", DisplayName: "Mini Fast", Source: "custom"},
		{ID: "off-model", ProviderID: "prov_disabled", DisplayName: "Off Model", Source: "custom"},
	}, true); err != nil {
		t.Fatalf("upsert models: %v", err)
	}

	parentSess, err := st.CreateOrUpdateSession(ctx, store.SessionRecord{
		ID:            "sess_parent",
		RunnerID:      "run_1",
		Title:         "Parent Session",
		WorkspacePath: "/tmp/ws",
		ProviderID:    "prov_main",
		ModelID:       "main-pro",
		Status:        "idle",
	})
	if err != nil {
		t.Fatalf("create parent session: %v", err)
	}

	w := &DefaultRuntimeWiring{node: &Node{store: st}}

	// 1. Built-in explore & worker inherit parent session model when no override exists.
	for _, agentType := range []string{"explore", "worker"} {
		prov, mod, effort := w.resolveSubagentModel(ctx, &parentSess, agentType, "")
		if prov != "prov_main" || mod != "main-pro" || effort != "" {
			t.Fatalf("%s default mismatch: got (%q, %q, %q), want (prov_main, main-pro, \"\")", agentType, prov, mod, effort)
		}
	}

	// 2. Profile override resolves configured ModelRef + ThinkingEffort.
	if err := st.UpsertAgentProfile(ctx, store.AgentProfile{
		Slug:           "explore",
		DisplayName:    "Explore",
		ModelRef:       "prov_fast::mini-fast",
		ThinkingEffort: "low",
		IsolationMode:  "shared",
	}); err != nil {
		t.Fatalf("upsert explore profile: %v", err)
	}
	prov, mod, effort := w.resolveSubagentModel(ctx, &parentSess, "explore", "")
	if prov != "prov_fast" || mod != "mini-fast" || effort != "low" {
		t.Fatalf("profile override mismatch: got (%q, %q, %q), want (prov_fast, mini-fast, low)", prov, mod, effort)
	}

	// 3. Explicit reqModelRef overrides profile; "inherit" falls back to parent model.
	prov, mod, effort = w.resolveSubagentModel(ctx, &parentSess, "explore", "prov_a::alpha-1")
	if prov != "prov_a" || mod != "alpha-1" || effort != "low" {
		t.Fatalf("explicit req override mismatch: got (%q, %q, %q), want (prov_a, alpha-1, low)", prov, mod, effort)
	}
	prov, mod, effort = w.resolveSubagentModel(ctx, &parentSess, "explore", "inherit")
	if prov != "prov_main" || mod != "main-pro" || effort != "low" {
		t.Fatalf("inherit keyword mismatch: got (%q, %q, %q), want (prov_main, main-pro, low)", prov, mod, effort)
	}

	// 4. Stale / disabled-provider override safely falls back to parent session's main model.
	if err := st.UpsertAgentProfile(ctx, store.AgentProfile{
		Slug:          "worker",
		DisplayName:   "Worker",
		ModelRef:      "prov_deleted::gone",
		IsolationMode: "shared",
	}); err != nil {
		t.Fatalf("upsert stale worker profile: %v", err)
	}
	prov, mod, effort = w.resolveSubagentModel(ctx, &parentSess, "worker", "")
	if prov != "prov_main" || mod != "main-pro" || effort != "" {
		t.Fatalf("stale override fallback mismatch: got (%q, %q, %q)", prov, mod, effort)
	}
	prov, mod, effort = w.resolveSubagentModel(ctx, &parentSess, "worker", "prov_disabled::off-model")
	if prov != "prov_main" || mod != "main-pro" || effort != "" {
		t.Fatalf("disabled provider fallback mismatch: got (%q, %q, %q)", prov, mod, effort)
	}
}
