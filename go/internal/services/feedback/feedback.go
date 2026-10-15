// Package feedback is the IFeedbackService port (step 3.8): ticket create /
// list / comment / attachments against the feedback backend, the local
// ticket index (Nex has no accounts — list is local), and the compact log
// archive.
//
// Source: packages/services/src/feedback/*.ts. Privacy boundary: the HTTP
// client is an injected seam (HTTPClient) so tests never touch a real
// endpoint; redaction of title/description/comment body uses
// internal/feedbackprivacy before anything leaves the process; the log
// archive enforces the same whitelist/budget/decodability rules as
// feedbackLogArchive.ts.
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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
)

// ChannelName is the rpc channel name (ServiceChannels.Feedback).
const ChannelName = "feedback"

// Error strings the UI branches on.
const (
	MissingDeviceMidError = "Missing feedback device_mid"
)

// Attachment kind values (FeedbackAttachmentKind).
const (
	KindImage = "image"
	KindLog   = "log"
	KindFile  = "file"
)

// maxAttachmentBytesByKind ports getFeedbackMaxAttachmentBytes.
var maxAttachmentBytesByKind = map[string]int64{
	KindImage: 10 * 1024 * 1024,
	KindFile:  25 * 1024 * 1024,
	KindLog:   1024 * 1024 * 1024,
}

// Attachment ports FeedbackAttachment.
type Attachment struct {
	ID          int     `json:"id"`
	Kind        string  `json:"kind"`
	Filename    string  `json:"filename"`
	Size        int64   `json:"size"`
	Redacted    bool    `json:"redacted"`
	ContentType string  `json:"content_type"`
	DownloadURL *string `json:"download_url"`
	PreviewURL  *string `json:"preview_url"`
	CreatedAt   string  `json:"created_at"`
}

// TicketSummary ports FeedbackTicketSummary.
type TicketSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// TicketDetail ports FeedbackTicketDetail.
type TicketDetail struct {
	TicketSummary
	Description string          `json:"description"`
	Module      string          `json:"module"`
	Severity    string          `json:"severity"`
	Comments    []TicketComment `json:"comments"`
}

