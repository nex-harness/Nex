// builtinsource.go ports NodeNexBuiltinProviderConfigSource +
// the materializer (packages/provider-node/src/nex-builtin-provider-config-source.ts,
// nex-builtin-provider-config-materializer.ts, and the server's
// bundledNexBuiltinProviderConfig.ts): Bundled (embedded) → Active/LKG cache
// selection, atomic materialization, and the applyRemoteRelease CAS.
//
// The bundled config is the SAME artifact the Node build embeds
// (scripts/builtin-provider-config.mjs reads config/provider/nex-builtin.json):
// the Go server embeds the file with go:embed so both engines ship one fact.
package modelprovider

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	"github.com/tylinux/nex/go/internal/providerconfig"
)

// BundledNexBuiltinProviderConfig is the build-time embedded release — the
// same config/provider/nex-builtin.json artifact the Node build defines as
// __NEX_BUILTIN_PROVIDER_CONFIG_JSON__. The copy lives in testdata so the
// embed works from any build cwd.
//
//go:embed testdata/nex-builtin.json
var BundledNexBuiltinProviderConfig string

// BuiltinSourceOptions ports NodeNexBuiltinProviderConfigSourceOptions.
type BuiltinSourceOptions struct {
	BundledFilePath string // empty uses the embedded artifact
	ActiveFilePath  string // empty = bundled path (no separate cache)
}

// ApplyReleaseResult is ApplyNexBuiltinReleaseResult.
type ApplyReleaseResult string

const (
	ApplyUpdated   ApplyReleaseResult = "updated"
	ApplyUnchanged ApplyReleaseResult = "unchanged"
	ApplyStale     ApplyReleaseResult = "stale"
)

// BuiltinSource ports NodeNexBuiltinProviderConfigSource.
type BuiltinSource struct {
	bundledPath string
	activePath  string
	sourceKey   string

	mu             sync.Mutex
	observedSig    string
	observedSigSet bool
	listeners      []func(reason string)
	disposed       bool
}

// NewBuiltinSource resolves the source key (sha256 of the resolved active
// path — 旧标识只有发布序号，不同 Endpoint 同序号会让 Registry 误复用上一来源).
func NewBuiltinSource(opts BuiltinSourceOptions) (*BuiltinSource, error) {
	bundled := opts.BundledFilePath
	if bundled == "" {
		bundled = "(embedded)"
	}
	active := opts.ActiveFilePath
	if active == "" {
		active = bundled
	}
	sum := sha256.Sum256([]byte(filepath.ToSlash(filepath.Clean(active))))
	return &BuiltinSource{
		bundledPath: bundled,
		activePath:  active,
		sourceKey:   hex.EncodeToString(sum[:]),
	}, nil
}

// ActiveFilePath exposes the materialized path.
func (s *BuiltinSource) ActiveFilePath() string { return s.activePath }

// Read returns the layer snapshot: Active (LKG) wins when newer; materialize
// repairs a stale Active; any Active failure falls back to Bundled (Active
// 只是可丢弃缓存).
func (s *BuiltinSource) Read(ctx context.Context) (*BuiltinConfigSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disposed {
		return nil, errors.New("NodeNexBuiltinProviderConfigSource 已 dispose")
	}
	release, err := s.readAndMaterializeLocked(ctx)
	if err != nil {
		var bundledErr error
		release, bundledErr = s.readReleaseCandidate(ctx, s.bundledPath)
		if bundledErr != nil {
			return nil, fmt.Errorf("Bundled 与 Active Nex Built-in Release 均不可用: %w / %v", err, bundledErr)
		}
		release, _ = providerconfig.SelectReleaseCandidate(release, nil, bundledErr, err)
	}
	if !s.observedSigSet {
		s.observedSig, s.observedSigSet = providerconfig.ReleaseSignature(release), true
	}
	return snapshotFromRelease(release, s.sourceKey), nil
}

// ApplyRemoteRelease ports applyRemoteRelease: CAS by revision, then content
// equality on equal revisions; an updated release emits "remote-updated".
func (s *BuiltinSource) ApplyRemoteRelease(ctx context.Context, release *providerconfig.NexBuiltinRelease) (ApplyReleaseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disposed {
		return "", errors.New("NodeNexBuiltinProviderConfigSource 已 dispose")
	}
	result := ApplyStale
	err := fsx.WithFileLock(ctx, s.activePath, func() error {
		if s.disposed {
			return errors.New("NodeNexBuiltinProviderConfigSource 已 dispose")
		}
		current, err := s.readAndMaterializeLocked(ctx)
		if err != nil {
			return err
		}
		if release.Revision < current.Revision {
			result = ApplyStale
			return nil
		}
		if release.Revision == current.Revision {
			if providerconfig.SerializeNexBuiltinRelease(release) == providerconfig.SerializeNexBuiltinRelease(current) {
				result = ApplyUnchanged
				return nil
			}
			return fmt.Errorf("Nex Built-in 相同 revision %d 对应不同内容", release.Revision)
		}
		if err := s.writeActiveLocked(ctx, release); err != nil {
			return err
		}
		s.observedSig = providerconfig.ReleaseSignature(release)
		s.observedSigSet = true
		result = ApplyUpdated
		return nil
	})
	if err != nil {
		return "", err
	}
	if result == ApplyUpdated && !s.disposed {
		for _, listener := range s.listeners {
			listener("remote-updated")
		}
	}
	return result, nil
}

