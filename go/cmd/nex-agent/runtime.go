package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	"github.com/tylinux/nex/go/internal/adapters/model/openai"
	"github.com/tylinux/nex/go/internal/adapters/model/openairesponses"
	"github.com/tylinux/nex/go/internal/adapters/store"
	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/runtime"
	"github.com/tylinux/nex/go/internal/tools"
)

// clock is the production runtime.Clock.
type clock struct{}

func (clock) NowMs() int64 { return time.Now().UnixMilli() }
func (clock) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// modelClock adapts the process clock to modelclient.Clock (retry backoff).
type modelClock struct{}

func (modelClock) Now() time.Time { return time.Now() }
func (modelClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (modelClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// ids mints the identifiers the Node agent generates. The shapes (prefixes)
// appear in frames; the values are opaque.
type ids struct {
	logEpoch string
}

func newIDs() *ids { return &ids{logEpoch: sortableEpoch()} }

func (i *ids) SessionID() string { return "sess_" + newUUID() }
func (i *ids) TurnID() string    { return "turn_" + newUUID() }
func (i *ids) MessageID() string { return "msg_" + sortableSegment() + "_" + newUUID() }
func (i *ids) PartID() string    { return "part_" + sortableSegment() + "_" + newUUID() }
func (i *ids) EventID() string   { return newUUID() }
func (i *ids) UUID() string      { return newUUID() }
func (i *ids) LogEpoch() string  { return i.logEpoch }

func sortableSegment() string { return formatBase36(time.Now().UnixMilli()) }

func sortableEpoch() string {
	now := formatBase36(time.Now().UnixMilli())
	return now + "-" + formatBase36(rand.Int63n(1<<40))
}

func formatBase36(n int64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	var buf [13]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%36]
		n /= 36
	}
	return string(buf[i:])
}

// outbox serialises frame writes.
type outbox struct{ w *protocol.FrameWriter }

func (o outbox) WriteMessage(m protocol.Message) error { return o.w.WriteMessage(m) }

// fsAdapter exposes the fsx port as the runtime FS.
type fsAdapter struct{ port *fsx.Port }

func (f fsAdapter) ToolFS() tools.FS { return f.port }

// env holds the process platform facts the prompt environment carries.
type env struct {
	cwd       string
	shell     string
	platform  string
	arch      string
	osVersion string
}

// probeEnv reads the same facts the Node context adapter does:
// platform = process.platform, arch = the Node arch name, osVersion =
// "<platform> <release> <arch>", shell = basename($SHELL).
func probeEnv(cwd string) env {
	return env{
		cwd:       cwd,
		shell:     shellName(),
		platform:  nodePlatform(),
		arch:      nodeArch(),
		osVersion: nodePlatform() + " " + osRelease() + " " + nodeArch(),
	}
}

// buildRuntime wires every adapter and returns the runtime.
func buildRuntime(ctx context.Context, w *protocol.FrameWriter, db *sql.DB) (*runtime.Runtime, error) {
	// The provider config is only needed for the model path. A host that never
	// sends a session (e.g. the S01 handshake) must still start, so a missing or
	// unparsable config degrades to a zero provider rather than failing startup.
	provider, err := openProviderConfig()
	if err != nil {
		provider = runtime.Provider{}
	}
	sessionStore := store.NewSessionStore(db, store.SessionStoreOptions{})
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	home, _ := os.UserHomeDir()
	envFacts := probeEnv(cwd)
	// Pick the model transport and decoder by the provider's API dialect.
	// chat-completions is the historical default; openai-responses is what the
	// in-house proxy (ai-proxy.tylinux.cn) speaks. anthropic is implemented but
	// not wired here yet because its Transport wants the request body bound at
	// construction time (sdk.MessageNewParams), not per call — reconciling that
	// with the per-turn modelreq shape is its own task.
	var transport modelclient.Transport
	var newDecoder modelclient.DecoderFactory
	headers := openai.HeaderInput{
		Authorization:  "Bearer " + provider.APIKey,
		UserAgent:      "Nex/1.0.0-go",
		RuntimeVersion: "24",
		Platform:       envFacts.platform + "-" + envFacts.arch,
		OSCategory:     envFacts.platform,
		OSVersion:      envFacts.osVersion,
		NexAgent:       "glm",
		ReleaseChannel: "production",
		Title:          "Z Code@electron",
		HTTPReferer:    provider.BaseURL,
	}
	switch provider.ProviderKind {
	case "openai-responses":
		transport = openairesponses.New(openairesponses.Config{
			BaseURL: provider.BaseURL,
			Headers: headers,
		})
		newDecoder = openairesponses.DecoderFactory()
	default:
		// "openai-chat-completions" and the test-time "openai-compatible" both
		// resolve to the chat-completions dialect; the existing adapters/model/openai
		// already handles both.
		transport = openai.New(openai.Config{
			URL:     provider.BaseURL + "/chat/completions",
			Headers: headers,
		})
		newDecoder = nil // chat-completions is the default in modelclient.
	}
	return runtime.New(runtime.Deps{
		Clock:     clock{},
		IDs:       newIDs(),
		Outbox:    outbox{w: w},
		Store:     sessionStore,
		FS:        fsAdapter{port: fsx.NewPort()},
		Transport: transport,
		ModelConfig: modelclient.Config{
			ProviderID: provider.ProviderID, ModelID: provider.ModelID,
			ProviderKind: provider.ProviderKind, Transport: "sse",
			Retry: modelclient.ResolveRetryOptions(nil, nil),
			Clock: modelClock{},
			// 与 Node 的 Math.random() 对齐：重试退避抖动必须真随机，否则 delayMs 恒为下限值。
			Rand: rand.Float64,
			// NewDecoder picks the stream dialect matching the transport. nil falls
			// through to chat-completions, which is what every existing callsite
			// (and the mock-model in scripts/go-rewrite/record/) speaks.
			NewDecoder: newDecoder,
		},
		Provider:     provider,
		CWD:          cwd,
		Home:         home,
		Shell:        envFacts.shell,
		Platform:     envFacts.platform,
		OSVersion:    envFacts.osVersion,
		PlatformArch: envFacts.arch,
	}, "trace-"+newUUID(), version), nil
}

