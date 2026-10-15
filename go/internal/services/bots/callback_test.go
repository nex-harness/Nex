package bots

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestPipeline builds a pipeline over a temp config, with the webhook provider
// and a bridge whose behaviour the test controls.
type pipelineHarness struct {
	pipeline  *CallbackPipeline
	config    ConfigFile
	repo      *Repo
	dir       string
	clock     time.Time
	bridgeErr atomic.Pointer[error]
	bridgeLog sync.Map
	bridgeMu  sync.Mutex
	processed []InboundMessage
	reply     string
	locale    string
}

func newPipelineHarness(t *testing.T, bots []Config) *pipelineHarness {
	t.Helper()
	dir := t.TempDir()
	repo := NewRepo(dir)
	if err := writeConfigFile(t, repo, ConfigFile{Version: ConfigVersion, Bots: bots}); err != nil {
		t.Fatal(err)
	}
	harness := &pipelineHarness{repo: repo, dir: dir, locale: "zh-CN", reply: "ok"}
	credentials := newStubCredentials()
	harness.pipeline = NewCallbackPipeline(CallbackDeps{
		ReadConfig: repo.ReadConfig,
		LoadCredential: CredentialLoaderFunc(func(_ context.Context, key string) (string, error) {
			return credentials.values[key], nil
		}),
		Bridge: SessionBridgeFunc(func(ctx context.Context, m InboundMessage) ([]OutboundMessage, error) {
			return harness.record(m)
		}),
		Locale: func(context.Context) string { return harness.locale },
		Now:    func() time.Time { return harness.clock },
		Providers: map[Provider]ProviderAdapter{
			ProviderWebhook: &WebhookProvider{Credential: credentials},
		},
	})
	return harness
}

func (h *pipelineHarness) setSecret(key, value string) {
	config, err := h.repo.ReadConfig(context.Background())
	if err != nil {
		panic(err)
	}
	for i := range config.Bots {
		if config.Bots[i].ID == key {
			config.Bots[i].WebhookSecretRef = BuildWebhookSecretKey(key)
		}
	}
	if _, err := h.repo.WriteConfig(context.Background(), config); err != nil {
		panic(err)
	}
	// The stub credential store is rebuilt on the next load; store it directly.
	h.pipeline.deps.LoadCredential = CredentialLoaderFunc(func(_ context.Context, credentialKey string) (string, error) {
		if credentialKey == BuildWebhookSecretKey(key) {
			return value, nil
		}
		return "", nil
	})
}

func (h *pipelineHarness) record(message InboundMessage) ([]OutboundMessage, error) {
	h.bridgeMu.Lock()
	defer h.bridgeMu.Unlock()
	h.processed = append(h.processed, message)
	if err := h.bridgeErr.Load(); err != nil {
		return nil, *err
	}
	if h.reply == "" {
		return nil, nil
	}
	return []OutboundMessage{CreateOutbound(message.Actor, h.reply)}, nil
}

func (h *pipelineHarness) processedCount() int {
	h.bridgeMu.Lock()
	defer h.bridgeMu.Unlock()
	return len(h.processed)
}

func webhookBot() Config {
	return Config{
		ID: "b1", Provider: ProviderWebhook, Enabled: true,
		AllowedWorkspaces: []string{AllBotWorkspaces},
		AllowedCommands:   DefaultAllowedCommands(),
		ReplyMode:         GranularityAssistantChanges,
	}
}

func writeConfigFile(t *testing.T, repo *Repo, config ConfigFile) error {
	t.Helper()
	_, err := repo.WriteConfig(context.Background(), config)
	return err
}

// TestCallbackSecretMismatchRepliesInsteadOfFailing pins the documented rule: a
// wrong webhook secret produces a user-facing reply, NOT a 401, because the
// provider is a bot user that can receive a message.
func TestCallbackSecretMismatchRepliesInsteadOfFailing(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	harness.setSecret("b1", "expected")
	result := harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, map[string]any{
		"botId": "b1", "userId": "u1", "text": "hi", "webhookSecret": "wrong",
	})
	if !result.OK {
		t.Errorf("ok = false; a secret mismatch is a reply, not a transport failure")
	}
	if len(result.Replies) != 1 || result.Replies[0].Text != FormatMessage("zh-CN", "webhookSecretInvalid", nil) {
		t.Fatalf("replies = %+v, want the webhookSecretInvalid message", result.Replies)
	}
	if result.Status != 0 {
		t.Errorf("status = %d, want unset (200)", result.Status)
	}
}

