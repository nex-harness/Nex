// channel.go: the rpc.Channel adapter for IFeedbackService ("feedback") and
// the onDynamicUploadProgress(id) dynamic event.
package feedback

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one IFeedbackService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "create":
		var params struct {
			Input   json.RawMessage `json:"input"`
			Options *struct {
				OperationID string `json:"operationId"`
			} `json:"options"`
		}
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		var input CreateTicketInput
		if len(params.Input) > 0 {
			if err := json.Unmarshal(params.Input, &input); err != nil {
				return nil, &rpc.Error{Message: "invalid params: " + err.Error()}
			}
		}
		options := CreateOptions{}
		if params.Options != nil {
			options.OperationID = params.Options.OperationID
		}
		ticket, err := c.Service.Create(ctx, input, options)
		if err != nil {
			return nil, err
		}
		return ticket, nil
	case "cancelCreate":
		var operationID string
		if len(args) > 0 {
			operationID, _ = args[0].(string)
		}
		c.Service.CancelCreate(operationID)
		return nil, nil
	case "list":
		var query ListQuery
		if len(args) > 0 {
			if err := decodeArg(args, &query); err != nil {
				return nil, err
			}
		}
		return c.Service.List(ctx, query)
	case "get":
		var id string
		if len(args) > 0 {
			id, _ = args[0].(string)
		}
		return c.Service.Get(ctx, id)
	case "comment":
		if len(args) < 2 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		id, _ := args[0].(string)
		body, _ := args[1].(string)
		return c.Service.Comment(ctx, id, body)
	case "uploadAttachment":
		return c.callUpload(ctx, args, false)
	case "uploadAttachmentWithProgress":
		return c.callUpload(ctx, args, true)
	case "cancelUpload":
		var progressID string
		if len(args) > 0 {
			progressID, _ = args[0].(string)
		}
		c.Service.CancelUpload(progressID)
		return nil, nil
	case "uploadAttachmentData":
		var params struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			File struct {
				DataBase64  string `json:"dataBase64"`
				Filename    string `json:"filename"`
				ContentType string `json:"contentType"`
				MessageID   string `json:"messageId"`
			} `json:"file"`
		}
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		return c.Service.UploadAttachmentData(ctx, params.ID, params.Kind,
			params.File.DataBase64, params.File.Filename, params.File.ContentType, params.File.MessageID)
	case "attachLogsFromExport":
		var params struct {
			ID      string `json:"id"`
			Options *struct {
				Full bool `json:"full"`
			} `json:"options"`
		}
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		full := params.Options != nil && params.Options.Full
		return c.Service.AttachLogsFromExport(ctx, params.ID, full)
	case "getDeviceSnapshot":
		return c.Service.DeviceSnapshot(), nil
	case "prepareCompactLogArchive":
		var params struct {
			Full       bool   `json:"full"`
			ProgressID string `json:"progressId"`
		}
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		if len(args) > 0 {
			if m, ok := args[0].(map[string]any); ok {
				if v, ok := m["options"].(map[string]any); ok {
					if full, ok := v["full"].(bool); ok {
						params.Full = full
					}
					if pid, ok := v["progressId"].(string); ok {
						params.ProgressID = pid
					}
				}
			}
		}
		path, size, err := c.Service.PrepareCompactLogArchive(ctx, params.Full, params.ProgressID)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("path", path, "size", size), nil
	case "cleanupPreparedLogArchive":
		var path string
		if len(args) > 0 {
			path, _ = args[0].(string)
		}
		c.Service.CleanupPreparedLogArchive(path)
		return nil, nil
	case "revealLogArchive":
		var path string
		if len(args) > 0 {
			path, _ = args[0].(string)
		}
		c.Service.RevealLogArchive(path)
		return nil, nil
	default:
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
}

// callUpload handles the two upload shapes (with and without progressId).
func (c *Channel) callUpload(ctx context.Context, args []rpc.Value, withProgress bool) (rpc.Value, error) {
	var params struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
		File struct {
			Path        string `json:"path"`
			Filename    string `json:"filename"`
			ContentType string `json:"contentType"`
			MessageID   string `json:"messageId"`
		} `json:"file"`
		ProgressID string `json:"progressId"`
	}
	if err := decodeArg(args, &params); err != nil {
		return nil, err
	}
	request := UploadFileRequest{
		TicketID: params.ID, Kind: params.Kind, Path: params.File.Path,
		Filename: params.File.Filename, ContentType: params.File.ContentType,
		MessageID: params.File.MessageID,
	}
	if withProgress {
		return c.Service.UploadAttachmentWithProgress(ctx, params.ID, params.Kind, request, params.ProgressID)
	}
	return c.Service.UploadAttachment(ctx, params.ID, params.Kind, request)
}

// Listen serves onDynamicUploadProgress(id).
func (c *Channel) Listen(ctx context.Context, event string, arg rpc.Value) (<-chan rpc.Value, func(), error) {
	if event != "onDynamicUploadProgress" {
		return nil, nil, &rpc.Error{Message: "Event not found: " + event}
	}
	id := ""
	switch v := arg.(type) {
	case string:
		id = v
	case map[string]any:
		if raw, ok := v["id"].(string); ok {
			id = raw
		}
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				id = s
			}
		}
	}
	source := make(chan UploadProgress, 64)
	cancel := c.Service.OnUploadProgress(id, func(progress UploadProgress) {
		select {
		case source <- progress:
		default: // a slow subscriber must not block the uploader
		}
	})
	out := make(chan rpc.Value, 64)
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case progress, ok := <-source:
				if !ok {
					return
				}
				obj := rpc.NewObject(
					"id", progress.ID,
					"phase", progress.Phase,
					"uploadedBytes", progress.UploadedBytes,
					"totalBytes", progress.TotalBytes,
				)
				select {
				case out <- obj:
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		close(done)
	}()
	return out, func() { cancel(); close(done) }, nil
}

func decodeArg(args []rpc.Value, target any) error {
	if len(args) == 0 {
		return nil
	}
	data, err := json.Marshal(args[0])
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return &rpc.Error{Message: "invalid params: " + err.Error()}
	}
	return nil
}
