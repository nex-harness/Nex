package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/store"
)

// runnerMemoryStore wraps Node's local SQLite store so any memory_save / memory_delete
// executed on a Runner immediately replicates back to nex-server (and other Runners / Web UI).
type runnerMemoryStore struct {
	node *Node
}

func (r *runnerMemoryStore) ListMemories(ctx context.Context, workspacePath string) ([]store.MemoryRecord, error) {
	return r.node.store.ListMemories(ctx, workspacePath)
}

func (r *runnerMemoryStore) GetMemory(ctx context.Context, id string) (*store.MemoryRecord, error) {
	return r.node.store.GetMemory(ctx, id)
}

func (r *runnerMemoryStore) SearchMemories(ctx context.Context, workspacePath, query string, limit int) ([]store.MemoryRecord, error) {
	return r.node.store.SearchMemories(ctx, workspacePath, query, limit)
}

func (r *runnerMemoryStore) UpsertMemory(ctx context.Context, rec store.MemoryRecord) (store.MemoryRecord, error) {
	saved, err := r.node.store.UpsertMemory(ctx, rec)
	if err != nil {
		return saved, err
	}
	if r.node.IsOnline() {
		cp := saved
		if env, eErr := protocol.NewEnvelope(protocol.TypeMemorySync, protocol.MemorySyncPayload{
			Action: "upsert",
			Record: &cp,
		}); eErr == nil {
			env.RunnerID = r.node.cfg.RunnerID
			_ = r.node.sendEnvelope(context.Background(), env)
		}
	}
	return saved, nil
}

func (r *runnerMemoryStore) DeleteMemory(ctx context.Context, id string) error {
	if err := r.node.store.DeleteMemory(ctx, id); err != nil {
		return err
	}
	if r.node.IsOnline() {
		if env, eErr := protocol.NewEnvelope(protocol.TypeMemorySync, protocol.MemorySyncPayload{
			Action:   "delete",
			MemoryID: id,
		}); eErr == nil {
			env.RunnerID = r.node.cfg.RunnerID
			_ = r.node.sendEnvelope(context.Background(), env)
		}
	}
	return nil
}

// formatMemoryPromptLines implements hybrid progressive disclosure:
//   - Small memory banks (<= 6 items & <= 1500 content bytes): inline `[id] (scope/category) Title: Content`
//     for zero-extra-turn recall.
//   - Larger memory banks: compact index `[id] (scope/category) Title` so hundreds of entries fit cleanly
//     without truncation, while the model calls `memory_read` on demand.
func formatMemoryPromptLines(mems []store.MemoryRecord) []string {
	if len(mems) == 0 {
		return nil
	}
	totalContentBytes := 0
	for _, m := range mems {
		totalContentBytes += len(m.Content)
	}
	inlineMode := len(mems) <= 6 && totalContentBytes <= 1500

	out := make([]string, 0, len(mems))
	for _, m := range mems {
		if inlineMode {
			out = append(out, fmt.Sprintf("[%s] (%s/%s) %s: %s", m.ID, m.Scope, m.Category, m.Title, m.Content))
		} else {
			out = append(out, fmt.Sprintf("[%s] (%s/%s) %s", m.ID, m.Scope, m.Category, m.Title))
		}
	}
	return out
}

func (n *Node) resolveMemoryConfig(ctx context.Context) protocol.MemoryConfigSync {
	n.mu.Lock()
	cfg := n.memoryCfg
	n.mu.Unlock()
	if cfg.AutoExtract || cfg.ModelID != "" || cfg.ProviderID != "" {
		return cfg
	}
	if prefs, err := n.store.GetUserPreferences(ctx); err == nil {
		return protocol.MemoryConfigSync{
			AutoExtract: prefs.MemoryAutoExtract,
			ProviderID:  prefs.MemoryProviderID,
			ModelID:     prefs.MemoryModelID,
		}
	}
	return cfg
}