// TestCallbackSecretMatchReachesTheBridge is the other half of the same rule.
func TestCallbackSecretMatchReachesTheBridge(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	harness.setSecret("b1", "expected")
	result := harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, map[string]any{
		"botId": "b1", "userId": "u1", "text": "hi", "webhookSecret": "expected",
	})
	if !result.OK || harness.processedCount() != 1 {
		t.Errorf("ok = %v, processed = %d; want the bridge reached once",
			result.OK, harness.processedCount())
	}
}

// TestCallbackDuplicateDeliveryIsDropped is the inbound dedupe (the TS Bugfix about
// Feishu re-delivering im.message.receive_v1 and Telegram/WeChat replaying a
// message id after a retry).
func TestCallbackDuplicateDeliveryIsDropped(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	payload := map[string]any{
		"botId": "b1", "userId": "u1", "text": "hello", "messageId": "m-1",
	}
	first := harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, payload)
	second := harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, payload)
	if !first.OK || !second.OK {
		t.Errorf("both deliveries must answer ok: %v / %v", first.OK, second.OK)
	}
	if harness.processedCount() != 1 {
		t.Errorf("bridge ran %d times, want 1 (the duplicate must be dropped)", harness.processedCount())
	}
	if len(second.Replies) != 0 {
		t.Errorf("a duplicate must produce no replies, got %d", len(second.Replies))
	}
}

// TestCallbackWithoutMessageIDIsNeverDeduped pins the deliberate other half: a
// message with no provider message id has no key, so two identical texts both
// run (the TS cannot tell them apart either).
func TestCallbackWithoutMessageIDIsNeverDeduped(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	payload := map[string]any{"botId": "b1", "userId": "u1", "text": "hello"}
	harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, payload)
	harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, payload)
	if harness.processedCount() != 2 {
		t.Errorf("bridge ran %d times, want 2 (no message id means no dedupe key)", harness.processedCount())
	}
}

// TestCallbackDedupeExpiresAfterTTL proves the dedupe window actually expires,
// so a legitimately repeated message hours later is not swallowed.
func TestCallbackDedupeExpiresAfterTTL(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	payload := map[string]any{"botId": "b1", "userId": "u1", "text": "hello", "messageId": "m-1"}
	harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, payload)
	harness.clock = harness.clock.Add(InboundDeliveryDedupeTTL + time.Second)
	harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, payload)
	if harness.processedCount() != 2 {
		t.Errorf("bridge ran %d times, want 2 after the TTL elapsed", harness.processedCount())
	}
}

// TestCallbackDedupeKeyIncludesTheChat is a multi-chat case: the same message id
// in two different chats is two different messages.
func TestCallbackDedupeKeyIncludesTheChat(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, map[string]any{
		"botId": "b1", "userId": "u1", "chatId": "c1", "text": "hi", "messageId": "m-1",
	})
	harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, map[string]any{
		"botId": "b1", "userId": "u1", "chatId": "c2", "text": "hi", "messageId": "m-1",
	})
	if harness.processedCount() != 2 {
		t.Errorf("bridge ran %d times, want 2 (different chats are different messages)", harness.processedCount())
	}
}

// TestCallbackBusinessFailureIs503 is the status contract the external cursor
// depends on: a failed message must answer 503 so Telegram never advances its
// offset for it.
func TestCallbackBusinessFailureIs503(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	failure := errString("agent unavailable")
	harness.pipeline.deps.Bridge = SessionBridgeFunc(func(context.Context, InboundMessage) ([]OutboundMessage, error) {
		return nil, failure
	})
	result := harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, map[string]any{
		"botId": "b1", "userId": "u1", "text": "hi", "messageId": "m-1",
	})
	if result.OK {
		t.Error("ok must be false after a business failure")
	}
	if result.Status != 503 {
		t.Errorf("status = %d, want 503", result.Status)
	}
	if len(result.Replies) != 1 {
		t.Fatalf("got %d replies, want the failure notice", len(result.Replies))
	}
	if !strings.Contains(result.Replies[0].Text, "agent unavailable") {
		t.Errorf("failure reply = %q", result.Replies[0].Text)
	}
}