// openProviderConfig resolves the provider_config.json the same way the Node
// agent does: NEX_PERSONAL_PROVIDER_CONFIG_FILE wins over ~/.nex/v2/provider_config.json
// (packages/provider-node/src/runtime-paths.ts). The server spawns the agent with
// the env var set when the data dir is not $HOME; without it we fall through to
// the user's home directory.
func openProviderConfig() (runtime.Provider, error) {
	if env := os.Getenv("NEX_PERSONAL_PROVIDER_CONFIG_FILE"); env != "" {
		data, err := os.ReadFile(env)
		if err != nil {
			return runtime.Provider{}, err
		}
		return parseProviderConfig(data)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return runtime.Provider{}, err
	}
	path := filepath.Join(home, ".nex", "v2", "provider_config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return runtime.Provider{}, err
	}
	return parseProviderConfig(data)
}

type providerConfigFile struct {
	Config struct {
		ProviderConfigRules struct {
			ProviderRules []struct {
				ProviderID   string `json:"providerId"`
				ProviderName string `json:"providerName"`
				Config       struct {
					Access struct {
						Type   string `json:"type"`
						APIKey string `json:"apiKey"`
					} `json:"access"`
					API struct {
						Type    string `json:"type"`
						BaseURL string `json:"baseUrl"`
					} `json:"api"`
				} `json:"config"`
			} `json:"providerRules"`
		} `json:"providerConfigRules"`
		ModelConfigRules struct {
			ManualProviderModelRules []struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
				Config     struct {
					Enabled    bool   `json:"enabled"`
					Name       string `json:"name"`
					Properties struct {
						ContextWindow int64 `json:"contextWindow"`
					} `json:"properties"`
					OptionSpecs struct {
						ReasoningLevel struct {
							Values []string `json:"values"`
						} `json:"reasoningLevel"`
						MaxOutputTokens struct {
							Max int64 `json:"max"`
						} `json:"maxOutputTokens"`
					} `json:"optionSpecs"`
				} `json:"config"`
			} `json:"manualProviderModelRules"`
		} `json:"modelConfigRules"`
	} `json:"config"`
}

func parseProviderConfig(data []byte) (runtime.Provider, error) {
	var f providerConfigFile
	if err := json.Unmarshal(data, &f); err != nil {
		return runtime.Provider{}, err
	}
	if len(f.Config.ModelConfigRules.ManualProviderModelRules) == 0 {
		return runtime.Provider{}, errors.New("provider config has no manual model rules")
	}
	m := f.Config.ModelConfigRules.ManualProviderModelRules[0]
	p := runtime.Provider{
		ProviderID:      m.ProviderID,
		ModelID:         m.ModelID,
		ModelName:       m.Config.Name,
		ContextWindow:   m.Config.Properties.ContextWindow,
		MaxOutputToken:  m.Config.OptionSpecs.MaxOutputTokens.Max,
		ReasoningLevels: m.Config.OptionSpecs.ReasoningLevel.Values,
	}
	if len(p.ReasoningLevels) > 0 {
		p.DefaultReasoning = p.ReasoningLevels[0]
	}
	for _, pr := range f.Config.ProviderConfigRules.ProviderRules {
		if pr.ProviderID != m.ProviderID {
			continue
		}
		p.ProviderName = pr.ProviderName
		p.BaseURL = pr.Config.API.BaseURL
		p.APIKey = pr.Config.Access.APIKey
		if pr.Config.API.Type == "openai-chat-completions" {
			p.ProviderKind = "openai-compatible"
		} else {
			p.ProviderKind = pr.Config.API.Type
		}
	}
	return p, nil
}
