// feedback_test.go (step 3.8): the IFeedbackService behavior suite. The HTTP
// seam is always a fake — NO real endpoint is ever contacted (privacy rule
// for this service). Covers: device_mid gate, create + local index upsert,
// list filtering/sorting/pagination + the legacy status migration, comment
// and attachment redaction, upload progress events, and the compact log
// archive (today filter, budget, symlink refusal, redaction inside the zip).
package feedback

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClient records requests; it never performs IO.
type fakeClient struct {
	mu           sync.Mutex
	created      []CreateTicketInput
	comments     []string
	uploads      []UploadFileRequest
	createTicket TicketSummary
	comment      TicketComment
	attachment   Attachment
	progress     func(uploaded, total int64)
}

func (f *fakeClient) Create(_ context.Context, input CreateTicketInput, _ DeviceInfo, deviceMid, _ string) (TicketSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, input)
	f.createTicket = TicketSummary{
		ID: "T-1", Title: input.Title, Type: input.Type, Status: "已提交",
		CreatedAt: "2026-10-15T00:00:00Z", UpdatedAt: "2026-10-15T00:00:00Z",
	}
	return f.createTicket, nil
}

func (f *fakeClient) Get(_ context.Context, id string) (TicketDetail, error) {
	return TicketDetail{TicketSummary: TicketSummary{ID: id}}, nil
}

func (f *fakeClient) Comment(_ context.Context, _, body string) (TicketComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, body)
	return TicketComment{ID: 1, Body: body}, nil
}

func (f *fakeClient) UploadFile(_ context.Context, request UploadFileRequest, progress func(uploaded, total int64)) (Attachment, error) {
	f.mu.Lock()
	f.uploads = append(f.uploads, request)
	f.mu.Unlock()
	if progress != nil {
		progress(5, 10)
		progress(10, 10)
	}
	return Attachment{ID: 1, Kind: request.Kind, Filename: request.Filename, Size: 10, ContentType: request.ContentType, CreatedAt: "2026-10-15T00:00:00Z"}, nil
}

func newTestService(t *testing.T) (*Service, *fakeClient, string) {
	t.Helper()
	client := &fakeClient{}
	root := t.TempDir()
	service := New(Options{
		Client:          client,
		GetDeviceMid:    func() string { return "device-mid-1" },
		TicketStoreRoot: filepath.Join(root, "tickets"),
	})
	return service, client, root
}