// TestCallbackFailureReleasesTheDedupeKey is the retry half: a failed message
// must be REPROCESSABLE, or a provider retry would be deduped away and the user
// would lose the message.
func TestCallbackFailureReleasesTheDedupeKey(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	attempts := 0
	harness.pipeline.deps.Bridge = SessionBridgeFunc(func(context.Context, InboundMessage) ([]OutboundMessage, error) {
		attempts++
		if attempts == 1 {
			return nil, errString("transient")
		}
		return []OutboundMessage{{Text: "ok"}}, nil
	})
	payload := map[string]any{"botId": "b1", "userId": "u1", "text": "hi", "messageId": "m-1"}
	first := harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, payload)
	if first.Status != 503 {
		t.Fatalf("first status = %d, want 503", first.Status)
	}
	second := harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderWebhook, payload)
	if !second.OK {
		t.Errorf("second status = %d, ok = false; the retry must be reprocessed", second.Status)
	}
	if attempts != 2 {
		t.Errorf("bridge ran %d times, want 2", attempts)
	}
}

// TestCallbackUnknownProviderIs400 pins the first status branch.
func TestCallbackUnknownProviderIs400(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	result := harness.pipeline.HandleProviderCallbackResponse(context.Background(), ProviderDiscord, map[string]any{})
	if result.OK || result.Status != 400 {
		t.Errorf("ok = %v, status = %d; want false/400", result.OK, result.Status)
	}
}

