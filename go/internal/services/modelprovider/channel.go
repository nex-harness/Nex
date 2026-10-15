// channel.go: the rpc.Channel adapters for IProviderSettingsService
// ("provider-settings") and IModelSelectionService ("model-selection")
// (providerFacadeServices.ts). Events: onDidChange for both.
package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/tylinux/nex/go/internal/providerconfig"
	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel names (ServiceChannels).
const (
	ChannelProviderSettings = "provider-settings"
	ChannelModelSelection   = "model-selection"
)

// SettingsChannel adapts the provider-settings service.
type SettingsChannel struct{ Service *ProviderSettingsService }

var _ rpc.Channel = (*SettingsChannel)(nil)

// Call dispatches one IProviderSettingsService method.
func (c *SettingsChannel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	switch method {
	case "getView":
		view, err := c.Service.GetView(ctx)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "refresh":
		reason, _ := args[0].(string)
		view, err := c.Service.Refresh(ctx, reason)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "onDidChange":
		return nil, &rpc.Error{Message: "Event not found: " + method}
	case "createPersonalProvider":
		var input struct {
			TemplateID    string                         `json:"templateId"`
			ProviderName  string                         `json:"providerName"`
			Locale        string                         `json:"locale"`
			InitialConfig *providerconfig.ProviderConfig `json:"initialConfig"`
		}
		if args[0] != nil {
			if err := decodeArg(args[0], &input); err != nil {
				return nil, err
			}
		}
		return c.Service.CreatePersonalProvider(ctx, input.TemplateID, input.ProviderName, input.Locale, input.InitialConfig)
	case "resolveModelConfig":
		var input struct {
			ProviderID      string         `json:"providerId"`
			OriginalModelID string         `json:"originalModelId,omitempty"`
			ModelID         string         `json:"modelId"`
			PersonalConfig  map[string]any `json:"personalConfig,omitempty"`
		}
		if err := decodeArg(args[0], &input); err != nil {
			return nil, err
		}
		return c.Service.ResolveModelConfig(ctx, &input.ProviderID, &input.OriginalModelID, &input.ModelID, input.PersonalConfig)
	case "lookupModelInfo":
		modelID, _ := args[0].(string)
		return c.Service.LookupModelInfo(ctx, modelID)
	case "listRemoteModels":
		providerID, _ := args[0].(string)
		return c.Service.ListRemoteModels(ctx, providerID)
	case "savePersonalProviderOverlay":
		providerID, _ := args[0].(string)
		config, err := decodeProviderConfigArg(args[1])
		if err != nil {
			return nil, err
		}
		var metadata map[string]any
		if len(args) > 2 && args[2] != nil {
			if err := decodeArg(args[2], &metadata); err != nil {
				return nil, err
			}
		}
		view, err := c.Service.SavePersonalProviderOverlay(ctx, providerID, config, metadata)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "deletePersonalProvider":
		providerID, _ := args[0].(string)
		view, err := c.Service.DeletePersonalProvider(ctx, providerID)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "reorderPersonalProviders":
		providerIDs, err := stringSliceArg(args[0])
		if err != nil {
			return nil, err
		}
		view, err := c.Service.ReorderPersonalProviders(ctx, providerIDs)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "reorderPersonalModels":
		providerID, _ := args[0].(string)
		modelIDs, err := stringSliceArg(args[1])
		if err != nil {
			return nil, err
		}
		view, err := c.Service.ReorderPersonalModels(ctx, providerID, modelIDs)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "addPersonalModel":
		return c.mutateModel(ctx, args, false)
	case "renamePersonalModel":
		providerID, _ := args[0].(string)
		currentModelID, _ := args[1].(string)
		nextModelID, _ := args[2].(string)
		view, err := c.Service.RenamePersonalModel(ctx, providerID, currentModelID, nextModelID)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "deletePersonalModel":
		providerID, _ := args[0].(string)
		modelID, _ := args[1].(string)
		view, err := c.Service.DeletePersonalModel(ctx, providerID, modelID)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "savePersonalModelDraft":
		var input SavePersonalModelDraftInput
		if err := decodeArg(args[0], &input); err != nil {
			return nil, err
		}
		view, err := c.Service.SavePersonalModelDraft(ctx, &input)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "setPersonalModelEnabled":
		providerID, _ := args[0].(string)
		modelID, _ := args[1].(string)
		enabled, _ := args[2].(bool)
		view, err := c.Service.SetPersonalModelEnabled(ctx, providerID, modelID, enabled)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	case "testModelConnectivity":
		var input struct {
			WorkspacePath     string `json:"workspacePath"`
			WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
			ProviderID        string `json:"providerId"`
			ModelID           string `json:"modelId"`
		}
		if err := decodeArg(args[0], &input); err != nil {
			return nil, err
		}
		return c.Service.TestModelConnectivity(ctx, &input)
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

func (c *SettingsChannel) mutateModel(ctx context.Context, args []rpc.Value, _ bool) (rpc.Value, error) {
	providerID, _ := args[0].(string)
	modelID, _ := args[1].(string)
	config, err := decodeModelConfigArg(args[2])
	if err != nil {
		return nil, err
	}
	var useRecommended *bool
	if len(args) > 3 {
		if v, ok := args[3].(bool); ok {
			useRecommended = &v
		}
	}
	view, err := c.Service.AddPersonalModel(ctx, providerID, modelID, config, useRecommended)
	if err != nil {
		return nil, err
	}
	return viewToValue(view), nil
}

// Listen: onDidChange with the settings view payload.
func (c *SettingsChannel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	if event != "onDidChange" {
		return nil, nil, &rpc.Error{Message: "Event not found: " + event}
	}
	out := make(chan rpc.Value, 8)
	stop := c.Service.OnDidChange(func(view *SettingsView) {
		select {
		case out <- viewToValue(view):
		default:
			// 慢消费者不阻塞快照发布（对齐 TS 的异步 emit 丢弃语义）.
		}
	})
	return out, stop, nil
}

// SelectionChannel adapts the model-selection service.
type SelectionChannel struct{ Service *ModelSelectionService }

var _ rpc.Channel = (*SelectionChannel)(nil)

// Call dispatches one IModelSelectionService method.
func (c *SelectionChannel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "getView":
		var input struct {
			Selection *providerconfig.ModelSelection `json:"selection"`
		}
		if len(args) > 0 && args[0] != nil {
			if err := decodeArg(args[0], &input); err != nil {
				return nil, err
			}
			view, err := c.Service.GetView(ctx, input.Selection)
			if err != nil {
				return nil, err
			}
			return viewToValue(view), nil
		}
		view, err := c.Service.GetView(ctx, nil)
		if err != nil {
			return nil, err
		}
		return viewToValue(view), nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: onDidChange with the selection view payload.
func (c *SelectionChannel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	if event != "onDidChange" {
		return nil, nil, &rpc.Error{Message: "Event not found: " + event}
	}
	out := make(chan rpc.Value, 8)
	stop := c.Service.OnDidChange(func(view *SelectionView) {
		select {
		case out <- viewToValue(view):
		default:
		}
	})
	return out, stop, nil
}

// arg decode helpers

func decodeArg(arg rpc.Value, target any) error {
	data, err := json.Marshal(arg)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return nil
}

func decodeProviderConfigArg(arg rpc.Value) (*providerconfig.ProviderConfig, error) {
	data, err := json.Marshal(arg)
	if err != nil {
		return nil, err
	}
	var config providerconfig.ProviderConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func decodeModelConfigArg(arg rpc.Value) (*providerconfig.ModelConfig, error) {
	data, err := json.Marshal(arg)
	if err != nil {
		return nil, err
	}
	var config providerconfig.ModelConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func stringSliceArg(arg rpc.Value) ([]string, error) {
	data, err := json.Marshal(arg)
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func viewToValue(v any) rpc.Value {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out rpc.Value
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

// RPCError wraps a service error into the channel error shape.
func RPCError(err error) *rpc.Error {
	if err == nil {
		return nil
	}
	var rpcErr *rpc.Error
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	return &rpc.Error{Message: err.Error()}
}

var _ = fmt.Sprintf
var _ sync.Mutex
