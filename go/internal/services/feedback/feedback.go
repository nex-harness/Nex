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
	"context"
	"fmt"
	"strings"
	"sync"
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
