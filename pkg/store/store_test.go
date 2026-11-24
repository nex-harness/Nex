package store

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

func TestStoreBlobsRegistriesArchiveAndStats(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer st.Close()

	// 1. Preferences & Config Revision
	prefs := DefaultUserPreferences()
	prefs.AutoArchiveAfterDays = 14
	prefs.Theme = "light"
	if err := st.SaveUserPreferences(ctx, prefs); err != nil {
		t.Fatal(err)
	}
	gotPrefs, err := st.GetUserPreferences(ctx)
	if err != nil || gotPrefs.AutoArchiveAfterDays != 14 || gotPrefs.Theme != "light" {
		t.Fatalf("prefs mismatch: %+v err=%v", gotPrefs, err)
	}

	// 2. Providers & Models (verifying custom override survives models.dev batch sync)
	if err := st.UpsertProvider(ctx, ProviderRecord{
		ID: "anthropic", Name: "Anthropic", Driver: ai.DriverAnthropic, BaseURL: "https://api.anthropic.com/v1", APIKey: "sk-plain", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertModelsBatch(ctx, []ai.ModelMeta{
		{ID: "claude-sonnet-4-5", ProviderID: "anthropic", DisplayName: "My Custom Price", Source: "custom", ContextWindow: 200000, Pricing: ai.ModelPricing{InputPer1M: 9.9}},
	}, true); err != nil {
		t.Fatal(err)
	}
	// Sync models.dev with overwriteCustom=false -> custom model must keep 9.9!
	seed, _ := ai.EmbeddedModelsCatalog()
	var antSeed []ai.ModelMeta
	for _, m := range seed {
		if m.ProviderID == "anthropic" {
			antSeed = append(antSeed, m)
		}
	}
	if err := st.UpsertModelsBatch(ctx, antSeed, false); err != nil {
		t.Fatal(err)
	}
	customMod, err := st.GetModel(ctx, "anthropic", "claude-sonnet-4-5")
	if err != nil || customMod.Pricing.InputPer1M != 9.9 {
		t.Fatalf("custom model override was clobbered: %+v err=%v", customMod, err)
	}

	// 3. Runners & Resource Registries
	_ = st.UpsertRunner(ctx, RunnerRecord{ID: "run_mac", Name: "MacBook Pro", OS: "darwin", Arch: "arm64", Status: "online", Tags: []string{"dev"}})
	_ = st.UpsertAgentProfile(ctx, AgentProfile{Slug: "explore", DisplayName: "Explore", ToolsAllow: []string{"read", "grep"}})
	_ = st.UpsertSkill(ctx, SkillRecord{Name: "browser", Description: "Control browser", ContentMD: "# Skill", Enabled: true})
	_ = st.UpsertPlugin(ctx, PluginRecord{Name: "gofmt", SourceTS: "export default ()=>{};", Enabled: true})
	_ = st.UpsertMCPServer(ctx, MCPServerRecord{Name: "github", HostMode: "server_hosted", Transport: "http", Enabled: true})
	_, _ = st.UpsertCronJob(ctx, CronJobRecord{RunnerID: "run_mac", Title: "每20分钟巡检", CronExpr: "*/20 * * * *", Prompt: "check", Enabled: true, Recurring: true})

	rev, _ := st.GetConfigRev(ctx)
	if rev < 5 {
		t.Fatalf("expected config_rev >= 5, got %d", rev)
	}

	// 4. CAS Zstd Blob deduplication
	largePrompt := []byte(strings.Repeat("System instructions block.\n", 1000))
	sysHash, _, err := st.PutBlob(ctx, largePrompt, "text/plain", 0)
	if err != nil {
		t.Fatal(err)
	}
	rawTraceHash, _, err := st.PutBlob(ctx, []byte(`{"raw":"trace_payload"}`), "application/json", 0)
	if err != nil {
		t.Fatal(err)
	}
	readBack, _, err := st.GetBlob(ctx, sysHash)
	if err != nil || string(readBack) != string(largePrompt) {
		t.Fatalf("CAS GetBlob round-trip failed: err=%v", err)
	}

	// 5. Concurrent Session Events + Outbox + Subagent child session
	rootSess, err := st.CreateOrUpdateSession(ctx, SessionRecord{
		ID: "sess_root", RunnerID: "run_mac", Title: "Build Nex", WorkspacePath: "/tmp/ws",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateOrUpdateSession(ctx, SessionRecord{
		ID: "sess_child_sub", RunnerID: "run_mac", ParentSessionID: rootSess.ID, Depth: 1, Title: "Explore subagent",
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.AppendSessionEvent(ctx, SessionEvent{
				SessionID: rootSess.ID,
				TurnIdx:   1,
				EventType: "message_update",
				Payload:   json.RawMessage(`{"delta":"hi"}`),
			}, true)
			if err != nil {
				t.Errorf("AppendSessionEvent: %v", err)
			}
		}()
	}
	wg.Wait()

	outbox, err := st.ListUnackedOutbox(ctx, 50)
	if err != nil || len(outbox) != 10 {
		t.Fatalf("expected 10 outbox entries, got %d err=%v", len(outbox), err)
	}
	if err := st.AckOutboxUpTo(ctx, outbox[len(outbox)-1].ID); err != nil {
		t.Fatal(err)
	}
	remOutbox, _ := st.ListUnackedOutbox(ctx, 50)
	if len(remOutbox) != 0 {
		t.Fatalf("expected 0 unacked outbox after ack, got %d", len(remOutbox))
	}

	// 6. RecordTurnTelemetry + Hourly Rollups + Timezone-aware Today/7D/30D stats
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 11, 18, 15, 30, 0, 0, shanghai)
	fiveDaysAgo := now.AddDate(0, 0, -5)

	if err := st.RecordTurnTelemetry(ctx, TurnTelemetry{
		SessionID:            rootSess.ID,
		TurnIdx:              1,
		RunnerID:             "run_mac",
		ProviderID:           "anthropic",
		ModelID:              "claude-sonnet-4-5",
		TriggerSource:        "chat",
		ContextWindow:        200000,
		TotalContextTokens:   50000,
		WindowUtilizationPct: 25.0,
		InputTokens:          10000,
		OutputTokens:         2000,
		CacheReadTokens:      40000,
		CacheHitRatePct:      80.0,
		EstimatedCostUSD:     0.072,
		CacheSavedUSD:        0.108,
		Breakdown: []ContextCategoryItem{
			{Category: "system_prompt", Tokens: 4000, Pct: 8.0, BlobHash: sysHash},
			{Category: "messages", Tokens: 46000, Pct: 92.0},
		},
		SystemPromptBlobHash: sysHash,
		RawTraceBlobHash:     rawTraceHash,
		RecordedAt:           now.Unix(),
	}); err != nil {
		t.Fatalf("RecordTurnTelemetry today: %v", err)
	}

	if err := st.RecordTurnTelemetry(ctx, TurnTelemetry{
		SessionID:          "sess_child_sub",
		TurnIdx:            1,
		RunnerID:           "run_mac",
		ProviderID:         "openai",
		ModelID:            "gpt-5-codex",
		TriggerSource:      "subagent",
		ContextWindow:      400000,
		TotalContextTokens: 20000,
		InputTokens:        20000,
		OutputTokens:       1000,
		EstimatedCostUSD:   0.035,
		Breakdown:          []ContextCategoryItem{{Category: "messages", Tokens: 20000, Pct: 100.0}},
		RecordedAt:         fiveDaysAgo.Unix(),
	}); err != nil {
		t.Fatalf("RecordTurnTelemetry 5d ago: %v", err)
	}

	todayStats, err := st.QueryUsageSummary(ctx, "run_mac", WindowToday, now, shanghai)
	if err != nil || todayStats.TurnCount != 1 || math.Abs(todayStats.EstimatedCostUSD-0.072) > 1e-6 {
		t.Fatalf("today stats mismatch: %+v err=%v", todayStats, err)
	}

	weekStats, err := st.QueryUsageSummary(ctx, "run_mac", Window7D, now, shanghai)
	if err != nil || weekStats.TurnCount != 2 || len(weekStats.Models) != 2 {
		t.Fatalf("7d stats mismatch: %+v err=%v", weekStats, err)
	}

	// 7. Cold/Hot Physical Archive -> Unarchive -> Permanent Delete with CAS Blob GC!
	if err := st.ArchiveSession(ctx, rootSess.ID); err != nil {
		t.Fatalf("ArchiveSession failed: %v", err)
	}
	// Must be gone from main.db
	if _, err := st.GetSession(ctx, rootSess.ID); err == nil {
		t.Fatalf("archived session still present in main.db!")
	}
	// Must be present in archive.db AND usage_rollups_hourly in main.db must remain intact!
	archivedList, err := st.ListArchivedSessions(ctx, 20, 0)
	if err != nil || len(archivedList) != 1 || archivedList[0].ID != rootSess.ID {
		t.Fatalf("expected 1 archived session, got %+v err=%v", archivedList, err)
	}
	weekAfterArchive, _ := st.QueryUsageSummary(ctx, "run_mac", Window7D, now, shanghai)
	if weekAfterArchive.TurnCount != 2 {
		t.Fatalf("archiving session corrupted usage_rollups_hourly!")
	}

	// Unarchive back to main.db
	if err := st.UnarchiveSession(ctx, rootSess.ID); err != nil {
		t.Fatalf("UnarchiveSession failed: %v", err)
	}
	restored, err := st.GetSession(ctx, rootSess.ID)
	if err != nil || restored.Title != "Build Nex" {
		t.Fatalf("Unarchive did not restore session: %+v err=%v", restored, err)
	}

	// Archive again and Permanently Delete -> verify orphaned CAS blobs are physically deleted!
	if err := st.ArchiveSession(ctx, rootSess.ID); err != nil {
		t.Fatal(err)
	}
	if !st.HasBlob(ctx, rawTraceHash) {
		t.Fatalf("expected rawTraceHash blob to exist before permanent delete")
	}
	if err := st.DeleteArchivedSessionPermanently(ctx, rootSess.ID); err != nil {
		t.Fatalf("DeleteArchivedSessionPermanently failed: %v", err)
	}
	if st.HasBlob(ctx, rawTraceHash) || st.HasBlob(ctx, sysHash) {
		t.Fatalf("expected orphaned CAS .zst blobs to be reclaimed after permanent delete!")
	}
}

func TestFileCheckpointRewindSearchAndExport(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer st.Close()

	workDir := t.TempDir()
	existingFile := filepath.Join(workDir, "app.go")
	createdFile := filepath.Join(workDir, "new_feature.go")
	if err := os.WriteFile(existingFile, []byte("v1 original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := st.CreateOrUpdateSession(ctx, SessionRecord{
		ID:            "sess_cp_1",
		RunnerID:      "run_local",
		Title:         "Refactor Auth Middleware",
		WorkspacePath: workDir,
		ModelID:       "claude-sonnet-4-5",
		ProviderID:    "anthropic",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1 (seq 10): edit existingFile from v1 -> v2
	if err := st.RecordFileCheckpoint(ctx, sess.ID, 10, "tc_1", existingFile); err != nil {
		t.Fatalf("RecordFileCheckpoint seq 10: %v", err)
	}
	if err := os.WriteFile(existingFile, []byte("v2 after turn 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Turn 2 (seq 20): edit existingFile v2 -> v3 AND create createdFile
	if err := st.RecordFileCheckpoint(ctx, sess.ID, 20, "tc_2", existingFile); err != nil {
		t.Fatalf("RecordFileCheckpoint seq 20 existing: %v", err)
	}
	if err := st.RecordFileCheckpoint(ctx, sess.ID, 20, "tc_3", createdFile); err != nil {
		t.Fatalf("RecordFileCheckpoint seq 20 created: %v", err)
	}
	if err := os.WriteFile(existingFile, []byte("v3 after turn 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(createdFile, []byte("brand new file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Rewind Turn 2 (beforeSeq=20): existingFile -> v2, createdFile -> deleted
	rw2, err := st.RewindSessionFiles(ctx, sess.ID, 20)
	if err != nil {
		t.Fatalf("RewindSessionFiles(20): %v", err)
	}
	if len(rw2.RestoredFiles) != 1 || rw2.RestoredFiles[0] != existingFile {
		t.Fatalf("expected existingFile restored, got %+v", rw2)
	}
	if len(rw2.DeletedFiles) != 1 || rw2.DeletedFiles[0] != createdFile {
		t.Fatalf("expected createdFile deleted, got %+v", rw2)
	}
	got, _ := os.ReadFile(existingFile)
	if string(got) != "v2 after turn 1\n" {
		t.Fatalf("expected v2 content after rewinding turn 2, got %q", string(got))
	}
	if _, statErr := os.Stat(createdFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected createdFile to be deleted after rewind")
	}

	// Rewind Turn 1 (beforeSeq=10): existingFile -> v1 original
	rw1, err := st.RewindSessionFiles(ctx, sess.ID, 10)
	if err != nil {
		t.Fatalf("RewindSessionFiles(10): %v", err)
	}
	if len(rw1.RestoredFiles) != 1 {
		t.Fatalf("expected 1 restored file on turn 1 rewind, got %+v", rw1)
	}
	got, _ = os.ReadFile(existingFile)
	if string(got) != "v1 original\n" {
		t.Fatalf("expected v1 original content after rewinding turn 1, got %q", string(got))
	}

	// Append conversation events and verify Full-Text Search & Markdown Export
	_, _ = st.AppendSessionEvent(ctx, SessionEvent{
		SessionID: sess.ID,
		TurnIdx:   1,
		EventType: "message_end",
		Payload:   json.RawMessage(`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"Please fix the JWT clock skew validation bug in middleware"}]}}`),
	}, false)
	_, _ = st.AppendSessionEvent(ctx, SessionEvent{
		SessionID: sess.ID,
		TurnIdx:   1,
		EventType: "message_end",
		Payload:   json.RawMessage(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Checking leeway setting"},{"type":"text","text":"Updated leeway to 30 seconds in auth.go."}]}}`),
	}, false)

	hits, err := st.SearchSessions(ctx, "clock skew", 10)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(hits) != 1 || hits[0].Session.ID != sess.ID || hits[0].MatchType != "message" || !strings.Contains(hits[0].Snippet, "clock skew") {
		t.Fatalf("unexpected search hits: %+v", hits)
	}

	md, filename, err := st.ExportSessionMarkdown(ctx, sess.ID)
	if err != nil {
		t.Fatalf("ExportSessionMarkdown: %v", err)
	}
	if filename != "Refactor-Auth-Middleware.md" {
		t.Fatalf("unexpected filename: %q", filename)
	}
	if !strings.Contains(md, "# Refactor Auth Middleware") ||
		!strings.Contains(md, "## User") ||
		!strings.Contains(md, "JWT clock skew") ||
		!strings.Contains(md, "<summary>Thinking</summary>") ||
		!strings.Contains(md, "Updated leeway to 30 seconds") {
		t.Fatalf("unexpected markdown export:\n%s", md)
	}
}

func TestSessionThinkingEffortPersistenceAndArchive(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer st.Close()

	_, err = st.CreateOrUpdateSession(ctx, SessionRecord{
		ID:             "sess_eff_1",
		RunnerID:       "run_local",
		Title:          "Effort Isolation",
		ModelID:        "claude-opus-4-5",
		ProviderID:     "anthropic",
		ThinkingEffort: "low",
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateSession: %v", err)
	}

	got, err := st.GetSession(ctx, "sess_eff_1")
	if err != nil || got.ThinkingEffort != "low" {
		t.Fatalf("expected ThinkingEffort=low from GetSession, got %+v err=%v", got, err)
	}

	listed, err := st.ListSessions(ctx, "", false)
	if err != nil || len(listed) != 1 || listed[0].ThinkingEffort != "low" {
		t.Fatalf("expected ThinkingEffort=low in ListSessions, got %+v err=%v", listed, err)
	}

	if err := st.ArchiveSession(ctx, "sess_eff_1"); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	archived, err := st.ListArchivedSessions(ctx, 10, 0)
	if err != nil || len(archived) != 1 || archived[0].ThinkingEffort != "low" {
		t.Fatalf("expected ThinkingEffort=low in ListArchivedSessions, got %+v err=%v", archived, err)
	}
	if err := st.UnarchiveSession(ctx, "sess_eff_1"); err != nil {
		t.Fatalf("UnarchiveSession: %v", err)
	}
	restored, err := st.GetSession(ctx, "sess_eff_1")
	if err != nil || restored.ThinkingEffort != "low" {
		t.Fatalf("expected ThinkingEffort=low after UnarchiveSession, got %+v err=%v", restored, err)
	}
}

func TestListSessionTelemetryOrdersChronologicallyAcrossTurns(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, err = st.CreateOrUpdateSession(ctx, SessionRecord{
		ID:         "sess_tel_order",
		RunnerID:   "run_local",
		Title:      "Telemetry Order",
		ModelID:    "auto/gemini-4-neon",
		ProviderID: "openai",
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateSession: %v", err)
	}

	if err := st.RecordTurnTelemetry(ctx, TurnTelemetry{
		SessionID:          "sess_tel_order",
		TurnIdx:            40,
		RunnerID:           "run_local",
		ProviderID:         "openai",
		ModelID:            "auto/gemini-4-neon",
		TotalContextTokens: 90434,
		RecordedAt:         1000,
	}); err != nil {
		t.Fatalf("RecordTurnTelemetry 1: %v", err)
	}
	if err := st.RecordTurnTelemetry(ctx, TurnTelemetry{
		SessionID:          "sess_tel_order",
		TurnIdx:            3,
		RunnerID:           "run_local",
		ProviderID:         "openai",
		ModelID:            "auto/gemini-4-neon",
		TotalContextTokens: 607707,
		RecordedAt:         2000,
	}); err != nil {
		t.Fatalf("RecordTurnTelemetry 2: %v", err)
	}

	got, err := st.ListSessionTelemetry(ctx, "sess_tel_order")
	if err != nil {
		t.Fatalf("ListSessionTelemetry: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 telemetry rows, got %d", len(got))
	}
	if got[0].TotalContextTokens != 90434 || got[1].TotalContextTokens != 607707 {
		t.Fatalf("unexpected telemetry order: [0]=%d (turn=%d) [1]=%d (turn=%d)",
			got[0].TotalContextTokens, got[0].TurnIdx, got[1].TotalContextTokens, got[1].TurnIdx)
	}
}