func TestCreateRequiresDeviceMid(t *testing.T) {
	service := New(Options{Client: &fakeClient{}, GetDeviceMid: func() string { return "  " }})
	if _, err := service.Create(context.Background(), CreateTicketInput{Title: "x"}, CreateOptions{}); err == nil || err.Error() != MissingDeviceMidError {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateUpsertsLocalIndexAndLists(t *testing.T) {
	service, client, _ := newTestService(t)
	ticket, err := service.Create(context.Background(), CreateTicketInput{Title: "崩溃了", Description: "d", Type: "bug"}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ticket.ID != "T-1" || len(client.created) != 1 {
		t.Fatalf("ticket = %+v created = %+v", ticket, client.created)
	}
	result, err := service.List(context.Background(), ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "T-1" || result.Total != 1 {
		t.Fatalf("list = %+v", result)
	}
	// 列表按设备隔离
	other := New(Options{Client: &fakeClient{}, GetDeviceMid: func() string { return "device-mid-2" }})
	if result, _ := other.List(context.Background(), ListQuery{}); len(result.Items) != 0 {
		t.Fatalf("other device sees %v", result.Items)
	}
}

func TestListFiltersSortsAndPaginates(t *testing.T) {
	service, _, _ := newTestService(t)
	writeTickets(t, service,
		ticketJSON("a", "bug", "已提交", "2026-10-01"),
		ticketJSON("b", "feature", "开发中", "2026-10-03"),
		ticketJSON("c", "bug", "已上线", "2026-10-02"),
	)
	result, err := service.List(context.Background(), ListQuery{Type: "bug"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].ID != "c" {
		t.Fatalf("type filter + updated desc: %+v", result.Items)
	}
	// Full list order by updated desc: b(10-03) > c(10-02) > a(10-01).
	page, _ := service.List(context.Background(), ListQuery{Offset: 1, Limit: 1})
	if len(page.Items) != 1 || page.Items[0].ID != "c" {
		t.Fatalf("pagination: %+v", page.Items)
	}
}

func TestListMigratesLegacyStatus(t *testing.T) {
	service, _, _ := newTestService(t)
	writeTickets(t, service, ticketJSON("old", "bug", "待评估", "2026-09-01"), ticketJSON("bogus", "bug", "未知状态", "2026-09-02"))
	result, _ := service.List(context.Background(), ListQuery{})
	for _, item := range result.Items {
		if item.Status != "已提交" {
			t.Fatalf("status %q must migrate to 已提交", item.Status)
		}
	}
}

func ticketJSON(id, type_, status, updated string) string {
	return fmt.Sprintf(`{"deviceMid":"device-mid-1","id":%q,"title":"t","type":%q,"status":%q,"created_at":"2026-09-01","updated_at":%q}`, id, type_, status, updated)
}

func writeTickets(t *testing.T, service *Service, tickets ...string) {
	t.Helper()
	store := &localTicketStore{rootDir: service.store.rootDir}
	if err := store.write(ticketStoreFile{}); err != nil {
		t.Fatal(err)
	}
	var all []storedTicket
	for _, raw := range tickets {
		var stored storedTicket
		json.Unmarshal([]byte(raw), &stored)
		all = append(all, stored)
	}
	if err := store.write(ticketStoreFile{Tickets: all}); err != nil {
		t.Fatal(err)
	}
}

func TestCommentIsRedacted(t *testing.T) {
	service, client, _ := newTestService(t)
	if _, err := service.Comment(context.Background(), "T-1", "my password=hunter2 detail"); err != nil {
		t.Fatal(err)
	}
	if len(client.comments) != 1 || client.comments[0] != "my password=[REDACTED] detail" {
		t.Fatalf("comments = %v", client.comments)
	}
}

func TestUploadAttachmentDefaults(t *testing.T) {
	service, client, _ := newTestService(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	os.WriteFile(path, []byte("x"), 0o644)
	if _, err := service.UploadAttachment(context.Background(), "T-1", KindImage, UploadFileRequest{Path: path}); err != nil {
		t.Fatal(err)
	}
	if len(client.uploads) != 1 {
		t.Fatal("no upload recorded")
	}
	upload := client.uploads[0]
	if upload.Filename != "shot.png" || upload.ContentType != "image/png" {
		t.Fatalf("upload = %+v", upload)
	}
	// log kind defaults to zip
	if _, err := service.UploadAttachment(context.Background(), "T-1", KindLog, UploadFileRequest{Path: path}); err != nil {
		t.Fatal(err)
	}
	if client.uploads[1].ContentType != "application/zip" {
		t.Fatalf("log content type = %q", client.uploads[1].ContentType)
	}
}

func TestUploadProgressEvents(t *testing.T) {
	service, _, _ := newTestService(t)
	var phases []string
	done := make(chan struct{})
	var once sync.Once
	cancel := service.OnUploadProgress("op-1", func(progress UploadProgress) {
		phases = append(phases, progress.Phase)
		if progress.Phase == "complete" {
			once.Do(func() { close(done) })
		}
	})
	defer cancel()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.zip")
	os.WriteFile(path, make([]byte, 10), 0o644)
	if _, err := service.UploadAttachmentWithProgress(context.Background(), "T-1", KindLog, UploadFileRequest{Path: path}, "op-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("no complete event")
	}
	if len(phases) < 3 || phases[0] != "preparing" || phases[len(phases)-1] != "complete" {
		t.Fatalf("phases = %v", phases)
	}
}

func TestUploadAttachmentDataSanitizesFilename(t *testing.T) {
	service, client, _ := newTestService(t)
	data := base64.StdEncoding.EncodeToString([]byte("hello"))
	attachment, err := service.UploadAttachmentData(context.Background(), "T-1", KindFile, data, `..\..\evil\name.txt`, "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Kind != KindFile {
		t.Fatalf("attachment = %+v", attachment)
	}
	if len(client.uploads) != 1 {
		t.Fatal("no upload")
	}
	upload := client.uploads[0]
	if strings.Contains(upload.Filename, `\`) || strings.Contains(upload.Filename, "..") {
		t.Fatalf("filename not sanitized: %q", upload.Filename)
	}
}

// ── compact log archive ───────────────────────────────────────────────────

func archiveFixture(t *testing.T) (sourcesDir, outputRoot string) {
	t.Helper()
	root := t.TempDir()
	logsDir := filepath.Join(root, "v2", "logs")
	os.MkdirAll(logsDir, 0o755)
	return logsDir, filepath.Join(root, "feedback", "logs")
}

func TestArchiveIncludesTodaysLogsWithRedaction(t *testing.T) {
	logsDir, outputRoot := archiveFixture(t)
	os.WriteFile(filepath.Join(logsDir, "app.log"), []byte("boot ok\npassword=hunter2\n"), 0o644)
	os.WriteFile(filepath.Join(logsDir, "old.log"), []byte("yesterday\n"), 0o644)
	// old.log must be mtime-dated yesterday
	old := time.Now().Add(-36 * time.Hour)
	os.Chtimes(filepath.Join(logsDir, "old.log"), old, old)
	service := New(Options{
		Client: &fakeClient{}, GetDeviceMid: func() string { return "d" },
		ArchiveSources: []ArchiveSource{{Directory: logsDir, ArchivePrefix: "logs"}},
		LogOutputRoot:  outputRoot,
		AppVersion:     "3.4.0", Commit: "abc123", RuntimeVersion: "go-test",
	})
	path, size, err := service.PrepareCompactLogArchive(context.Background(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if size == 0 {
		t.Fatal("empty archive")
	}
	defer service.CleanupPreparedLogArchive(path)
	entries := readZip(t, path)
	if _, ok := entries["logs/app.log"]; !ok {
		t.Fatalf("entries = %v", entries)
	}
	if _, ok := entries["logs/old.log"]; ok {
		t.Fatal("yesterday's log must be excluded")
	}
	if _, ok := entries["about.txt"]; !ok {
		t.Fatal("about.txt missing")
	}
	if strings.Contains(entries["logs/app.log"], "hunter2") {
		t.Fatal("secret leaked into the archive")
	}
	if !strings.Contains(entries["logs/app.log"], "[REDACTED]") {
		t.Fatalf("redaction missing: %q", entries["logs/app.log"])
	}
}

func TestArchiveSkipsNonWhitelistedAndSymlinks(t *testing.T) {
	logsDir, outputRoot := archiveFixture(t)
	os.WriteFile(filepath.Join(logsDir, "image.png"), []byte("x"), 0o644)
	// a symlinked log file must be skipped (unsafe-path)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "linked.log"), []byte("linked\n"), 0o644)
	os.Symlink(filepath.Join(outside, "linked.log"), filepath.Join(logsDir, "link.log"))
	service := New(Options{
		Client: &fakeClient{}, GetDeviceMid: func() string { return "d" },
		ArchiveSources: []ArchiveSource{{Directory: logsDir, ArchivePrefix: "logs"}},
		LogOutputRoot:  outputRoot,
	})
	path, _, err := service.PrepareCompactLogArchive(context.Background(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.CleanupPreparedLogArchive(path)
	entries := readZip(t, path)
	if _, ok := entries["logs/image.png"]; ok {
		t.Fatal("png must be skipped (not a .log/.jsonl)")
	}
	if _, ok := entries["logs/link.log"]; ok {
		t.Fatal("symlinked log must be skipped")
	}
}

func TestArchiveBudgetSkipsOversized(t *testing.T) {
	logsDir, outputRoot := archiveFixture(t)
	// compact budget is 2 MiB: a 2.5 MiB log must be skipped
	big := make([]byte, 25*1024*1024/10)
	os.WriteFile(filepath.Join(logsDir, "big.log"), big, 0o644)
	service := New(Options{
		Client: &fakeClient{}, GetDeviceMid: func() string { return "d" },
		ArchiveSources: []ArchiveSource{{Directory: logsDir, ArchivePrefix: "logs"}},
		LogOutputRoot:  outputRoot,
	})
	path, size, err := service.PrepareCompactLogArchive(context.Background(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.CleanupPreparedLogArchive(path)
	if size > 2*1024*1024+4096 {
		t.Fatalf("archive size %d exceeds the compact budget", size)
	}
}

func TestArchiveRejectsBinaryContent(t *testing.T) {
	logsDir, outputRoot := archiveFixture(t)
	binary := bytes.Repeat([]byte{0x00, 0x01, 0x02}, 100)
	os.WriteFile(filepath.Join(logsDir, "bin.log"), binary, 0o644)
	service := New(Options{
		Client: &fakeClient{}, GetDeviceMid: func() string { return "d" },
		ArchiveSources: []ArchiveSource{{Directory: logsDir, ArchivePrefix: "logs"}},
		LogOutputRoot:  outputRoot,
	})
	path, _, err := service.PrepareCompactLogArchive(context.Background(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.CleanupPreparedLogArchive(path)
	entries := readZip(t, path)
	if _, ok := entries["logs/bin.log"]; ok {
		t.Fatal("non-text bytes must not fall through to a raw include")
	}
}

func readZip(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stat, _ := file.Stat()
	reader, err := zip.NewReader(file, stat.Size())
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]string{}
	for _, entry := range reader.File {
		rc, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		entries[entry.Name] = string(data)
	}
	return entries
}
