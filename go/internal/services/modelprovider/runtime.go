// runtime.go ports ProviderRuntime + the service fronts
// (providerRuntime.ts + providerFacadeServices.ts): one process-wide
// ProviderRuntime (config sources → config service → registry service →
// facades), plus the per-service ensureReady + view/mutation forwarding with
// the mutation-tail serialization the TS facades implement.
package modelprovider

import (
	"context"
	"sync"
)

// RuntimeOptions ports ProviderRuntimeOptions + ProviderConfigRuntimeOptions.
type RuntimeOptions struct {
	BuiltinOptions   BuiltinSourceOptions
	PersonalOptions  PersonalRepoOptions
	AccountSource    AccountSourceAPI
	TestConnectivity ConnectivityTester
	ModelsDev        *ModelsDev
}

// ConnectivityTester ports ProviderSettingsConnectivityTester: the executor
// handed in by the environment (uses the formal model chain).
type ConnectivityTester func(ctx context.Context, input ConnectivityTestInput) error

// ConnectivityTestInput ports FormalModelConnectivityInput.
type ConnectivityTestInput struct {
	WorkspacePath     string
	WorkspaceIdentity string
	ProviderID        string
	ModelID           string
}

// ProviderRuntime ports ProviderRuntime.
type ProviderRuntime struct {
	BuiltinSource   *BuiltinSource
	PersonalRepo    *PersonalRepo
	ConfigService   *ConfigService
	RegistryService *RegistryService

	ProviderSettings *ProviderSettingsService
	ModelSelection   *ModelSelectionService

	startOnce sync.Once
	startErr  error
	disposed  bool
}

// NewProviderRuntime assembles the stack.
func NewProviderRuntime(opts RuntimeOptions) (*ProviderRuntime, error) {
	builtin, err := NewBuiltinSource(opts.BuiltinOptions)
	if err != nil {
		return nil, err
	}
	personal, err := NewPersonalRepo(opts.PersonalOptions)
	if err != nil {
		return nil, err
	}
	configService := NewConfigService(ConfigServiceOptions{
		BuiltinSource: builtin, PersonalRepository: personal,
	})
	accountSource := opts.AccountSource
	if accountSource == nil {
		// 普通 API Provider 可以在账号能力尚未装配时独立运行；Account Provider 由
		// access.entitled=false Overlay 显式 fail-closed.
		accountSource = &failClosedAccountSource{config: configService}
	}
	registryService := NewRegistryService(RegistryServiceOptions{
		ConfigSource: configService, AccountSource: accountSource,
	})
	runtime := &ProviderRuntime{
		BuiltinSource: builtin, PersonalRepo: personal, ConfigService: configService,
		RegistryService: registryService,
	}
	runtime.ProviderSettings = &ProviderSettingsService{
		runtime:          runtime,
		modelsDev:        opts.ModelsDev,
		testConnectivity: opts.TestConnectivity,
	}
	runtime.ModelSelection = &ModelSelectionService{runtime: runtime}
	return runtime, nil
}

// Start ports ProviderRuntime.start.
func (r *ProviderRuntime) Start(ctx context.Context) error {
	r.startOnce.Do(func() {
		if _, err := r.ConfigService.Read(ctx); err != nil {
			r.startErr = err
			return
		}
		r.startErr = r.RegistryService.Start(ctx)
	})
	return r.startErr
}

// Dispose tears the stack down.
func (r *ProviderRuntime) Dispose() {
	if r.disposed {
		return
	}
	r.disposed = true
	r.RegistryService.Dispose()
	r.ConfigService.Dispose()
	r.PersonalRepo.Dispose()
	if r.BuiltinSource != nil {
		r.BuiltinSource.Dispose()
	}
}

// failClosedAccountSource ports EmptyAccountProviderConfigSource.
type failClosedAccountSource struct {
	config *ConfigService
}

func (s *failClosedAccountSource) Read(ctx context.Context) (*AccountSnapshot, error) {
	config, err := s.config.Read(ctx)
	if err != nil {
		return nil, err
	}
	return FailClosedAccountSnapshot(config), nil
}

func (s *failClosedAccountSource) OnDidChange(func(string)) func() { return func() {} }

// NewFailClosedAccountSource is the exported assembly helper for callers
// outside this package (the provisioning target tests) that build a runtime
// by hand: 普通 API Provider 可以在账号能力尚未装配时独立运行.
func NewFailClosedAccountSource(config *ConfigService) AccountSourceAPI {
	return &failClosedAccountSource{config: config}
}
