// repo.go ports packages/services/src/bots/repo.ts (BotsRepo) and
// storageMigration.ts (importLegacyBotConfig / importLegacyBotState).
//
// Read-modify-write spans hold the same <file>.lock the TS withFileLock takes, so
// a Node host and a Go host writing bot-state.v3.json during the strangler
// serialise instead of interleaving. Writes are the TS atomicWritePrivateTextFile:
// temp file + rename, private mode, 2-space JSON + trailing newline.
package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
)

// ErrConfigCorrupt is returned when a v3 config/state file exists but does not
// decode. The TS deliberately surfaces the zod error instead of falling back to
// a legacy file (回滚兼容：v3 已存在就只认 v3；损坏时暴露错误), so this port
// refuses to silently reset a user's bots.
var ErrConfigCorrupt = errors.New("bots: config file is corrupt")

// Repo reads and writes bot-config.v3.json / bot-state.v3.json under the app
// config dir. It is safe for concurrent use.
type Repo struct {
	appConfigDir string
	mu           sync.Mutex
}

// NewRepo builds a repo rooted at appConfigDir (the TS getAppConfigDir()).
func NewRepo(appConfigDir string) *Repo {
	return &Repo{appConfigDir: appConfigDir}
}

func (r *Repo) configPath() string {
	return filepath.Join(r.appConfigDir, ConfigFileName)
}

func (r *Repo) statePath() string {
	return filepath.Join(r.appConfigDir, StateFileName)
}

// readOptionalJSON returns (nil, nil) on ENOENT, mirroring readOptionalJson.
func readOptionalJSON(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("%w: %s is not valid JSON", ErrConfigCorrupt, filepath.Base(path))
	}
	return json.RawMessage(data), nil
}

// writeJSONAtomic ports writeJson: 2-space JSON + newline, atomic private write
// (the TS atomicWritePrivateTextFile).
func writeJSONAtomic(ctx context.Context, path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return fsx.AtomicWritePrivateTextFile(ctx, path, append(data, '\n'), nil)
}

// ReadConfig ports BotsRepo.readConfig. On first read (no v3 file) it imports a
// legacy bot-config.json when present, else writes the default — and pins the
// migrated result under the same lock so a concurrent Node host cannot re-run
// the import over a fresh state.
func (r *Repo) ReadConfig(ctx context.Context) (ConfigFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path := r.configPath()
	var result ConfigFile
	err := fsx.WithFileLock(ctx, path, func() error {
		current, err := readOptionalJSON(path)
		if err != nil {
			return err
		}
		if current != nil {
			if err := json.Unmarshal(current, &result); err != nil {
				return fmt.Errorf("%w: %v", ErrConfigCorrupt, err)
			}
			return validateConfigFile(&result)
		}
		legacyPath := filepath.Join(r.appConfigDir, LegacyConfigFileName)
		legacy, err := readOptionalJSON(legacyPath)
		if err != nil {
			return err
		}
		var migrated json.RawMessage
		if legacy == nil {
			migrated = json.RawMessage(`{"version":3,"bots":[]}`)
		} else {
			migrated = ImportLegacyConfig(legacy)
		}
		if err := json.Unmarshal(migrated, &result); err != nil {
			return fmt.Errorf("%w: %v", ErrConfigCorrupt, err)
		}
		if err := validateConfigFile(&result); err != nil {
			return err
		}
		return writeJSONAtomic(ctx, path, result)
	})
	return result, err
}

// WriteConfig ports BotsRepo.writeConfig.
func (r *Repo) WriteConfig(ctx context.Context, config ConfigFile) (ConfigFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateConfigFile(&config); err != nil {
		return config, err
	}
	var result ConfigFile
	err := fsx.WithFileLock(ctx, r.configPath(), func() error {
		if err := writeJSONAtomic(ctx, r.configPath(), config); err != nil {
			return err
		}
		result = config
		return nil
	})
	return result, err
}