// TestCallbackActorSerialization proves two concurrent messages from the SAME
// actor are processed one at a time (the TS Bugfix about a user rapidly pressing
// AskUserQuestion buttons).
func TestCallbackActorSerialization(t *testing.T) {
	harness := newPipelineHarness(t, []Config{webhookBot()})
	var concurrent atomic.Int32
	var peak atomic.Int32
	harness.pipeline.deps.Bridge = SessionBridgeFunc(func(ctx context.Context, message InboundMessage) ([]OutboundMessage, error) {
		running := concurrent.Add(1)
		for {
			observed := peak.Load()
			if running <= observed || peak.CompareAndSwap(observed, running) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		concurrent.Add(-1)
		return nil, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Same actor, DIFFERENT message ids so the dedupe does not drop them.
			harness.pipeline.HandleProviderCallbackResponse(ctx(), ProviderWebhook, map[string]any{
				"botId": "b1", "userId": "u1", "text": "hi",
				"messageId": "m-" + string(rune('a'+i)),
			})
		}(i)
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Errorf("peak concurrency = %d, want 1 for one actor", peak.Load())
	}
}

func ctx() context.Context { return context.Background() }

// TestAssertCallbackSucceeded pins the error text the Telegram runtime throws on.
func TestAssertCallbackSucceeded(t *testing.T) {
	if err := AssertCallbackSucceeded("Telegram", CallbackResult{OK: true}); err != nil {
		t.Errorf("ok result must not error: %v", err)
	}
	err := AssertCallbackSucceeded("Telegram", CallbackResult{OK: false})
	if err == nil || !strings.Contains(err.Error(), "status=unknown") {
		t.Errorf("err = %v, want status=unknown", err)
	}
	err = AssertCallbackSucceeded("Feishu", CallbackResult{OK: false, Status: 503})
	if err == nil || !strings.Contains(err.Error(), "status=503") {
		t.Errorf("err = %v, want status=503", err)
	}
}

// TestTelegramOffsetIsNotAdvancedOnFailure is THE offset-correctness regression:
// the cursor is the external queue's consumption ack, so a failed callback must
// leave it untouched so the retry replays the message.
func TestTelegramOffsetIsNotAdvancedOnFailure(t *testing.T) {
	dir := t.TempDir()
	repo := NewRepo(dir)
	ctx := context.Background()
	if _, err := repo.WriteConfig(ctx, ConfigFile{Version: ConfigVersion, Bots: []Config{{
		ID: "t1", Provider: ProviderTelegram, Enabled: true,
		CredentialRef: BuildCredentialKey("t1"), AllowedCommands: DefaultAllowedCommands(),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteTelegramOffset(ctx, "t1", 100); err != nil {
		t.Fatal(err)
	}

	// getUpdates returns one update; the callback fails.
	fake := newFakeTelegram(t, func(record recordedRequest) (int, string) {
		return http.StatusOK, `{"ok":true,"result":[{"update_id":100,"message":{"message_id":1,"text":"hi","from":{"id":9},"chat":{"id":9,"type":"private"}}}]}`
	})
	credentials := newStubCredentials()
	credentials.values[BuildCredentialKey("t1")] = "TOKEN"
	provider := &TelegramProvider{Client: &ProviderClient{Doer: fake.server.Client()}, Credential: credentials}
	var attempts atomic.Int32
	runtime := NewTelegramRuntime(TelegramRuntimeDeps{
		Provider:            provider,
		Credential:          credentials,
		Status:              NewRuntimeStatusStore(),
		ReadConfig:          repo.ReadConfig,
		ReadTelegramOffset:  repo.ReadTelegramOffset,
		WriteTelegramOffset: repo.WriteTelegramOffset,
		ProcessProviderCallback: func(context.Context, map[string]any) (CallbackResult, error) {
			attempts.Add(1)
			return CallbackResult{OK: false, Status: 503}, nil
		},
		RunBackgroundTasks: true,
		Sleep:              func(context.Context, time.Duration) {},
	})
	pollCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.pollBot(pollCtx, Config{ID: "t1", Provider: ProviderTelegram, Enabled: true, CredentialRef: BuildCredentialKey("t1")})
	}()
	waitFor(t, func() bool { return attempts.Load() >= 1 })
	// The loop must be joined before the temp dir is torn down.
	cancel()
	<-done

	offset, err := repo.ReadTelegramOffset(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if offset == nil || *offset != 100 {
		t.Fatalf("offset = %v, want 100 unchanged: a failed callback must NOT advance the cursor", offset)
	}
}

// TestTelegramOffsetAdvancesAfterSuccess is the other half: the cursor moves by
// update_id + 1 only once the callback succeeded.
func TestTelegramOffsetAdvancesAfterSuccess(t *testing.T) {
	dir := t.TempDir()
	repo := NewRepo(dir)
	ctx := context.Background()
	if _, err := repo.WriteConfig(ctx, ConfigFile{Version: ConfigVersion, Bots: []Config{{
		ID: "t1", Provider: ProviderTelegram, Enabled: true,
		CredentialRef: BuildCredentialKey("t1"), AllowedCommands: DefaultAllowedCommands(),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteTelegramOffset(ctx, "t1", 100); err != nil {
		t.Fatal(err)
	}
	fake := newFakeTelegram(t, func(recordedRequest) (int, string) {
		return http.StatusOK, `{"ok":true,"result":[{"update_id":100,"message":{"message_id":1,"text":"hi","from":{"id":9},"chat":{"id":9,"type":"private"}}}]}`
	})
	credentials := newStubCredentials()
	credentials.values[BuildCredentialKey("t1")] = "TOKEN"
	provider := &TelegramProvider{Client: &ProviderClient{Doer: fake.server.Client()}, Credential: credentials}
	runtime := NewTelegramRuntime(TelegramRuntimeDeps{
		Provider:            provider,
		Credential:          credentials,
		Status:              NewRuntimeStatusStore(),
		ReadConfig:          repo.ReadConfig,
		ReadTelegramOffset:  repo.ReadTelegramOffset,
		WriteTelegramOffset: repo.WriteTelegramOffset,
		ProcessProviderCallback: func(context.Context, map[string]any) (CallbackResult, error) {
			return CallbackResult{OK: true}, nil
		},
		RunBackgroundTasks: true,
		Sleep:              func(context.Context, time.Duration) {},
	})
	pollCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.pollBot(pollCtx, Config{ID: "t1", Provider: ProviderTelegram, Enabled: true, CredentialRef: BuildCredentialKey("t1")})
	}()
	waitFor(t, func() bool {
		offset, err := repo.ReadTelegramOffset(context.Background(), "t1")
		return err == nil && offset != nil && *offset == 101
	})
	cancel()
	<-done
}

// TestTelegramOffsetSurvivesAProcessRestart is the crash test: a cursor written
// before a crash is read back by a FRESH repo (new process), and the state file
// is never truncated or lost.
func TestTelegramOffsetSurvivesAProcessRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	first := NewRepo(dir)
	if err := first.WriteTelegramOffset(ctx, "t1", 4242); err != nil {
		t.Fatal(err)
	}
	// A "restart": a second Repo over the same directory, as a new process would.
	second := NewRepo(dir)
	offset, err := second.ReadTelegramOffset(ctx, "t1")
	if err != nil {
		t.Fatalf("reading the persisted cursor after a restart: %v", err)
	}
	if offset == nil || *offset != 4242 {
		t.Fatalf("offset = %v, want 4242", offset)
	}
	// The file must still be valid JSON of the documented shape.
	data, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("the persisted state file is not JSON: %v", err)
	}
	if state["version"] != float64(StateVersion) {
		t.Errorf("version = %v, want %d", state["version"], StateVersion)
	}
}

// TestRepoWriteIsAtomicUnderConcurrentReaders proves a reader never observes a
// half-written state file: the temp-file+rename strategy means every read sees
// either the old or the new document.
func TestRepoWriteIsAtomicUnderConcurrentReaders(t *testing.T) {
	dir := t.TempDir()
	repo := NewRepo(dir)
	ctx := context.Background()
	if err := repo.WriteTelegramOffset(ctx, "t1", 1); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := repo.ReadTelegramOffset(ctx, "t1"); err != nil {
				t.Errorf("a concurrent read failed: %v", err)
				return
			}
		}
	}()
	for i := 2; i < 200; i++ {
		if err := repo.WriteTelegramOffset(ctx, "t1", float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

// TestRepoMigratesLegacyStateOnce covers the readConfig/readState migration: a
// legacy file is imported once and the v3 file then becomes the source of truth.
func TestRepoMigratesLegacyStateOnce(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"bots":{"b1":{"botId":"b1","workspacePath":"/w","mode":"draft","activeTaskId":null,"updatedAt":1,"draftOptions":{"provider":"nex-agent","model":"builtin:zai/gpt-x","thoughtLevel":"high"}}}}`
	if err := os.WriteFile(filepath.Join(dir, LegacyStateFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(dir)
	state, err := repo.ReadState(context.Background())
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if len(state.Bots) != 1 {
		t.Fatalf("got %d bots, want 1", len(state.Bots))
	}
	draft := state.Bots["b1"].DraftOptions
	if draft == nil || draft.ModelSelection == nil {
		t.Fatalf("draftOptions = %+v, want a migrated selection", draft)
	}
	// builtin:zai migrates to the zai-api template; the unknown model name rides
	// along untouched (迁移只搬意图，不能检查当前可用性).
	if draft.ModelSelection.ProviderID != builtinTemplateZai {
		t.Errorf("providerId = %q, want %q", draft.ModelSelection.ProviderID, builtinTemplateZai)
	}
	// The migration result must now be pinned in the v3 file.
	if _, err := os.Stat(filepath.Join(dir, StateFileName)); err != nil {
		t.Errorf("the migrated state was not written to the v3 file: %v", err)
	}
}

// TestRepoRefusesCorruptV3Config pins the rollback-compatibility rule: a damaged
// v3 file surfaces an error and is NEVER replaced by a legacy one, because that
// would resurrect bots the user deleted.
func TestRepoRefusesCorruptV3Config(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, LegacyConfigFileName),
		[]byte(`{"version":3,"bots":[{"id":"old","name":"old","provider":"webhook","enabled":true,"allowedWorkspaces":[],"allowedCommands":{"status":true,"new":true,"workspace":true,"model":true,"thoughtLevel":true,"reply":true},"currentOptions":{},"replyMode":"assistant_changes"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(`{ not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(dir)
	if _, err := repo.ReadConfig(context.Background()); err == nil {
		t.Fatal("a corrupt v3 config must surface an error, not silently fall back")
	}
	// The damaged file must be left exactly as it was.
	data, _ := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if string(data) != `{ not json` {
		t.Errorf("the corrupt file was rewritten: %q", data)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}
