// Package providers is the step-3.6 security-sensitive half: the
// IProviderProvisioningTargetService port (providerProvisioningTarget.ts)
// and the connection-mode gate that wraps it (packages/server/src/http.ts).
//
// Provisioning 携带跨 Environment 凭据，只允许 Desktop trusted host 使用；普通
// Web remote/replayable 客户端即使知道频道名，也不能获得 target 写入接口.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	"github.com/tylinux/nex/go/internal/providerconfig"
	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/services/modelprovider"
)

// Channel name (ServiceChannels.ProviderProvisioningTarget).
const ChannelProviderProvisioningTarget = "provider-provisioning-target"

// ProvisioningOnlyTrustedHostError is the exact rejection error a
// web-remote-replayable connection gets when it calls apply.
const ProvisioningOnlyTrustedHostError = "Provider Provisioning 仅支持受信 Desktop Host"

// Envelope ports ProviderProvisioningEnvelope (shared/provider-provisioning.ts).
type Envelope struct {
	SchemaVersion   int               `json:"schemaVersion"`
	SyncID          string            `json:"syncId"`
	PersonalConfig  json.RawMessage   `json:"personalConfig"`
	AccountSettings json.RawMessage   `json:"accountSettings"`
	Credentials     []CredentialEntry `json:"credentials"`
}

// CredentialEntry ports providerProvisioningCredentialEntrySchema.
type CredentialEntry struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Result ports ProviderProvisioningResult.
type Result struct {
	SyncID                string `json:"syncId"`
	Status                string `json:"status"`
	PersonalProviderCount int    `json:"personalProviderCount"`
	CredentialCount       int    `json:"credentialCount"`
	ConfigRevision        string `json:"configRevision,omitempty"`
	ErrorMessage          string `json:"errorMessage,omitempty"`
	RolledBack            bool   `json:"rolledBack"`
}

// TargetOptions ports ProviderProvisioningTargetOptions.
type TargetOptions struct {
	Runtime            *modelprovider.ProviderRuntime
	PersonalConfigPath string
	StateFilePath      string
}

const provisioningSchemaVersion = 1

type provisioningStateRecord struct {
	SyncID string `json:"syncId"`
	Result Result `json:"result"`
}

type provisioningStateFile struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Records       []provisioningStateRecord `json:"records"`
}

// Target ports createProviderProvisioningTarget.
type Target struct {
	opts TargetOptions
}

// NewTarget builds the provisioning target.
func NewTarget(opts TargetOptions) *Target { return &Target{opts: opts} }

// Apply ports apply: envelope validation, idempotency via the state file,
// CAS replace of the personal layer, registry refresh + default-selection
// validation, rollback on failure (回滚前做 CAS 式校验；若期间已有其它写入，
// 宁可报告 rollback_failed，也不能用过期快照覆盖用户的新配置).
func (t *Target) Apply(ctx context.Context, envelope Envelope) (*Result, error) {
	if envelope.SchemaVersion != 1 || envelope.SyncID == "" || len(envelope.Credentials) > 256 {
		return nil, errors.New("invalid provisioning envelope")
	}
	personalUpdate, err := providerconfig.DecodeProviderConfigFile(
		[]byte(`{"schemaVersion":1,"config":` + string(envelope.PersonalConfig) + `}`),
	)
	if err != nil {
		return nil, err
	}
	var result *Result
	stateErr := fsx.WithFileLock(ctx, t.opts.StateFilePath, func() error {
		previousState, err := readStateFile(t.opts.StateFilePath)
		if err != nil {
			return err
		}
		if previousState != nil {
			for _, record := range previousState.Records {
				if record.SyncID == envelope.SyncID {
					already := record.Result
					already.Status = "already-applied"
					result = &already
					return nil
				}
			}
		}
		if err := t.opts.Runtime.Start(ctx); err != nil {
			return err
		}
		before, err := modelprovider.ReadProvisionablePersonalConfig(ctx, t.opts.Runtime, t.opts.PersonalConfigPath)
		if err != nil {
			return err
		}
		personalConfigApplied := false
		_, applyErr := t.opts.Runtime.PersonalRepo.Update(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
			// 其他域写入期间 Personal 可能已被编辑；检查与替换必须在同一个文件锁内.
			if !providerconfig.SameLayerUpdate(current.AsUpdate(), before.AsUpdate()) {
				return nil, errors.New("Personal Provider Config 在同步期间被其它操作修改")
			}
			// 先登记再写入：底层原子写即使在替换完成后才抛错，也必须进入回滚集合.
			personalConfigApplied = true
			return personalUpdate, nil
		})
		if applyErr == nil {
			snapshot, refreshErr := t.opts.Runtime.RegistryService.RequestRefresh(ctx, "provider-provisioning")
			if refreshErr != nil {
				applyErr = refreshErr
			} else if personalUpdate.DefaultModelSelection != nil {
				if validation := t.opts.Runtime.RegistryService.ValidateSelection(*personalUpdate.DefaultModelSelection); !validation.OK {
					applyErr = errors.New("同步后的远端 Registry 不支持本地默认模型")
				}
			} else {
				_ = snapshot
			}
		}
		if applyErr == nil {
			applied := &Result{
				SyncID: envelope.SyncID, Status: "applied",
				PersonalProviderCount: personalUpdate.Providers.Len(),
				CredentialCount:       0,
				ConfigRevision:        snapshotRevision(t.opts.Runtime),
				RolledBack:            false,
			}
			if err := writeStateFile(ctx, t.opts.StateFilePath, appendStateRecord(previousState, applied)); err != nil {
				return err
			}
			result = applied
			return nil
		}
		// Rollback path.
		var rollbackErr error
		if personalConfigApplied {
			_, rollbackErr = t.opts.Runtime.PersonalRepo.Update(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
				if providerconfig.SameLayerUpdate(current.AsUpdate(), before.AsUpdate()) {
					return current.AsUpdate(), nil
				}
				if !providerconfig.SameLayerUpdate(current.AsUpdate(), personalUpdate) {
					return nil, errors.New("Personal Provider Config 在同步期间被其它操作修改，跳过回滚")
				}
				// 默认选择与 Provider/Model 共用一份 CAS；不能分别回滚制造混合状态.
				return before.AsUpdate(), nil
			})
			if rollbackErr == nil {
				if _, err := t.opts.Runtime.RegistryService.RequestRefresh(ctx, "provider-provisioning-rollback"); err != nil {
					rollbackErr = err
				}
			}
		}
		if rollbackErr != nil {
			result = &Result{
				SyncID: envelope.SyncID, Status: "rollback_failed",
				PersonalProviderCount: before.Providers.Len(), CredentialCount: 0,
				ErrorMessage: fmt.Sprintf("%s；回滚失败：%s", applyErr.Error(), rollbackErr.Error()),
				RolledBack:   false,
			}
			return nil
		}
		result = &Result{
			SyncID: envelope.SyncID, Status: "failed",
			PersonalProviderCount: before.Providers.Len(), CredentialCount: 0,
			ErrorMessage: applyErr.Error(), RolledBack: true,
		}
		return nil
	})
	if stateErr != nil {
		return nil, stateErr
	}
	return result, nil
}

