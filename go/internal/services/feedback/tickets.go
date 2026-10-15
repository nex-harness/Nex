// tickets.go holds the ticket query and comment methods: list, get and
// comment. Split out of feedback.go; symbols are unchanged.
package feedback

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