// ReadState ports BotsRepo.readState: v3 first, else v2, else legacy, else the
// empty state; the migrated result is pinned inside the lock.
func (r *Repo) ReadState(ctx context.Context) (StateFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path := r.statePath()
	var result StateFile
	err := fsx.WithFileLock(ctx, path, func() error {
		current, err := readOptionalJSON(path)
		if err != nil {
			return err
		}
		if current != nil {
			if err := json.Unmarshal(current, &result); err != nil {
				return fmt.Errorf("%w: %v", ErrConfigCorrupt, err)
			}
			if result.Bots == nil {
				result.Bots = map[string]State{}
			}
			return nil
		}
		v2, err := readOptionalJSON(filepath.Join(r.appConfigDir, V2StateFileName))
		if err != nil {
			return err
		}
		legacy := v2
		if legacy == nil {
			legacy, err = readOptionalJSON(filepath.Join(r.appConfigDir, LegacyStateFileName))
			if err != nil {
				return err
			}
		}
		migrated := json.RawMessage(`{"version":3,"bots":{}}`)
		if legacy != nil {
			migrated = ImportLegacyState(legacy)
		}
		if err := json.Unmarshal(migrated, &result); err != nil {
			return fmt.Errorf("%w: %v", ErrConfigCorrupt, err)
		}
		if result.Bots == nil {
			result.Bots = map[string]State{}
		}
		return writeJSONAtomic(ctx, path, result)
	})
	return result, err
}

// WriteState ports BotsRepo.writeState.
func (r *Repo) WriteState(ctx context.Context, state StateFile) (StateFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path := r.statePath()
	if state.Bots == nil {
		state.Bots = map[string]State{}
	}
	err := fsx.WithFileLock(ctx, path, func() error {
		return writeJSONAtomic(ctx, path, state)
	})
	return state, err
}

// ReadTelegramOffset reads one bot's persisted Telegram cursor. Returns nil when
// the bot has no state yet, matching `(state.bots[botId]?.telegramOffset)`.
func (r *Repo) ReadTelegramOffset(ctx context.Context, botID string) (*float64, error) {
	state, err := r.ReadState(ctx)
	if err != nil {
		return nil, err
	}
	botState, ok := state.Bots[botID]
	if !ok {
		return nil, nil
	}
	return botState.TelegramOffset, nil
}

// WriteTelegramOffset persists one bot's Telegram cursor (update_id + 1). This
// is the external queue's consumption ack: it must only ever be called after
// the provider callback succeeded (see callback.go / telegram runtime).
func (r *Repo) WriteTelegramOffset(ctx context.Context, botID string, offset float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	path := r.statePath()
	return fsx.WithFileLock(ctx, path, func() error {
		raw, err := readOptionalJSON(path)
		if err != nil {
			return err
		}
		state := StateFile{Version: StateVersion, Bots: map[string]State{}}
		if raw != nil {
			if err := json.Unmarshal(raw, &state); err != nil {
				return fmt.Errorf("%w: %v", ErrConfigCorrupt, err)
			}
		}
		if state.Bots == nil {
			state.Bots = map[string]State{}
		}
		botState := state.Bots[botID]
		value := offset
		botState.TelegramOffset = &value
		state.Bots[botID] = botState
		return writeJSONAtomic(ctx, path, state)
	})
}

// validateConfigFile enforces the version literal and the non-empty bot id, the
// two invariants the zod schema pins and the UI depends on.
func validateConfigFile(config *ConfigFile) error {
	if config.Version != ConfigVersion {
		return fmt.Errorf("%w: expected version %d, got %d", ErrConfigCorrupt, ConfigVersion, config.Version)
	}
	for _, bot := range config.Bots {
		if strings.TrimSpace(bot.ID) == "" {
			return fmt.Errorf("%w: bot id must not be empty", ErrConfigCorrupt)
		}
		if !IsProvider(string(bot.Provider)) {
			return fmt.Errorf("%w: unknown provider %q", ErrConfigCorrupt, bot.Provider)
		}
	}
	return nil
}