// OnDidChange registers a listener.
func (s *BuiltinSource) OnDidChange(listener func(reason string)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, listener)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, candidate := range s.listeners {
			if &candidate == &listener {
				s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
				return
			}
		}
	}
}

// Dispose stops the source.
func (s *BuiltinSource) Dispose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disposed = true
	s.listeners = nil
}

// readAndMaterializeLocked ports #readAndMaterializeLocked.
func (s *BuiltinSource) readAndMaterializeLocked(ctx context.Context) (*providerconfig.NexBuiltinRelease, error) {
	bundled, bundledErr := s.readReleaseCandidate(ctx, s.bundledPath)
	var active *providerconfig.NexBuiltinRelease
	var activeErr error
	if s.activePath != s.bundledPath {
		active, activeErr = s.readReleaseCandidate(ctx, s.activePath)
	}
	selected, err := providerconfig.SelectReleaseCandidate(bundled, active, bundledErr, activeErr)
	if err != nil {
		return nil, err
	}
	if s.activePath != s.bundledPath {
		activeSig := ""
		if active != nil {
			activeSig = providerconfig.ReleaseSignature(active)
		}
		if activeSig != providerconfig.ReleaseSignature(selected) {
			if err := s.writeActiveLocked(ctx, selected); err != nil {
				return nil, err
			}
		}
	}
	return selected, nil
}

// writeActiveLocked ports #writeActiveLocked: atomic private write of the
// 2-space-indented encoding.
func (s *BuiltinSource) writeActiveLocked(ctx context.Context, release *providerconfig.NexBuiltinRelease) error {
	if s.disposed {
		return errors.New("NodeNexBuiltinProviderConfigSource 已 dispose")
	}
	encoded, err := json.MarshalIndent(providerconfig.EncodeNexBuiltinRelease(release), "", "  ")
	if err != nil {
		return err
	}
	return fsx.AtomicWritePrivateTextFile(ctx, s.activePath, encoded, nil)
}

// readReleaseCandidate decodes one file; a read error is returned, ENOENT is
// a nil candidate.
func (s *BuiltinSource) readReleaseCandidate(ctx context.Context, path string) (*providerconfig.NexBuiltinRelease, error) {
	var data []byte
	var err error
	if path == "(embedded)" {
		data = []byte(BundledNexBuiltinProviderConfig)
	} else {
		data, err = os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
	}
	release, err := providerconfig.DecodeNexBuiltinRelease(data)
	if err != nil {
		return nil, err
	}
	return release, nil
}

// BuiltinConfigSnapshot is the layer snapshot a builtin source produces
// (ProviderConfigLayerSnapshot with template map).
type BuiltinConfigSnapshot struct {
	Revision          string
	Providers         *providerconfig.ProviderConfigMap
	ProviderTemplates *providerconfig.ProviderTemplateMap
	Models            *providerconfig.ModelConfigRules
}

func snapshotFromRelease(release *providerconfig.NexBuiltinRelease, sourceKey string) *BuiltinConfigSnapshot {
	return &BuiltinConfigSnapshot{
		Revision:          fmt.Sprintf("nex-builtin:%d:%s", release.Revision, sourceKey),
		Providers:         release.Config.Providers,
		ProviderTemplates: release.Config.ProviderTemplates,
		Models:            release.Config.ModelConfigRules,
	}
}

// MaterializeBuiltinProviderConfig ports materializeNexBuiltinProviderConfig
// + readBundledNexBuiltinProviderConfig: take the embedded (or given) content,
// canonicalize it and atomically write the environment's fixed copy:
// <root>/runtime/provider/bundled/nex-builtin.json. 升级以退出旧进程为前提，
// 不保留历史 hash 副本.
func MaterializeBuiltinProviderConfig(ctx context.Context, environmentConfigRoot string, content string) (string, error) {
	release, err := providerconfig.DecodeNexBuiltinRelease([]byte(content))
	if err != nil {
		return "", err
	}
	serialized := providerconfig.SerializeNexBuiltinRelease(release) + "\n"
	filePath := filepath.Join(environmentConfigRoot, "runtime", "provider", "bundled", "nex-builtin.json")
	err = fsx.WithFileLock(ctx, filePath, func() error {
		existing, readErr := os.ReadFile(filePath)
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return readErr
		}
		if string(existing) == serialized {
			return nil
		}
		return fsx.AtomicWritePrivateTextFile(ctx, filePath, []byte(serialized), nil)
	})
	if err != nil {
		return "", err
	}
	return filePath, nil
}
