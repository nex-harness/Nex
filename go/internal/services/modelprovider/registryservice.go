// registryservice.go ports ProviderRegistryService
// (packages/provider/src/registry-service.ts): the refresh loop that combines
// the config snapshot + account snapshot through the resolver into the
// registry, coalescing concurrent refresh requests by generation.
package modelprovider

import (
	"context"
	"errors"
	"sync"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// AccountSnapshot ports AccountProviderConfigSnapshot.
type AccountSnapshot struct {
	Revision                  string
	BasedOnNexBuiltinRevision string
	Providers                 *providerconfig.ProviderConfigMap
	States                    map[string]providerconfig.AccountProviderState
}

// FailClosedAccountSnapshot ports createFailClosedAccountProviderConfigSnapshot:
// 首次 Account 事实尚未到达时，基于当前 Built-in 生成可发布的 fail-closed Overlay.
func FailClosedAccountSnapshot(config *ConfigSnapshot) *AccountSnapshot {
	unentitled := providerconfig.EmptyProviderConfigMap()
	for _, rule := range config.NexBuiltinProviders.Rules() {
		if rule.Config == nil || rule.Config.Access == nil ||
			rule.Config.Access.AccessType() != providerconfig.AccessZhipuAccount {
			continue
		}
		unentitled = unentitled.SetRule(providerconfig.ProviderConfigRule{
			ProviderID: rule.ProviderID,
			Config: &providerconfig.ProviderConfig{Access: &providerconfig.ProviderAccess{
				ZhipuAccount: &providerconfig.ZhipuAccountAccessConfig{Entitled: boolPtr(false)},
			}},
		})
	}
	return &AccountSnapshot{
		Revision:                  accountRevision(config.NexBuiltinRevision, unentitled, nil),
		BasedOnNexBuiltinRevision: config.NexBuiltinRevision,
		Providers:                 unentitled,
	}
}

func accountRevision(basedOn string, providers *providerconfig.ProviderConfigMap, states map[string]providerconfig.AccountProviderState) string {
	return "account:" + jsonMarshalOrEmpty([]any{basedOn, providers.ToJSON(), states})
}

func jsonMarshalOrEmpty(v any) string {
	data, err := jsonMarshalString(v)
	if err != nil {
		return ""
	}
	return data
}

// RegistryServiceSnapshot ports ProviderRegistryServiceSnapshot.
type RegistryServiceSnapshot struct {
	SourceRevisions struct {
		Config  string
		Account string
	}
	Config     *ConfigSnapshot
	Account    *AccountSnapshot
	Resolution *providerconfig.Resolution
	Registry   *RegistryView
}

// RegistryView is the published registry view (revision + providers).
type RegistryView struct {
	Revision  int
	Providers []providerconfig.RegistryProvider
}

// ChangedEvent ports ProviderRegistryServiceChangedEvent.
type ChangedEvent struct {
	Snapshot *RegistryServiceSnapshot
	Reasons  []string
}

// AccountSourceAPI is the account snapshot source (refreshable).
type AccountSourceAPI interface {
	Read(ctx context.Context) (*AccountSnapshot, error)
	OnDidChange(listener func(reason string)) func()
}

// RegistryServiceOptions wires the sources.
type RegistryServiceOptions struct {
	ConfigSource  *ConfigService
	AccountSource AccountSourceAPI
}

// RegistryService ports ProviderRegistryService.
type RegistryService struct {
	config  *ConfigService
	account AccountSourceAPI

	mu              sync.Mutex
	snapshot        *RegistryServiceSnapshot
	requestedGen    int
	completedGen    int
	refreshWait     []registryRefreshWaiter
	pendingReas     []string
	changeListeners []func(ChangedEvent)
	errorListeners  []func(err error, reasons []string)
	started         bool
	disposed        bool
	registry        *providerconfig.Registry
	stopConfig      func()
	stopAccount     func()
}

// NewRegistryService wires the source fan-in.
func NewRegistryService(opts RegistryServiceOptions) *RegistryService {
	svc := &RegistryService{
		config: opts.ConfigSource, account: opts.AccountSource,
		registry: providerconfig.NewRegistry(),
	}
	svc.stopConfig = opts.ConfigSource.OnDidChange(func(reason string) {
		svc.refreshFromSource("config", reason)
	})
	if opts.AccountSource != nil {
		svc.stopAccount = opts.AccountSource.OnDidChange(func(reason string) {
			svc.refreshFromSource("account", reason)
		})
	}
	return svc
}

// Start ports start(): subscribe + first refresh. 启动只负责就绪；业务读取必须
// 使用 getSnapshot.
func (s *RegistryService) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return errors.New("ProviderRegistryService 已 dispose")
	}
	alreadyStarted := s.started
	s.started = true
	s.mu.Unlock()
	if !alreadyStarted {
		_, err := s.RequestRefresh(ctx, "start")
		return err
	}
	s.mu.Lock()
	hasSnapshot := s.snapshot != nil
	s.mu.Unlock()
	if !hasSnapshot {
		_, err := s.RequestRefresh(ctx, "start")
		return err
	}
	return nil
}

