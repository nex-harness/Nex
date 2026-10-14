package onboarding

// onboarding_test.go: record lifecycle, v1 legacy migration, corrupt-file
// tolerance, deviceMid authority, write serialization.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "onboarding-record.json")
	return NewService(Options{RecordFile: file}), file
}

func validEntry() Entry {
	return Entry{
		Occupation:                  "developer",
		InterfaceMode:               "coding",
		MemoryEnabled:               true,
		ProactiveSuggestionsEnabled: false,
		CompletedAt:                 "2024-01-01T00:00:00.000Z",
	}
}

func TestAppendAndGetLatest(t *testing.T) {
	svc, file := newService(t)
	if err := svc.AppendRecord(context.Background(), "mid-1", validEntry()); err != nil {
		t.Fatal(err)
	}
	entry, err := svc.GetLatestEntry(context.Background())
	if err != nil || entry == nil {
		t.Fatalf("getLatestEntry: %v %v", entry, err)
	}
	if entry.Occupation != "developer" || entry.UploadState != "pending" {
		t.Fatalf("entry wrong: %+v", entry)
	}
	// The file on disk is valid JSON with the TS key order.
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("file not valid JSON: %v", err)
	}
	if parsed["deviceMid"] != "mid-1" || parsed["version"] != float64(2) {
		t.Fatalf("file shape wrong: %s", data)
	}
}

func TestAppendReplacesExistingEntry(t *testing.T) {
	// 同一用户重复完成引导覆盖旧条目，而不是追加.
	svc, _ := newService(t)
	if err := svc.AppendRecord(context.Background(), "mid-1", validEntry()); err != nil {
		t.Fatal(err)
	}
	second := validEntry()
	second.Occupation = "design"
	if err := svc.AppendRecord(context.Background(), "mid-1", second); err != nil {
		t.Fatal(err)
	}
	file, err := svc.GetRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Entries) != 1 || file.Entries[0].Occupation != "design" {
		t.Fatalf("entry not replaced: %+v", file.Entries)
	}
}

func TestDeviceMidAuthority(t *testing.T) {
	// deviceMid 以文件内已有值为权威：调用方传入不同值时沿用旧值.
	svc, _ := newService(t)
	if err := svc.AppendRecord(context.Background(), "mid-original", validEntry()); err != nil {
		t.Fatal(err)
	}
	if err := svc.AppendRecord(context.Background(), "mid-different", validEntry()); err != nil {
		t.Fatal(err)
	}
	file, _ := svc.GetRecords(context.Background())
	if file.DeviceMid != "mid-original" {
		t.Fatalf("deviceMid not authoritative: %s", file.DeviceMid)
	}
}

func TestDismissWithoutEntry(t *testing.T) {
	svc, _ := newService(t)
	if err := svc.DismissOnboarding(context.Background(), "mid-1"); err != nil {
		t.Fatal(err)
	}
	file, _ := svc.GetRecords(context.Background())
	if len(file.Decisions) != 1 || file.Decisions[0].Status != "dismissed" || file.Decisions[0].Reason != "user_closed" {
		t.Fatalf("decision wrong: %+v", file.Decisions)
	}
	// 已有作答时 dismiss 为空操作.
	if err := svc.AppendRecord(context.Background(), "mid-1", validEntry()); err != nil {
		t.Fatal(err)
	}
	before, _ := svc.GetRecords(context.Background())
	if err := svc.DismissOnboarding(context.Background(), "mid-1"); err != nil {
		t.Fatal(err)
	}
	after, _ := svc.GetRecords(context.Background())
	if len(after.Decisions) != len(before.Decisions) {
		t.Fatal("dismiss with an existing entry must be a no-op")
	}
}

func TestV1LegacyFileMigration(t *testing.T) {
	// v1 记录：无 decisions 字段。读取时补空 decisions，业务写入落为 v2.
	_, file := newService(t)
	v1 := `{"version":1,"deviceMid":"mid-1","entries":[{"userId":null,"occupation":"dev","interfaceMode":"coding","memoryEnabled":null,"proactiveSuggestionsEnabled":null,"completedAt":"2024-01-01T00:00:00.000Z","uploadState":"pending"}]}`
	if err := os.WriteFile(file, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Options{RecordFile: file})
	records, err := svc.GetRecords(context.Background())
	if err != nil || records == nil {
		t.Fatalf("v1 read: %v %v", records, err)
	}
	if records.Version != 2 || len(records.Decisions) != 0 || len(records.Entries) != 1 {
		t.Fatalf("v1 transform wrong: %+v", records)
	}
}

func TestCorruptFileTreatedAsMissing(t *testing.T) {
	_, file := newService(t)
	if err := os.WriteFile(file, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Options{RecordFile: file})
	entry, err := svc.GetLatestEntry(context.Background())
	if err != nil || entry != nil {
		t.Fatalf("corrupt file must read as missing: %v %v", entry, err)
	}
	// 重新触发引导后在下次 append 时重建.
	if err := svc.AppendRecord(context.Background(), "mid-new", validEntry()); err != nil {
		t.Fatal(err)
	}
	records, _ := svc.GetRecords(context.Background())
	if records == nil || records.DeviceMid != "mid-new" {
		t.Fatalf("rebuild failed: %+v", records)
	}
}

func TestUpdateRecordPreferences(t *testing.T) {
	svc, _ := newService(t)
	// 无条目时忽略.
	err := svc.UpdateRecordPreferences(context.Background(), PreferencesPatch{MemoryEnabled: true, MemoryEnabledSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AppendRecord(context.Background(), "mid-1", validEntry()); err != nil {
		t.Fatal(err)
	}
	err = svc.UpdateRecordPreferences(context.Background(), PreferencesPatch{
		MemoryEnabled:                  false,
		MemoryEnabledSet:               true,
		ProactiveSuggestionsEnabled:    true,
		ProactiveSuggestionsEnabledSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := svc.GetLatestEntry(context.Background())
	if entry.MemoryEnabled != false || entry.ProactiveSuggestionsEnabled != true {
		t.Fatalf("preferences not updated: %+v", entry)
	}
}

func TestClearRecords(t *testing.T) {
	svc, file := newService(t)
	if err := svc.AppendRecord(context.Background(), "mid-1", validEntry()); err != nil {
		t.Fatal(err)
	}
	if err := svc.ClearRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("clearRecords must remove the file")
	}
}

func TestConcurrentAppendsSerialize(t *testing.T) {
	// 引导保存与并发触发判定同时发生时不丢条目（写队列串行化）.
	svc, _ := newService(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.AppendRecord(context.Background(), "mid-1", validEntry())
		}()
	}
	wg.Wait()
	file, err := svc.GetRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Entries) != 1 {
		t.Fatalf("concurrent appends must serialize to one entry, got %d", len(file.Entries))
	}
}
