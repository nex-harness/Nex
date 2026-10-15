// channel.go: the rpc.Channel adapter for IConversationShareService
// ("conversation-share"): getCapabilities, preflight, publish,
// onDynamicPublishProgress(id), importShare, onDynamicImportProgress(id),
// getImportedConversation, getPreview, getContinuation.
package conversationshare

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one IConversationShareService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "getCapabilities":
		capabilities, err := c.Service.GetCapabilities(ctx)
		if err != nil {
			return nil, toRPCError(err)
		}
		return capabilities, nil
	case "preflight":
		var params struct {
			WorkspacePath     string    `json:"workspacePath"`
			WorkspaceIdentity string    `json:"workspaceIdentity"`
			RemoteSessionID   string    `json:"remoteSessionId"`
			SessionID         string    `json:"sessionId"`
			Selection         Selection `json:"selection"`
		}
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		result, err := c.Service.Preflight(ctx, PreflightInput{
			WorkspaceRef: WorkspaceRef{
				WorkspacePath: params.WorkspacePath, WorkspaceIdentity: params.WorkspaceIdentity,
				RemoteSessionID: params.RemoteSessionID, SessionID: params.SessionID,
			},
			Selection: params.Selection,
		})
		if err != nil {
			return nil, toRPCError(err)
		}
		return result, nil
	case "publish":
		var params struct {
			Input struct {
				WorkspacePath        string    `json:"workspacePath"`
				WorkspaceIdentity    string    `json:"workspaceIdentity"`
				RemoteSessionID      string    `json:"remoteSessionId"`
				SessionID            string    `json:"sessionId"`
				Title                string    `json:"title"`
				AccessMode           string    `json:"accessMode"`
				Selection            Selection `json:"selection"`
				ClientRequestID      string    `json:"clientRequestId"`
				DisclosureAcceptedAt float64   `json:"disclosureAcceptedAt"`
				Locale               string    `json:"locale"`
			} `json:"input"`
			OperationID string `json:"operationId"`
		}
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		disclosure := int64(0)
		if params.Input.DisclosureAcceptedAt == float64(int64(params.Input.DisclosureAcceptedAt)) {
			disclosure = int64(params.Input.DisclosureAcceptedAt)
		}
		record, err := c.Service.Publish(ctx, PublishInput{
			WorkspaceRef: WorkspaceRef{
				WorkspacePath: params.Input.WorkspacePath, WorkspaceIdentity: params.Input.WorkspaceIdentity,
				RemoteSessionID: params.Input.RemoteSessionID, SessionID: params.Input.SessionID,
			},
			Title: params.Input.Title, AccessMode: params.Input.AccessMode,
			Selection: params.Input.Selection, ClientRequestID: params.Input.ClientRequestID,
			DisclosureAcceptedAt: disclosure, Locale: params.Input.Locale,
		}, params.OperationID)
		if err != nil {
			return nil, toRPCError(err)
		}
		return record, nil
	case "importShare":
		var params struct {
			Input struct {
				ShareCode               string `json:"shareCode"`
				ClientRequestID         string `json:"clientRequestId"`
				TargetWorkspacePath     string `json:"targetWorkspacePath"`
				TargetWorkspaceIdentity string `json:"targetWorkspaceIdentity"`
				TargetWorkspaceKind     string `json:"targetWorkspaceKind"`
				Locale                  string `json:"locale"`
			} `json:"input"`
			OperationID string `json:"operationId"`
		}
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		result, err := c.Service.ImportShare(ctx, ImportInput{
			ShareCode: params.Input.ShareCode, ClientRequestID: params.Input.ClientRequestID,
			TargetWorkspacePath:     params.Input.TargetWorkspacePath,
			TargetWorkspaceIdentity: params.Input.TargetWorkspaceIdentity,
			TargetWorkspaceKind:     params.Input.TargetWorkspaceKind, Locale: params.Input.Locale,
		}, params.OperationID)
		if err != nil {
			return nil, toRPCError(err)
		}
		return result, nil
	case "getImportedConversation":
		var params struct {
			WorkspacePath string `json:"workspacePath"`
			ContextID     string `json:"contextId"`
		}
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		imported, err := c.Service.GetImportedConversation(ctx, params.WorkspacePath, params.ContextID)
		if err != nil {
			return nil, toRPCError(err)
		}
		return imported, nil
	case "getPreview":
		var shareCode string
		if len(args) > 0 {
			shareCode, _ = args[0].(string)
		}
		preview, err := c.Service.GetPreview(ctx, shareCode)
		if err != nil {
			return nil, toRPCError(err)
		}
		return preview, nil
	case "getContinuation":
		var params struct {
			ShareCode       string `json:"shareCode"`
			ClientRequestID string `json:"clientRequestId"`
		}
		if err := decodeArg(args, &params); err != nil {
			return nil, err
		}
		continuation, err := c.Service.GetContinuation(ctx, params.ShareCode, params.ClientRequestID)
		if err != nil {
			return nil, toRPCError(err)
		}
		return continuation, nil
	default:
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
}