func snapshotRevision(runtime *modelprovider.ProviderRuntime) string {
	if snapshot := runtime.RegistryService.GetSnapshot(); snapshot != nil {
		return snapshot.SourceRevisions.Config
	}
	return ""
}

func readStateFile(path string) (*provisioningStateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		// 读取失败与损坏都按无状态处理（the TS returns null on every error).
		return nil, nil
	}
	var parsed provisioningStateFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, nil
	}
	if parsed.SchemaVersion != provisioningSchemaVersion {
		return nil, nil
	}
	var records []provisioningStateRecord
	for _, record := range parsed.Records {
		if record.SyncID == "" {
			continue
		}
		if record.Result.SyncID == "" || record.Result.Status == "" {
			continue
		}
		records = append(records, record)
	}
	return &provisioningStateFile{SchemaVersion: provisioningSchemaVersion, Records: records}, nil
}

func appendStateRecord(previous *provisioningStateFile, result *Result) *provisioningStateFile {
	var records []provisioningStateRecord
	if previous != nil {
		for _, record := range previous.Records {
			if record.SyncID != result.SyncID {
				records = append(records, record)
			}
		}
	}
	records = append(records, provisioningStateRecord{SyncID: result.SyncID, Result: *result})
	return &provisioningStateFile{SchemaVersion: provisioningSchemaVersion, Records: records}
}

func writeStateFile(ctx context.Context, path string, state *provisioningStateFile) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fsx.AtomicWritePrivateTextFile(ctx, path, data, nil)
}

// Channel adapts the target to the connection-scoped rpc.Channel.
type Channel struct {
	Target *Target
}

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches apply (the channel's only method).
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if method != "apply" || len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	data, err := json.Marshal(args[0])
	if err != nil {
		return nil, err
	}
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	result, err := c.Target.Apply(ctx, envelope)
	if err != nil {
		return nil, &rpc.Error{Message: err.Error()}
	}
	return valueOf(result), nil
}

// Listen: no events.
func (c *Channel) Listen(_ context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// RejectedChannel is the per-connection override a web-remote-replayable
// connection gets: every apply throws the exact trusted-host error.
type RejectedChannel struct{}

var _ rpc.Channel = (*RejectedChannel)(nil)

// Call always rejects with ProvisioningOnlyTrustedHostError.
func (c *RejectedChannel) Call(_ context.Context, method string, _ []rpc.Value) (rpc.Value, error) {
	return nil, &rpc.Error{Message: ProvisioningOnlyTrustedHostError}
}

// Listen: no events.
func (c *RejectedChannel) Listen(_ context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// ConnectionScope decides, per connection mode, whether the target is
// exposed. It is the Go shape of the http.ts override block.
type ConnectionScope struct {
	mode              string // "desktop-continuous" | "web-remote-replayable"
	target            *Target
	trustedConnection bool
}

// NewConnectionScope: mode is the endpoint mode string the server handed the
// connection. Anything that is not desktop-continuous is untrusted for
// provisioning (the trusted-host relay is the only legitimate apply caller).
func NewConnectionScope(mode string, target *Target) *ConnectionScope {
	return &ConnectionScope{mode: mode, target: target, trustedConnection: mode == "desktop-continuous"}
}

// Mode returns the scope's connection mode.
func (s *ConnectionScope) Mode() string { return s.mode }

// ScopeFor returns the per-connection channel: the real target for a trusted
// desktop-continuous host, the rejecting stub otherwise, nil when no target
// was configured at all (nothing to expose).
func (s *ConnectionScope) ScopeFor(channelName string) rpc.Channel {
	if channelName != ChannelProviderProvisioningTarget {
		return nil
	}
	if s.target == nil {
		return nil
	}
	if s.trustedConnection {
		return &Channel{Target: s.target}
	}
	return &RejectedChannel{}
}

// Dispose: the scope holds no per-connection state beyond the stub choice.
func (s *ConnectionScope) Dispose() {}

func valueOf(v any) rpc.Value {
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