// RequestRefresh ports refresh()/requestRefresh.
func (s *RegistryService) RequestRefresh(ctx context.Context, reason string) (*RegistryServiceSnapshot, error) {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return nil, errors.New("ProviderRegistryService 已 dispose")
	}
	if !s.started {
		s.mu.Unlock()
		return nil, errors.New("ProviderRegistryService 必须先 start() 再 refresh()")
	}
	s.requestedGen++
	generation := s.requestedGen
	s.pendingReas = append(s.pendingReas, orDefault(reason, "explicit"))
	result := make(chan struct {
		snapshot *RegistryServiceSnapshot
		err      error
	}, 1)
	s.refreshWait = append(s.refreshWait, registryRefreshWaiter{
		generation: generation,
		resolve: func(snapshot *RegistryServiceSnapshot) {
			result <- struct {
				snapshot *RegistryServiceSnapshot
				err      error
			}{snapshot, nil}
		},
		reject: func(err error) {
			result <- struct {
				snapshot *RegistryServiceSnapshot
				err      error
			}{nil, err}
		},
	})
	s.mu.Unlock()
	go s.runRefreshLoop(ctx)
	select {
	case outcome := <-result:
		return outcome.snapshot, outcome.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// GetSnapshot returns the current snapshot or nil.
func (s *RegistryService) GetSnapshot() *RegistryServiceSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

// RegistryView returns the published view.
func (s *RegistryService) View() *RegistryView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &RegistryView{Revision: s.registry.Revision(), Providers: s.registry.Providers()}
}

// ValidateSelection validates against the current registry.
func (s *RegistryService) ValidateSelection(selection providerconfig.ModelSelection) providerconfig.SelectionValidation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registry.ValidateSelection(selection)
}

// OnDidChange registers a change listener.
func (s *RegistryService) OnDidChange(listener func(ChangedEvent)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changeListeners = append(s.changeListeners, listener)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, candidate := range s.changeListeners {
			if &candidate == &listener {
				s.changeListeners = append(s.changeListeners[:i], s.changeListeners[i+1:]...)
				return
			}
		}
	}
}

// Dispose tears everything down.
func (s *RegistryService) Dispose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disposed {
		return
	}
	s.disposed = true
	if s.stopConfig != nil {
		s.stopConfig()
	}
	if s.stopAccount != nil {
		s.stopAccount()
	}
	for _, waiter := range s.refreshWait {
		waiter.reject(errors.New("ProviderRegistryService 已 dispose"))
	}
	s.refreshWait = nil
	s.changeListeners = nil
	s.errorListeners = nil
}

func (s *RegistryService) refreshFromSource(source, reason string) {
	s.mu.Lock()
	disposed := s.disposed
	s.mu.Unlock()
	if disposed {
		return
	}
	// Source 驱动的后台刷新必须异步：它可能在 PersonalRepo 写入的 emit 回调里
	// 被触发，同步 RequestRefresh 会等一个又要读同一个 repo 的刷新循环（死锁）。
	// TS 的 void this.#requestRefresh(...).catch(...) 就是 fire-and-forget.
	go func() {
		if _, err := s.RequestRefresh(context.Background(), source+":"+orDefault(reason, "changed")); err != nil {
			s.mu.Lock()
			listeners := append([]func(error, []string){}, s.errorListeners...)
			s.mu.Unlock()
			for _, listener := range listeners {
				listener(err, nil)
			}
		}
	}()
}

// runRefreshLoop ports runRefreshLoop: read both sources, guard on account
// staleness, resolve, publish. Generations coalesce; a newer request
// restarts the loop.
func (s *RegistryService) runRefreshLoop(ctx context.Context) {
	for {
		s.mu.Lock()
		if s.disposed || s.completedGen >= s.requestedGen {
			s.mu.Unlock()
			return
		}
		generation := s.requestedGen
		reasons := s.pendingReas
		s.pendingReas = nil
		s.mu.Unlock()

		config, err := s.config.Read(ctx)
		if err == nil {
			var account *AccountSnapshot
			account, err = s.readAccount(ctx)
			if err == nil {
				s.mu.Lock()
				// Built-in 已变化但 Account 仍基于旧事实时，继续服务上一份完整 Registry.
				if account.BasedOnNexBuiltinRevision != config.NexBuiltinRevision {
					s.completedGen = generation
					if s.snapshot != nil {
						s.resolveWaiters(generation, s.snapshot)
					}
					s.mu.Unlock()
					continue
				}
				if s.snapshot != nil &&
					s.snapshot.SourceRevisions.Config == config.Revision &&
					s.snapshot.SourceRevisions.Account == account.Revision {
					s.completedGen = generation
					s.resolveWaiters(generation, s.snapshot)
					s.mu.Unlock()
					continue
				}
				s.mu.Unlock()

				resolution := providerconfig.Resolve(providerconfig.ResolutionInput{
					NexBuiltinProviders:         config.NexBuiltinProviders,
					NexBuiltinProviderTemplates: config.NexBuiltinProviderTemplates,
					PersonalProviders:           config.PersonalProviders,
					NexBuiltinModelRules:        config.NexBuiltinModelRules,
					PersonalModels:              config.PersonalModels,
					AccountProviders:            account.Providers,
					AccountStates:               account.States,
					PersonalProviderOrder:       config.PersonalProviderOrder,
				})
				if err := s.registry.Replace(resolution.RegistryProviders, joinStrings(reasons, ",")); err != nil {
					s.completeWithError(generation, err, reasons)
					return
				}
				snapshot := &RegistryServiceSnapshot{}
				snapshot.SourceRevisions.Config = config.Revision
				snapshot.SourceRevisions.Account = account.Revision
				snapshot.Config = config
				snapshot.Account = account
				snapshot.Resolution = resolution
				snapshot.Registry = &RegistryView{Revision: s.registry.Revision(), Providers: s.registry.Providers()}

				s.mu.Lock()
				if generation < s.requestedGen {
					s.mu.Unlock()
					continue
				}
				s.snapshot = snapshot
				s.completedGen = generation
				s.resolveWaiters(generation, snapshot)
				listeners := append([]func(ChangedEvent){}, s.changeListeners...)
				s.mu.Unlock()
				event := ChangedEvent{Snapshot: snapshot, Reasons: reasons}
				for _, listener := range listeners {
					listener(event)
				}
				continue
			}
		}
		s.completeWithError(generation, err, reasons)
		return
	}
}