// Listen serves onDynamicPublishProgress(id) / onDynamicImportProgress(id).
func (c *Channel) Listen(ctx context.Context, event string, arg rpc.Value) (<-chan rpc.Value, func(), error) {
	switch event {
	case "onDynamicPublishProgress":
		operationID := dynamicIDOf(arg)
		source := make(chan PublishProgress, 64)
		cancel := c.Service.OnPublishProgress(operationID, func(progress PublishProgress) {
			select {
			case source <- progress:
			default: // a slow subscriber must not block the publisher
			}
		})
		stream, stop := pumpProgress(ctx, source, func(progress PublishProgress) rpc.Value {
			return rpc.NewObject(
				"operationId", progress.OperationID,
				"phase", progress.Phase,
				"completedArtifacts", progress.CompletedArtifacts,
				"totalArtifacts", progress.TotalArtifacts,
				"warnings", progress.Warnings,
				"omittedWarningCount", progress.OmittedWarningCount,
			)
		})
		return stream, func() { cancel(); stop() }, nil
	case "onDynamicImportProgress":
		operationID := dynamicIDOf(arg)
		source := make(chan ImportProgress, 64)
		cancel := c.Service.OnImportProgress(operationID, func(progress ImportProgress) {
			select {
			case source <- progress:
			default:
			}
		})
		stream, stop := pumpProgress(ctx, source, func(progress ImportProgress) rpc.Value {
			return rpc.NewObject(
				"operationId", progress.OperationID,
				"phase", progress.Phase,
				"completedArtifacts", progress.CompletedArtifacts,
				"totalArtifacts", progress.TotalArtifacts,
			)
		})
		return stream, func() { cancel(); stop() }, nil
	default:
		return nil, nil, &rpc.Error{Message: "Event not found: " + event}
	}
}

func dynamicIDOf(arg rpc.Value) string {
	switch v := arg.(type) {
	case string:
		return v
	case map[string]any:
		if raw, ok := v["id"].(string); ok {
			return raw
		}
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

// pumpProgress forwards source into an rpc.Value channel. stop closes the
// stream: the Listen cancel func must stop it even when the request context
// stays alive, so stop is what the caller returns — no watcher goroutine that
// only ever wakes on ctx.Done().
func pumpProgress[T any](ctx context.Context, source <-chan T, encode func(T) rpc.Value) (<-chan rpc.Value, func()) {
	out := make(chan rpc.Value, 64)
	done := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(done) }) }
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
				select {
				case out <- encode(progress):
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}
	}()
	return out, stop
}

// toRPCError maps a service/client error onto the rpc passthrough fields
// (kind/code/status/retryAfterMs/details — the TS Error subclass members).
func toRPCError(err error) error {
	if err == nil {
		return nil
	}
	if typed, ok := isServiceError(err); ok {
		rpcErr := &rpc.Error{Name: "ConversationShareServiceError", Message: typed.message, Kind: typed.kind}
		if typed.details != nil {
			rpcErr.Details = typed.details
		}
		return rpcErr
	}
	if typed, ok := isClientError(err); ok {
		rpcErr := &rpc.Error{Name: "ConversationShareServiceError", Message: typed.message, Kind: typed.kind}
		if typed.status != 0 {
			rpcErr.Status = typed.status
		}
		if typed.hasCode {
			rpcErr.Code = typed.code
		}
		if typed.requestID != "" {
			rpcErr.Detail = map[string]any{"requestId": typed.requestID}
		}
		if typed.hasRetry {
			rpcErr.RetryAfterMs = typed.retryAfterMs
		}
		return rpcErr
	}
	return err
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
