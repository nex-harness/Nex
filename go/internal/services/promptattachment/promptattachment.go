// Package promptattachment is the local IPromptAttachmentTransferService
// port (step 3.8): the renderer consumes this host service instead of
// depending on the SSH/WSL/Docker backends directly. The LOCAL host keeps
// localPath zero-copy and never fakes upload progress; the remote-host
// wrapper (cross-machine staging) is the desktop host's concern, not the
// server's — it stays on the Node side with the remote backends.
//
// Source: packages/services/src/prompt-attachment-transfer/*.ts.
package promptattachment

import (
	"context"
	"os"
	"sync"

	"github.com/tylinux/nex/go/internal/rpc"
)

// ChannelName is the rpc channel name (ServiceChannels.PromptAttachmentTransfer).
const ChannelName = "prompt-attachment-transfer"

// StageParams ports PromptAttachmentStageParams.
type StageParams struct {
	OperationID       string `json:"operationId"`
	SessionID         string `json:"sessionId"`
	WorkspacePath     string `json:"workspacePath"`
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
	RemoteSessionID   string `json:"remoteSessionId,omitempty"`
	LocalPath         string `json:"localPath"`
	FileName          string `json:"fileName"`
	Mime              string `json:"mime"`
	SizeBytes         int64  `json:"sizeBytes,omitempty"`
}

// StageResult ports PromptAttachmentStageResult.
type StageResult struct {
	OperationID string `json:"operationId"`
	Ref         string `json:"ref"`
	Bytes       int64  `json:"bytes"`
	Staged      bool   `json:"staged"`
}

// Progress ports PromptAttachmentTransferProgress.
type Progress struct {
	OperationID   string `json:"operationId"`
	Phase         string `json:"phase"` // uploading | committing | complete | canceled
	UploadedBytes int64  `json:"uploadedBytes"`
	TotalBytes    int64  `json:"totalBytes"`
}

// Service ports the local IPromptAttachmentTransferService.
type Service struct {
	mu       sync.Mutex
	handlers map[string][]func(Progress)
}

// New ports createLocalPromptAttachmentTransferService.
func New() *Service {
	return &Service{handlers: map[string][]func(Progress){}}
}

// Stage ports stage: local workspace keeps localPath zero-copy (staged=false
// — nothing was copied), size from params or stat.
func (s *Service) Stage(ctx context.Context, params StageParams) (StageResult, error) {
	bytes := params.SizeBytes
	if bytes <= 0 {
		if info, err := os.Stat(params.LocalPath); err == nil {
			bytes = info.Size()
		} else {
			bytes = 0
		}
	}
	return StageResult{
		OperationID: params.OperationID,
		Ref:         params.LocalPath,
		Bytes:       bytes,
		Staged:      false,
	}, nil
}

// Adopt / Cancel / Cleanup are no-ops for the local host (nothing to adopt,
// cancel or clean — the ref IS the local path).
func (s *Service) Adopt(ctx context.Context, operationID string)   {}
func (s *Service) Cancel(ctx context.Context, operationID string)  {}
func (s *Service) Cleanup(ctx context.Context, operationID string) {}

// OnProgress ports onDynamicProgress(operationId): the local event seam.
// The local host never fires it (no upload happens); the stream exists so
// the renderer's subscription does not error.
func (s *Service) OnProgress(operationID string, handler func(Progress)) (cancel func()) {
	s.mu.Lock()
	s.handlers[operationID] = append(s.handlers[operationID], handler)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		handlers := s.handlers[operationID]
		for i, h := range handlers {
			if &h == &handler {
				s.handlers[operationID] = append(handlers[:i], handlers[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}
}

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches stage/adopt/cancel/cleanup.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "stage":
		var params StageParams
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		return c.Service.Stage(ctx, params)
	case "adopt", "cancel", "cleanup":
		var operationID string
		if len(args) > 0 {
			operationID, _ = args[0].(string)
		}
		switch method {
		case "adopt":
			c.Service.Adopt(ctx, operationID)
		case "cancel":
			c.Service.Cancel(ctx, operationID)
		case "cleanup":
			c.Service.Cleanup(ctx, operationID)
		}
		return nil, nil
	default:
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
}

// Listen serves onDynamicProgress(operationId).
func (c *Channel) Listen(ctx context.Context, event string, arg rpc.Value) (<-chan rpc.Value, func(), error) {
	if event != "onDynamicProgress" {
		return nil, nil, &rpc.Error{Message: "Event not found: " + event}
	}
	operationID := ""
	switch v := arg.(type) {
	case string:
		operationID = v
	case map[string]any:
		if raw, ok := v["id"].(string); ok {
			operationID = raw
		}
	}
	// The local host never fires progress; subscribe to a closed stream so a
	// stale id cannot break the subscription path (the TS Event.None shape).
	out := make(chan rpc.Value)
	source := make(chan Progress)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case progress, ok := <-source:
				if !ok {
					return
				}
				obj := rpc.NewObject(
					"operationId", progress.OperationID,
					"phase", progress.Phase,
					"uploadedBytes", progress.UploadedBytes,
					"totalBytes", progress.TotalBytes,
				)
				select {
				case out <- obj:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	cancel := c.Service.OnProgress(operationID, func(progress Progress) {
		select {
		case source <- progress:
		default:
		}
	})
	close(source) // the local service never fires; unblock the pump
	return out, cancel, nil
}

func decodeArg(args []rpc.Value, target any) error {
	if len(args) == 0 {
		return nil
	}
	data, err := jsonMarshal(args[0])
	if err != nil {
		return err
	}
	if err := jsonUnmarshal(data, target); err != nil {
		return &rpc.Error{Message: "invalid params: " + err.Error()}
	}
	return nil
}
