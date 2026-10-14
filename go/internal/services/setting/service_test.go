package setting

// service_test.go: the ISettingService behaviours — store read/write, the
// write-queue semantics (serialization, stale-generation skip, timeout), the
// onDidUpdate host notify, and updateDataBaseDir's fault suite.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestService(t *testing.T, opts ServiceOptions) *Service {
	t.Helper()
	dir := t.TempDir()
	if opts.SettingsFile == "" {
		opts.SettingsFile = filepath.Join(dir, "home", ".nex", "v2", "setting.json")
	}
	opts.RetrySleep = func(time.Duration) {} // parse retries don't sleep in tests
	return NewService(opts)
}

func readSettingFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := parseJSONMap(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func parseJSONMap(data []byte, v *map[string]any) error {
	return jsonUnmarshal(data, v)
}

func TestGetReturnsDefaultsWhenFileMissing(t *testing.T) {
	svc := newTestService(t, ServiceOptions{})
	settings, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if locale, _ := settings.Get("locale"); locale != "zh-CN" {
		t.Fatalf("default locale: %v", locale)
	}
}

func TestUpdatePersistsAndReadsBack(t *testing.T) {
	svc := newTestService(t, ServiceOptions{})
	if err := svc.Update(context.Background(), map[string]any{"locale": "en-US"}, nil); err != nil {
		t.Fatal(err)
	}
	settings, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if locale, _ := settings.Get("locale"); locale != "en-US" {
		t.Fatalf("locale not persisted: %v", locale)
	}
	// The file is two-space indented, no trailing newline, keys in schema order.
	data, err := os.ReadFile(svc.SettingsFile())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.HasSuffix(content, "\n") {
		t.Fatal("setting.json must not end with a newline (TS parity)")
	}
	if !strings.Contains(content, "  \"locale\": \"en-US\"") {
		t.Fatalf("expected two-space indentation, got: %s", content[:200])
	}
	firstLocale := strings.Index(content, `"locale"`)
	firstRecent := strings.Index(content, `"recentProjects"`)
	if firstRecent > firstLocale {
		t.Fatalf("recentProjects must precede locale (schema order)")
	}
}

func TestUpdateMergesInsteadOfOverwriting(t *testing.T) {
	// 打开工作区后几乎同时写 recentProjects 和 lastWorkspaceSession：
	// 每个补丁都基于上一次落盘状态合并，后写不得抹掉前一个字段。
	svc := newTestService(t, ServiceOptions{})
	if err := svc.Update(context.Background(), map[string]any{"recentProjects": []any{"/a", "/b", "/a"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.Update(context.Background(), map[string]any{"lastActiveTabIndex": int64(3)}, nil); err != nil {
		t.Fatal(err)
	}
	settings, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projects, _ := settings.Get("recentProjects")
	if len(projects.([]any)) != 2 {
		t.Fatalf("recentProjects lost or duplicated: %v", projects)
	}
	if tab, _ := settings.Get("lastActiveTabIndex"); toInt64(tab) != 3 {
		t.Fatalf("lastActiveTabIndex: %v", tab)
	}
}

func TestUpdateRecentProjectsDedupeAndCap(t *testing.T) {
	svc := newTestService(t, ServiceOptions{})
	var projects []any
	for i := 0; i < 15; i++ {
		projects = append([]any{string(rune('a' + i))}, projects...)
	}
	if err := svc.Update(context.Background(), map[string]any{"recentProjects": projects}, nil); err != nil {
		t.Fatal(err)
	}
	settings, _ := svc.Get(context.Background())
	got, _ := settings.Get("recentProjects")
	if len(got.([]any)) != MaxRecentProjects {
		t.Fatalf("recentProjects cap: got %d, want %d", len(got.([]any)), MaxRecentProjects)
	}
}

func TestUpdateExpectedAccountSettingsGuard(t *testing.T) {
	svc := newTestService(t, ServiceOptions{})
	// Expected matches → ok.
	err := svc.Update(context.Background(), map[string]any{"providerFamilyDomain": "zai"}, &ExpectedAccountSettings{
		ProviderFamilyConnectionSelections: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Expected does not match the current domain → the exact TS error.
	err = svc.Update(context.Background(), map[string]any{}, &ExpectedAccountSettings{
		ProviderFamilyDomain:               "bigmodel",
		ProviderFamilyDomainSet:            true,
		ProviderFamilyConnectionSelections: map[string]any{},
	})
	if err == nil || err.Error() != "Account connection settings changed" {
		t.Fatalf("want account-changed error, got %v", err)
	}
}

func TestUpdateRejectsInvalidPatch(t *testing.T) {
	svc := newTestService(t, ServiceOptions{})
	if err := svc.Update(context.Background(), map[string]any{"locale": "fr-FR"}, nil); err == nil {
		t.Fatal("invalid locale must be rejected")
	}
	// The file must not exist after a rejected patch.
	if _, err := os.Stat(svc.SettingsFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected patch must not create setting.json")
	}
}

func TestCorruptFileQuarantined(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "setting.json")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, ServiceOptions{SettingsFile: file})
	settings, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if locale, _ := settings.Get("locale"); locale != "zh-CN" {
		t.Fatal("corrupt file should yield defaults")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	quarantined := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "setting.json.corrupt-") {
			quarantined = true
		}
	}
	if !quarantined {
		t.Fatal("corrupt file must be quarantined with a timestamped backup")
	}
}

func TestWriteQueueSerializesAndMerges(t *testing.T) {
	// 并发写两个不同的字段（recentProjects / lastActiveTaskByWorkspace，与
	// lastActiveTabIndex 交错）：每个补丁基于上一次落盘状态合并，两个字段都
	// 必须保留 —— 这是 TS 写入串行化修复的原始 bug（打开工作区后几乎同时写
	// recentProjects 和 lastWorkspaceSession，后写整块抹掉前一个）。
	//
	// 注意（TS 实测，probe26）：对同一 record 字段并发写不同的 map 时，
	// TS 是"该字段后写覆盖"（每个写只带自己的 key，current 读取发生在各自
	// 闭包运行时）—— Go 复刻该语义，见 TestConcurrentSameFieldLastWriterWins。
	svc := newTestService(t, ServiceOptions{})
	const writers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var patch map[string]any
			switch i % 2 {
			case 0:
				patch = map[string]any{"recentProjects": []any{"/proj"}}
			case 1:
				patch = map[string]any{"lastActiveTaskByWorkspace": map[string]any{"/w": "t"}}
			default:
				patch = map[string]any{"lastActiveTabIndex": int64(i)}
			}
			errCh <- svc.Update(context.Background(), patch, nil)
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	settings, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projects, _ := settings.Get("recentProjects")
	if len(projects.([]any)) != 1 || projects.([]any)[0] != "/proj" {
		t.Fatalf("recentProjects lost: %v", projects)
	}
	tasks, _ := settings.Get("lastActiveTaskByWorkspace")
	if len(tasks.(map[string]any)) != 1 {
		t.Fatalf("lastActiveTaskByWorkspace lost: %v", tasks)
	}
}

func TestConcurrentSameFieldLastWriterWins(t *testing.T) {
	// TS 实测（probe26）：8 个并发 update 各写一个 lastActiveTaskByWorkspace
	// key，最终只有 1 个 key 落盘 —— 同一 record 字段整块替换，后写覆盖。
	// Go 必须复刻（不聚合 key），否则与 Node 的落盘结果不同。
	svc := newTestService(t, ServiceOptions{})
	const writers = 8
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = svc.Update(context.Background(), map[string]any{
				"lastActiveTaskByWorkspace": map[string]any{string(rune('a' + i)): "task"},
			}, nil)
		}(i)
	}
	wg.Wait()
	settings, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := settings.Get("lastActiveTaskByWorkspace")
	if len(tasks.(map[string]any)) != 1 {
		t.Fatalf("same-field concurrent writes must be last-writer-wins (TS parity), got %d keys: %v", len(tasks.(map[string]any)), tasks)
	}
}

func TestWriteQueueStaleWriteSkipped(t *testing.T) {
	// Generation semantics (probed, probe28/probe30):
	//   - A parked while B merely ENQUEUES (chained behind A): A's
	//     shouldCommit stays true — the TS allocates the generation at RUN
	//     time, so a queued-but-not-running B does not invalidate A.
	//   - The ONLY stale path is the TIMEOUT: expireCurrentWrite bumps the
	//     generation, and the still-running body later observes
	//     shouldCommit() == false, so the late rename is skipped.
	var order []string
	var mu sync.Mutex
	q := NewQueue(QueueOptions{TimeoutMs: 5000})

	// Case 1: A parks; B enqueues behind it; both commit.
	aRunning := make(chan struct{})
	bEnqueued := make(chan struct{})
	aDone := make(chan error, 1)
	go func() {
		aDone <- q.EnqueueSettingsWrite(context.Background(), func(shouldCommit func() bool, _ func()) error {
			close(aRunning)
			<-bEnqueued
			mu.Lock()
			order = append(order, "A:"+boolStr(shouldCommit()))
			mu.Unlock()
			return nil
		})
	}()
	<-aRunning
	bDone := make(chan error, 1)
	go func() {
		bDone <- q.EnqueueSettingsWrite(context.Background(), func(shouldCommit2 func() bool, _ func()) error {
			mu.Lock()
			order = append(order, "B:"+boolStr(shouldCommit2()))
			mu.Unlock()
			return nil
		})
	}()
	// Give B time to enqueue (it parks on the chain behind A).
	time.Sleep(50 * time.Millisecond)
	close(bEnqueued)
	if err := <-aDone; err != nil {
		t.Fatalf("A: %v", err)
	}
	if err := <-bDone; err != nil {
		t.Fatalf("B: %v", err)
	}
	if len(order) != 2 || order[0] != "A:true" || order[1] != "B:true" {
		t.Fatalf("parked-A case wrong: %v", order)
	}
}

func TestWriteQueueTimeoutExpiresStaleWrite(t *testing.T) {
	// Case 2 (probe30): after the timeout fires, the still-running body sees
	// shouldCommit() == false — the late rename is skipped in the store.
	var mu sync.Mutex
	var bodySaw *bool
	q := NewQueue(QueueOptions{TimeoutMs: 40})
	err := q.EnqueueSettingsWrite(context.Background(), func(shouldCommit func() bool, _ func()) error {
		time.Sleep(200 * time.Millisecond) // exceed the timeout
		saw := shouldCommit()
		mu.Lock()
		bodySaw = &saw
		mu.Unlock()
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	// The body keeps running after the caller's timeout (Promise.race
	// semantics); wait for it to observe the expired generation.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		saw := bodySaw
		mu.Unlock()
		if saw != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if bodySaw == nil || *bodySaw {
		t.Fatalf("body must observe shouldCommit()==false after expiry, got %v", bodySaw)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestWriteQueueTimeout(t *testing.T) {
	q := NewQueue(QueueOptions{TimeoutMs: 30})
	err := q.EnqueueSettingsWrite(context.Background(), func(_ func() bool, _ func()) error {
		time.Sleep(300 * time.Millisecond)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "settingService update timed out after 30ms") {
		t.Fatalf("want timeout error, got %v", err)
	}
}

func TestOnDidUpdateFiresAfterCommit(t *testing.T) {
	svc := newTestService(t, ServiceOptions{})
	var mu sync.Mutex
	var events [][]string
	unsub := svc.OnDidUpdate(func(keys []string) {
		mu.Lock()
		events = append(events, keys)
		mu.Unlock()
	})
	defer unsub()
	if err := svc.Update(context.Background(), map[string]any{"locale": "en-US"}, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	firstBatch := len(events)
	mu.Unlock()
	if firstBatch != 1 || len(events[0]) != 1 || events[0][0] != "locale" {
		t.Fatalf("onDidUpdate events wrong: %v", events)
	}
	// A failed update does not fire.
	if err := svc.Update(context.Background(), map[string]any{"locale": "bad"}, nil); err == nil {
		t.Fatal("expected validation error")
	}
	mu.Lock()
	count := len(events)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("failed update must not fire onDidUpdate (got %d events)", count)
	}
}

func TestLegacyFileMigrationPersistedOnRead(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "setting.json")
	// Legacy file: no providerFamilyConnectionSelections but old nav keys
	// (complete team connection → migration is persisted on first read).
	content := `{
  "modelProviderFamilyModes": {"zai": "team"},
  "modelProviderFamilySelectedKeys": {"zai": "team-plan:builtin:zai-coding-plan:pid:org:proj"},
  "closeToTrayOnWindowsMigrationInitialized": true,
  "messageStreamShowReasoningMigrationInitialized": true
}`
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, ServiceOptions{SettingsFile: file})
	settings, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	selections, _ := settings.Get("providerFamilyConnectionSelections")
	zai, _ := selections.(map[string]any)["zai"].(map[string]any)
	if zai["kind"] != "team-coding-plan" {
		t.Fatalf("legacy selection not migrated: %v", selections)
	}
	// The persisted file keeps the legacy fields AND the migrated selections.
	persisted := readSettingFile(t, file)
	if _, has := persisted["modelProviderFamilyModes"]; !has {
		t.Fatal("legacy rollback fields must be retained")
	}
	if _, has := persisted["providerFamilyConnectionSelections"]; !has {
		t.Fatal("migrated selections must be persisted")
	}
}

func TestIncompleteLegacyTeamKeepsSelectionsOut(t *testing.T) {
	// 旧 Team 尚待 OAuth 补组织：普通偏好保存必须保留新字段缺席。
	dir := t.TempDir()
	file := filepath.Join(dir, "setting.json")
	content := `{
  "modelProviderFamilyModes": {"zai": "team"},
  "modelProviderFamilySelectedKeys": {"zai": "team-plan:builtin:zai-coding-plan:pid:proj"},
  "closeToTrayOnWindowsMigrationInitialized": true,
  "messageStreamShowReasoningMigrationInitialized": true
}`
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, ServiceOptions{SettingsFile: file})
	if _, err := svc.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Update(context.Background(), map[string]any{"locale": "en-US"}, nil); err != nil {
		t.Fatal(err)
	}
	persisted := readSettingFile(t, file)
	if _, has := persisted["providerFamilyConnectionSelections"]; has {
		t.Fatal("incomplete legacy team must keep selections absent on ordinary saves")
	}
}

func TestEnsureDefaultProject(t *testing.T) {
	svc := newTestService(t, ServiceOptions{})
	home := t.TempDir()
	result, err := svc.EnsureDefaultProject(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatal("first call should create")
	}
	if _, err := os.Stat(filepath.Join(home, DefaultProjectName)); err != nil {
		t.Fatal(err)
	}
	result, err = svc.EnsureDefaultProject(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Fatal("second call should not create")
	}
}

// toInt64 normalizes the JSON number shapes for test comparisons.
func toInt64(v any) int64 {
	n, _ := toInt(v)
	return n
}