type extractedMemoryCandidate struct {
	ID       string `json:"id,omitempty"`
	Scope    string `json:"scope"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Content  string `json:"content"`
}

func (n *Node) extractMemoriesAsync(workDir, userText, assistantText, fallbackProvID, fallbackModelID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	cfg := n.resolveMemoryConfig(ctx)
	if !cfg.AutoExtract {
		return
	}
	uTrim := strings.TrimSpace(userText)
	aTrim := strings.TrimSpace(assistantText)
	if len(uTrim) < 12 || strings.HasPrefix(uTrim, "/compact") || aTrim == "" {
		return
	}

	targetProvID := strings.TrimSpace(cfg.ProviderID)
	targetModelID := strings.TrimSpace(cfg.ModelID)
	if targetProvID == "" || targetModelID == "" {
		targetProvID = fallbackProvID
		targetModelID = fallbackModelID
	}
	prov, _, modelID, _, err := n.resolveProviderAndModel(ctx, targetProvID, targetModelID)
	if err != nil || prov == nil {
		return
	}

	existing, _ := n.store.ListMemories(ctx, workDir)
	var existingIdx strings.Builder
	for _, m := range existing {
		fmt.Fprintf(&existingIdx, "- [%s] (%s/%s) %s\n", m.ID, m.Scope, m.Category, m.Title)
	}

	if len(uTrim) > 1500 {
		uTrim = uTrim[:1500] + "…"
	}
	if len(aTrim) > 2000 {
		aTrim = aTrim[:2000] + "…"
	}

	sysPrompt := "You are a background memory extractor for a coding assistant. " +
		"Inspect the latest user/assistant turn and extract ONLY durable, reusable facts worth remembering in future sessions: " +
		"explicit user preferences, coding conventions, architectural decisions, deployment facts, or recurring constraints. " +
		"NEVER extract transient debugging steps, one-off questions, or secrets/API keys/passwords. " +
		"If nothing durable should be saved, output exactly []. " +
		"If an existing memory should be updated, reuse its exact `id`. " +
		"Output ONLY a valid JSON array (at most 3 items) with schema: " +
		`[{"id":"optional","scope":"global|project","category":"preference|architecture|convention|fact","title":"short title","content":"concise self-contained fact"}].`

	var promptBuf strings.Builder
	if existingIdx.Len() > 0 {
		promptBuf.WriteString("Existing memories index:\n")
		promptBuf.WriteString(existingIdx.String())
		promptBuf.WriteString("\n")
	}
	fmt.Fprintf(&promptBuf, "User message:\n%s\n\nAssistant reply:\n%s\n\nJSON array:", uTrim, aTrim)

	stream, err := prov.Stream(ctx, &ai.StreamRequest{
		Model:           modelID,
		SystemPrompt:    sysPrompt,
		Messages:        []ai.Message{ai.NewUserTextMessage(promptBuf.String())},
		MaxOutputTokens: 600,
	})
	if err != nil {
		return
	}

	var raw strings.Builder
	for ev := range stream {
		if ev.Type == ai.StreamEventTextDelta {
			raw.WriteString(ev.TextDelta)
		} else if ev.Type == ai.StreamEventDone && ev.Message != nil && raw.Len() == 0 {
			raw.WriteString(ev.Message.PlainText())
		}
	}

	candidates := parseExtractedMemoriesJSON(raw.String())
	if len(candidates) == 0 {
		return
	}

	memStore := &runnerMemoryStore{node: n}
	savedCount := 0
	for _, c := range candidates {
		if savedCount >= 3 {
			break
		}
		title := strings.TrimSpace(c.Title)
		content := strings.TrimSpace(c.Content)
		if title == "" || content == "" {
			continue
		}
		if tools.ContainsSecretLikeMaterial(title) || tools.ContainsSecretLikeMaterial(content) {
			continue
		}
		scope := strings.TrimSpace(c.Scope)
		if scope != "global" && scope != "project" {
			scope = "project"
		}
		ws := ""
		if scope == "project" {
			ws = workDir
		}
		cat := strings.TrimSpace(c.Category)
		if cat == "" {
			cat = "fact"
		}
		_, err := memStore.UpsertMemory(ctx, store.MemoryRecord{
			ID:            strings.TrimSpace(c.ID),
			Scope:         scope,
			WorkspacePath: ws,
			Category:      cat,
			Title:         title,
			Content:       content,
		})
		if err == nil {
			savedCount++
		}
	}
}

func parseExtractedMemoriesJSON(raw string) []extractedMemoryCandidate {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end <= start {
		return nil
	}
	var out []extractedMemoryCandidate
	if err := json.Unmarshal([]byte(s[start:end+1]), &out); err != nil {
		return nil
	}
	return out
}