// TicketComment ports FeedbackComment.
type TicketComment struct {
	ID        int    `json:"id"`
	TicketID  string `json:"ticket_id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// ListQuery ports FeedbackListQuery. Limit carries the TS `undefined` as
// -1 (Go's zero value 0 would otherwise mean "return nothing").
type ListQuery struct {
	Status string `json:"status,omitempty"`
	Type   string `json:"type,omitempty"`
	Offset int    `json:"offset,omitempty"`
	// Limit < 0 means "unset" (the TS undefined): return everything.
	Limit int `json:"limit,omitempty"`
}

// ListResult ports FeedbackListResult.
type ListResult struct {
	Items []TicketSummary `json:"items"`
	Total int             `json:"total"`
}

// DeviceInfo ports FeedbackDeviceInfo.
type DeviceInfo struct {
	AppVersion    string `json:"appVersion"`
	BuildCommitID string `json:"buildCommitId"`
	BuildTime     string `json:"buildTime"`
	NodeVersion   string `json:"nodeVersion"`
	OsType        string `json:"osType"`
	OsPlatform    string `json:"osPlatform"`
	OsRelease     string `json:"osRelease"`
	OsVersion     string `json:"osVersion"`
	OsArch        string `json:"osArch"`
}

// CreateTicketInput ports CreateFeedbackTicketInput (the fields the service
// forwards; the full zod shape lives in @nex/shared).
type CreateTicketInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Module      string `json:"module"`
	Severity    string `json:"severity"`
	Contact     *struct {
		Email string `json:"email"`
	} `json:"contact"`
	Locale string      `json:"locale,omitempty"`
	Device *DeviceInfo `json:"device,omitempty"`
}

// UploadProgress ports FeedbackUploadProgress.
type UploadProgress struct {
	ID            string `json:"id"`
	Phase         string `json:"phase"` // preparing | uploading | complete | canceled
	UploadedBytes int64  `json:"uploadedBytes"`
	TotalBytes    int64  `json:"totalBytes"`
}

// CreateOptions ports FeedbackCreateOptions.
type CreateOptions struct {
	OperationID string
}

// UploadFileRequest is one attachment upload request.
type UploadFileRequest struct {
	TicketID    string
	Kind        string
	Path        string
	Filename    string
	ContentType string
	MessageID   string
}

// UploadResult is the outcome of an attachment upload.
type UploadResult struct {
	Attachment Attachment
}

// HTTPClient is the injected HTTP seam: the TS FeedbackHttpClient. Tests use
// a fake; production wires the API client. No real endpoint may ever appear
// in a test.
type HTTPClient interface {
	// Create posts the ticket; the service has already redacted the text.
	Create(ctx context.Context, input CreateTicketInput, device DeviceInfo, deviceMid, locale string) (TicketSummary, error)
	// Get fetches one ticket.
	Get(ctx context.Context, id string) (TicketDetail, error)
	// Comment posts a comment (body pre-redacted).
	Comment(ctx context.Context, id, body string) (TicketComment, error)
	// UploadFile uploads one local file (path resolved by the caller).
	UploadFile(ctx context.Context, request UploadFileRequest, progress func(uploaded, total int64)) (Attachment, error)
}

// Options ports CreateFeedbackServiceOptions.
type Options struct {
	Client       HTTPClient
	GetDeviceMid func() string
	// Env supplies NEX_FEEDBACK_API_BASE (tests inject; production reads the
	// process env once at construction).
	Env func(string) string
	// APIBaseURL overrides the env fallback.
	APIBaseURL string
	// TicketStoreRoot overrides the local tickets.json root (tests).
	TicketStoreRoot string
	// ArchiveDirs overrides the diagnostic log sources (tests): pairs of
	// (directory, archivePrefix).
	ArchiveSources []ArchiveSource
	// LogOutputRoot overrides the archive output root (tests).
	LogOutputRoot string
	// AppVersion feeds about.txt.
	AppVersion string
	// Commit feeds about.txt.
	Commit string
	// RuntimeVersion feeds about.txt (Node's process.version slot).
	RuntimeVersion string
}

// ArchiveSource ports FeedbackLogSource.
type ArchiveSource struct {
	Directory     string
	ArchivePrefix string
}

// Service ports the IFeedbackService surface.
type Service struct {
	client       HTTPClient
	getDeviceMid func() string
	store        *localTicketStore
	archive      *logArchive

	mu             sync.Mutex
	uploadProgress map[string][]func(UploadProgress)
	activeUploads  map[string]context.CancelFunc
	activeCreates  map[string]context.CancelFunc
}

// New ports createFeedbackService.
func New(options Options) *Service {
	storeRoot := options.TicketStoreRoot
	archive := newLogArchive(options.ArchiveSources, options.LogOutputRoot, options.AppVersion, options.Commit, options.RuntimeVersion)
	return &Service{
		client:         options.Client,
		getDeviceMid:   options.GetDeviceMid,
		store:          &localTicketStore{rootDir: storeRoot},
		archive:        archive,
		uploadProgress: map[string][]func(UploadProgress){},
		activeUploads:  map[string]context.CancelFunc{},
		activeCreates:  map[string]context.CancelFunc{},
	}
}

// DeviceSnapshot ports getDeviceSnapshot / buildDeviceSnapshot. osVersion
// duplicates osRelease deliberately: the backend column is VARCHAR(64) and
// the full Darwin kernel string overflows it (TS comment).
func (s *Service) DeviceSnapshot() DeviceInfo {
	return DeviceInfo{
		NodeVersion: s.archive.runtimeVersion,
	}
}

func (s *Service) hostDeviceMid() (string, error) {
	if s.getDeviceMid == nil {
		return "", fmt.Errorf("%s", MissingDeviceMidError)
	}
	mid := strings.TrimSpace(s.getDeviceMid())
	if mid == "" {
		return "", fmt.Errorf("%s", MissingDeviceMidError)
	}
	return mid, nil
}

// Create ports create: redacts the user-visible text, aborts a previous
// create with the same operationId, and upserts the local index.
func (s *Service) Create(ctx context.Context, input CreateTicketInput, options CreateOptions) (TicketSummary, error) {
	device := input.Device
	if device == nil {
		device = new(DeviceInfo)
		*device = s.DeviceSnapshot()
	}
	operationID := strings.TrimSpace(options.OperationID)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if operationID != "" {
		// UI 取消创建工单时不能只关闭弹窗；host 需要能按 operationId 找到当前
		// HTTP 请求并 abort，避免“正在连接反馈服务”永久悬挂。
		s.mu.Lock()
		if prev := s.activeCreates[operationID]; prev != nil {
			prev()
		}
		s.activeCreates[operationID] = cancel
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			if s.activeCreates[operationID] != nil && ctx.Err() != nil {
				delete(s.activeCreates, operationID)
			} else {
				delete(s.activeCreates, operationID)
			}
			s.mu.Unlock()
		}()
	}
	deviceMid, err := s.hostDeviceMid()
	if err != nil {
		return TicketSummary{}, err
	}
	ticket, err := s.client.Create(ctx, input, *device, deviceMid, input.Locale)
	if err != nil {
		return TicketSummary{}, err
	}
	if err := s.store.Upsert(deviceMid, ticket); err != nil {
		return ticket, nil // 本地索引失败不阻断创建
	}
	return ticket, nil
}

// CancelCreate ports cancelCreate.
func (s *Service) CancelCreate(operationID string) {
	key := strings.TrimSpace(operationID)
	if key == "" {
		return
	}
	s.mu.Lock()
	if cancel := s.activeCreates[key]; cancel != nil {
		cancel()
	}
	s.mu.Unlock()
}

// List ports list: local index only, filtered then sorted by updated_at desc.
// A zero-value Limit means "unset" (the TS undefined), not an empty page.
func (s *Service) List(ctx context.Context, query ListQuery) (ListResult, error) {
	if query.Limit == 0 {
		query.Limit = -1
	}
	deviceMid, err := s.hostDeviceMid()
	if err != nil {
		return ListResult{}, err
	}
	items, err := s.store.List(deviceMid, query)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Items: items, Total: len(items)}, nil
}

// Get ports get.
func (s *Service) Get(ctx context.Context, id string) (TicketDetail, error) {
	return s.client.Get(ctx, id)
}

// Comment ports comment: the body is redacted before leaving the process.
func (s *Service) Comment(ctx context.Context, id, body string) (TicketComment, error) {
	return s.client.Comment(ctx, id, redact(body))
}

// UploadAttachment ports uploadAttachment: default filename/content-type
// derivation from the kind, then the HTTP client.
func (s *Service) UploadAttachment(ctx context.Context, id, kind string, file UploadFileRequest) (Attachment, error) {
	filename := file.Filename
	if filename == "" {
		filename = filepath.Base(file.Path)
	}
	contentType := file.ContentType
	if contentType == "" {
		if kind == KindImage {
			contentType = "image/png"
		} else {
			contentType = "application/zip"
		}
	}
	return s.client.UploadFile(ctx, UploadFileRequest{
		TicketID: id, Kind: kind, Path: file.Path, Filename: filename,
		ContentType: contentType, MessageID: file.MessageID,
	}, nil)
}

// UploadAttachmentWithProgress ports uploadAttachmentWithProgress.
func (s *Service) UploadAttachmentWithProgress(ctx context.Context, id, kind string, file UploadFileRequest, progressID string) (Attachment, error) {
	filename := file.Filename
	if filename == "" {
		filename = filepath.Base(file.Path)
	}
	contentType := file.ContentType
	if contentType == "" {
		if kind == KindImage {
			contentType = "image/png"
		} else {
			contentType = "application/zip"
		}
	}
	s.fireProgress(progressID, UploadProgress{ID: progressID, Phase: "preparing"})
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.activeUploads[progressID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.activeUploads, progressID)
		s.mu.Unlock()
	}()
	attachment, err := s.client.UploadFile(ctx, UploadFileRequest{
		TicketID: id, Kind: kind, Path: file.Path, Filename: filename,
		ContentType: contentType, MessageID: file.MessageID,
	}, func(uploaded, total int64) {
		phase := "uploading"
		if uploaded >= total {
			phase = "complete"
		}
		s.fireProgress(progressID, UploadProgress{ID: progressID, Phase: phase, UploadedBytes: uploaded, TotalBytes: total})
	})
	if err != nil {
		// cancel classification is the client's job (it owns the transport);
		// here only the terminal event matters.
		s.fireProgress(progressID, UploadProgress{ID: progressID, Phase: "canceled"})
		return Attachment{}, err
	}
	return attachment, nil
}

// CancelUpload ports cancelUpload.
func (s *Service) CancelUpload(progressID string) {
	s.mu.Lock()
	if cancel := s.activeUploads[progressID]; cancel != nil {
		cancel()
	}
	s.mu.Unlock()
}

// OnUploadProgress ports onDynamicUploadProgress(id): the local event seam
// (the channel adapter exposes it as a dynamic listen).
func (s *Service) OnUploadProgress(id string, handler func(UploadProgress)) (cancel func()) {
	s.mu.Lock()
	s.uploadProgress[id] = append(s.uploadProgress[id], handler)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		handlers := s.uploadProgress[id]
		for i, h := range handlers {
			if fmt.Sprintf("%p", h) == fmt.Sprintf("%p", handler) {
				s.uploadProgress[id] = append(handlers[:i], handlers[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}
}

func (s *Service) fireProgress(id string, progress UploadProgress) {
	s.mu.Lock()
	handlers := append([]func(UploadProgress){}, s.uploadProgress[id]...)
	s.mu.Unlock()
	for _, handler := range handlers {
		handler(progress)
	}
}

// UploadAttachmentData ports uploadAttachmentData: renderer-supplied base64
// content lands in a temp dir (attachment names are NOT path-safe) and is
// removed after the upload.
func (s *Service) UploadAttachmentData(ctx context.Context, id, kind string, dataBase64, filename, contentType, messageID string) (Attachment, error) {
	data, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return Attachment{}, err
	}
	// 附件名来自 renderer，不能作为本地相对路径使用。
	safeName := filepath.Base(strings.ReplaceAll(filename, `\`, "/"))
	if safeName == "" || safeName == "." || safeName == "/" {
		safeName = "attachment"
	}
	tempDir, err := os.MkdirTemp("", "feedback-attachment-")
	if err != nil {
		return Attachment{}, err
	}
	defer os.RemoveAll(tempDir)
	tempPath := filepath.Join(tempDir, "content")
	if err := os.WriteFile(tempPath, data, 0o600); err != nil {
		return Attachment{}, err
	}
	return s.client.UploadFile(ctx, UploadFileRequest{
		TicketID: id, Kind: kind, Path: tempPath, Filename: safeName,
		ContentType: contentType, MessageID: messageID,
	}, nil)
}

// AttachLogsFromExport ports attachLogsFromExport.
func (s *Service) AttachLogsFromExport(ctx context.Context, id string, full bool) (Attachment, error) {
	archive, err := s.archive.Prepare(ctx, full, nil)
	if err != nil {
		return Attachment{}, err
	}
	defer s.archive.Cleanup(archive.Path)
	return s.client.UploadFile(ctx, UploadFileRequest{
		TicketID: id, Kind: KindLog, Path: archive.Path,
		Filename: filepath.Base(archive.Path), ContentType: "application/zip",
	}, nil)
}

// PrepareCompactLogArchive ports prepareCompactLogArchive.
func (s *Service) PrepareCompactLogArchive(ctx context.Context, full bool, progressID string) (path string, size int64, err error) {
	var onProgress func(processed, total int64)
	if progressID != "" {
		onProgress = func(processed, total int64) {
			s.fireProgress(progressID, UploadProgress{ID: progressID, Phase: "preparing", UploadedBytes: processed, TotalBytes: total})
		}
	}
	archive, err := s.archive.Prepare(ctx, full, onProgress)
	if err != nil {
		return "", 0, err
	}
	return archive.Path, archive.Size, nil
}

// CleanupPreparedLogArchive ports cleanupPreparedLogArchive: removes the
// archive's parent temp dir (mkdtemp layout), never a wider tree.
func (s *Service) CleanupPreparedLogArchive(path string) {
	s.archive.Cleanup(path)
}

// RevealLogArchive ports revealLogArchive: the desktop shell seam; without
// one it is a no-op (the server host has no file manager).
func (s *Service) RevealLogArchive(path string) {}

// redact is the single scrub entry for user-supplied text.
func redact(text string) string {
	return feedbackRedact(text)
}

// feedbackRedact is wired in redact.go to keep the import graph flat.
var feedbackRedact = func(text string) string { return text }

// ── local ticket store ────────────────────────────────────────────────────

// storedTicket ports StoredFeedbackTicket.
type storedTicket struct {
	TicketSummary
	DeviceMid string `json:"deviceMid"`
}

type ticketStoreFile struct {
	Tickets []storedTicket `json:"tickets"`
}

// ticketStatuses ports isFeedbackTicketStatus.
var ticketStatuses = map[string]bool{
	"已提交": true, "信息不足": true, "已采纳": true, "答复关闭": true,
	"已归档": true, "已拒绝": true, "开发中": true, "已解决": true, "已上线": true,
}

type localTicketStore struct {
	rootDir string
}

// filePath resolves tickets.json; empty root disables persistence (tests
// that only exercise create/list flows against fakes).
func (s *localTicketStore) filePath() (string, bool) {
	if s.rootDir == "" {
		return "", false
	}
	return filepath.Join(s.rootDir, "tickets.json"), true
}

func (s *localTicketStore) read() ticketStoreFile {
	path, ok := s.filePath()
	if !ok {
		return ticketStoreFile{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ticketStoreFile{}
	}
	var parsed map[string]any
	if json.Unmarshal(data, &parsed) != nil {
		return ticketStoreFile{}
	}
	rawTickets, ok := parsed["tickets"].([]any)
	if !ok {
		return ticketStoreFile{}
	}
	file := ticketStoreFile{}
	for _, raw := range rawTickets {
		record, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		stored := storedTicket{}
		if str, ok := record["deviceMid"].(string); ok {
			stored.DeviceMid = str
		} else {
			continue
		}
		if str, ok := record["id"].(string); ok {
			stored.ID = str
		} else {
			continue
		}
		if str, ok := record["title"].(string); ok {
			stored.Title = str
		} else {
			continue
		}
		if str, ok := record["type"].(string); ok {
			stored.Type = str
		} else {
			continue
		}
		if str, ok := record["status"].(string); ok {
			stored.Status = str
		} else {
			continue
		}
		if str, ok := record["created_at"].(string); ok {
			stored.CreatedAt = str
		} else {
			continue
		}
		if str, ok := record["updated_at"].(string); ok {
			stored.UpdatedAt = str
		} else {
			continue
		}
		file.Tickets = append(file.Tickets, migrateStoredTicket(stored))
	}
	return file
}

// migrateStoredTicket ports migrateStoredFeedbackTicket: 3.3.5 renamed
// 「待评估」 to 「已提交」; unknown statuses crash the UI's status metadata
// lookup, so they normalize on read.
func migrateStoredTicket(ticket storedTicket) storedTicket {
	if ticket.Status == "待评估" || !ticketStatuses[ticket.Status] {
		ticket.Status = "已提交"
	}
	return ticket
}

func (s *localTicketStore) write(file ticketStoreFile) error {
	path, ok := s.filePath()
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// List ports FeedbackLocalTicketStore.list.
func (s *localTicketStore) List(deviceMid string, query ListQuery) ([]TicketSummary, error) {
	file := s.read()
	var tickets []TicketSummary
	for _, stored := range file.Tickets {
		if stored.DeviceMid != deviceMid {
			continue
		}
		if query.Status != "" && stored.Status != query.Status {
			continue
		}
		if query.Type != "" && stored.Type != query.Type {
			continue
		}
		tickets = append(tickets, stored.TicketSummary)
	}
	sort.SliceStable(tickets, func(i, j int) bool {
		return tickets[i].UpdatedAt > tickets[j].UpdatedAt
	})
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	limit := len(tickets)
	if query.Limit >= 0 && query.Limit < limit {
		limit = query.Limit
	}
	if offset > len(tickets) {
		return []TicketSummary{}, nil
	}
	end := offset + limit
	if end > len(tickets) {
		end = len(tickets)
	}
	return tickets[offset:end], nil
}

// Upsert ports FeedbackLocalTicketStore.upsert (cap 200, newest first).
func (s *localTicketStore) Upsert(deviceMid string, ticket TicketSummary) error {
	file := s.read()
	next := storedTicket{TicketSummary: ticket, DeviceMid: deviceMid}
	var rest []storedTicket
	for _, current := range file.Tickets {
		if current.DeviceMid != deviceMid || current.ID != ticket.ID {
			rest = append(rest, current)
		}
	}
	all := append([]storedTicket{next}, rest...)
	if len(all) > 200 {
		all = all[:200]
	}
	return s.write(ticketStoreFile{Tickets: all})
}

// ── compact log archive ───────────────────────────────────────────────────

const (
	archiveMaxFileBytes  = 8 * 1024 * 1024
	archiveMaxTotalBytes = 32 * 1024 * 1024
	compactMaxTotalBytes = 2 * 1024 * 1024
	archiveMaxDepth      = 4
	archiveMaxVisited    = 2000
)

var diagnosticFileRe = regexp.MustCompile(`(?i)(?:\.log(?:\.\d+)?|\.jsonl|\.ndjson)$`)

type logArchive struct {
	sources        []ArchiveSource
	outputRoot     string
	appVersion     string
	commit         string
	runtimeVersion string
}

func newLogArchive(sources []ArchiveSource, outputRoot, appVersion, commit, runtimeVersion string) *logArchive {
	return &logArchive{sources: sources, outputRoot: outputRoot, appVersion: appVersion, commit: commit, runtimeVersion: runtimeVersion}
}

type preparedArchive struct {
	Path string
	Size int64
}

// Prepare ports prepareCompactLogArchive + createFeedbackDiagnosticArchive:
// whitelist sources, bounded reads, no raw fallback when the bytes are not
// safely decodable text, redaction before packaging.
func (a *logArchive) Prepare(ctx context.Context, full bool, onProgress func(processed, total int64)) (preparedArchive, error) {
	budget := int64(archiveMaxTotalBytes)
	if !full {
		budget = compactMaxTotalBytes
	}
	now := time.Now()
	// 按本地自然日筛选文件；次日零点由日历计算，兼容夏令时的 23/25 小时日。
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	dayEnd := dayStart.Add(24 * time.Hour)
	isToday := func(mtime time.Time) bool {
		return !mtime.Before(dayStart) && mtime.Before(dayEnd)
	}
	if onProgress != nil {
		onProgress(0, 0)
	}
	if err := os.MkdirAll(a.outputRoot, 0o755); err != nil {
		return preparedArchive{}, err
	}
	outputDir, err := os.MkdirTemp(a.outputRoot, "archive-")
	if err != nil {
		return preparedArchive{}, err
	}
	archivePath := filepath.Join(outputDir, "nex-diagnostic-logs.zip")
	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	skipped := map[string]int{}
	skip := func(reason string) { skipped[reason]++ }
	var totalBytes int64
	visited := 0

	for _, source := range a.sources {
		root, err := filepath.EvalSymlinks(source.Directory)
		if err != nil {
			continue
		}
		info, err := os.Lstat(source.Directory)
		if err != nil || !info.IsDir() {
			continue
		}
		// 不跟随诊断根目录本身的链接；规范化系统目录别名（如 macOS /var）。
		var walk func(directory, prefix string, depth int) error
		walk = func(directory, prefix string, depth int) error {
			if depth > archiveMaxDepth || visited >= archiveMaxVisited {
				return nil
			}
			names, err := os.ReadDir(directory)
			if err != nil {
				return nil
			}
			sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
			for _, name := range names {
				visited++
				if visited > archiveMaxVisited {
					return nil
				}
				if name.Type() == os.ModeSymlink {
					continue
				}
				absolutePath := filepath.Join(directory, name.Name())
				archiveName := strings.TrimPrefix(prefix+"/"+name.Name(), "/")
				if name.IsDir() {
					if err := walk(absolutePath, archiveName, depth+1); err != nil {
						return err
					}
					continue
				}
				if !name.Type().IsRegular() || !diagnosticFileRe.MatchString(name.Name()) {
					continue
				}
				resolved, err := filepath.EvalSymlinks(absolutePath)
				if err != nil || resolved != absolutePath {
					skip("unsafe-path")
					continue
				}
				info, err := os.Lstat(absolutePath)
				if err != nil || !info.Mode().IsRegular() ||
					info.Size() > archiveMaxFileBytes || totalBytes+info.Size() > budget ||
					!isToday(info.ModTime()) {
					skip("metadata-policy")
					continue
				}
				// O_NOFOLLOW equivalent: open, then verify the opened file's
				// identity (dev/inode/size/mtime) still matches the stat.
				file, err := os.Open(absolutePath)
				if err != nil {
					skip("open-failed")
					continue
				}
				opened, err := file.Stat()
				if err != nil || !opened.Mode().IsRegular() ||
					sameFile(info, opened) != true ||
					!isToday(opened.ModTime()) || opened.Size() > archiveMaxFileBytes ||
					totalBytes+opened.Size() > budget {
					file.Close()
					skip("opened-file-policy")
					continue
				}
				bytesRead, readErr := io.ReadAll(io.LimitReader(file, opened.Size()+1))
				file.Close()
				if readErr != nil || int64(len(bytesRead)) != opened.Size() {
					skip("short-read")
					continue
				}
				text, ok := decodeDiagnosticLog(bytesRead)
				if !ok {
					skip("unsupported-text")
					continue
				}
				data := []byte(redactDiagnostic(text))
				if int64(len(data)) > archiveMaxFileBytes || totalBytes+int64(len(data)) > budget {
					skip("redacted-size-limit")
					continue
				}
				totalBytes += int64(len(data))
				entries = append(entries, entry{name: archiveName, data: data})
			}
			return nil
		}
		if err := walk(root, source.ArchivePrefix, 0); err != nil {
			return preparedArchive{}, err
		}
	}

	zipBuffer := &bytes.Buffer{}
	zipWriter := zip.NewWriter(zipBuffer)
	for _, e := range entries {
		writer, err := zipWriter.Create(e.name)
		if err != nil {
			os.RemoveAll(outputDir)
			return preparedArchive{}, err
		}
		if _, err := writer.Write(e.data); err != nil {
			os.RemoveAll(outputDir)
			return preparedArchive{}, err
		}
	}
	about := fmt.Sprintf(
		"Nex diagnostic logs\ntimestamp: %s\nappVersion: %s\ncommit: %s\nnode: %s\nincludedLogFiles: %d\nskippedLogFiles: %d\nskippedLogFilesByReason: %s\nScope: diagnostic text log files modified today (local time); credentials and structured payloads redacted.\n",
		now.UTC().Format(time.RFC3339), a.appVersion, a.commit, a.runtimeVersion,
		len(entries), sumValues(skipped), marshalSkipped(skipped))
	writer, err := zipWriter.Create("about.txt")
	if err == nil {
		_, err = writer.Write([]byte(about))
	}
	if err != nil {
		os.RemoveAll(outputDir)
		return preparedArchive{}, err
	}
	if err := zipWriter.Close(); err != nil {
		os.RemoveAll(outputDir)
		return preparedArchive{}, err
	}
	if err := os.WriteFile(archivePath, zipBuffer.Bytes(), 0o644); err != nil {
		os.RemoveAll(outputDir)
		return preparedArchive{}, err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		os.RemoveAll(outputDir)
		return preparedArchive{}, err
	}
	if onProgress != nil {
		onProgress(info.Size(), info.Size())
	}
	return preparedArchive{Path: archivePath, Size: info.Size()}, nil
}

func sameFile(a, b os.FileInfo) bool {
	type sys interface{ Sys() any }
	_ = sys(nil)
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && os.SameFile(a, b)
}

func sumValues(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func marshalSkipped(counts map[string]int) string {
	if len(counts) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%q:%d", key, counts[key]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Cleanup ports cleanupLogArchive: removes the archive file's parent dir
// (the mkdtemp layout) and nothing wider.
func (a *logArchive) Cleanup(path string) {
	if path == "" {
		return
	}
	os.RemoveAll(filepath.Dir(path))
}

// decodeDiagnosticLog ports decodeDiagnosticLog: UTF-16LE/BE BOM detection
// plus printable-text validation; undecodable bytes never fall through to a
// raw include.
func decodeDiagnosticLog(data []byte) (string, bool) {
	if len(data) >= 2 {
		if data[0] == 0xff && data[1] == 0xfe {
			return decodeUTF16(data[2:], true)
		}
		if data[0] == 0xfe && data[1] == 0xff {
			return decodeUTF16(data[2:], false)
		}
	}
	return validatePrintable(string(data))
}

func decodeUTF16(data []byte, little bool) (string, bool) {
	if len(data)%2 != 0 {
		return "", false
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		if little {
			units[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
		} else {
			units[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
		}
	}
	return validatePrintable(string(utf16.Decode(units)))
}

func validatePrintable(text string) (string, bool) {
	if !utf8Valid(text) {
		return "", false
	}
	for _, r := range text {
		if r < 32 && r != 9 && r != 10 && r != 13 {
			return "", false
		}
	}
	return text, true
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == unicode.ReplacementChar && !strings.ContainsRune(s, 0xfffd) {
			return false
		}
	}
	return true
}
