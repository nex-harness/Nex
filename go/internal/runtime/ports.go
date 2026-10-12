// Package runtime orchestrates the agent's observable behaviour: the v4
// command handlers, the session turn loop, model streaming, tools and the
// non-conversation frames around a turn. It performs no I/O itself; every
// external effect is reached through an interface defined here and wired to a
// concrete adapter in cmd/nex-agent.
//
// The behaviour reproduced (and the two sequence-number views) is documented in
// docs/specs/go-rewrite/runtime.md.
package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/protocol"
	"github.com/tylinux/nex/go/internal/sessionstore"
	"github.com/tylinux/nex/go/internal/tools"
)

// Clock is the pipeline's time source. Frame timestamps, event timestamps and
// the flush timer all read it, so tests inject a fake.
type Clock interface {
	NowMs() int64
	// NewTimer returns a channel that fires after d. The returned stop must be
	// called to release the timer.
	NewTimer(d time.Duration) (<-chan time.Time, func())
}

// IDs mints the identifiers the Node agent generates: session/turn/message/part
// ids, event ids and uuids. Injecting it keeps the wire deterministic in tests.
type IDs interface {
	SessionID() string
	TurnID() string
	MessageID() string
	PartID() string
	EventID() string
	UUID() string
	// LogEpoch is the v4 topic generation printed as the subscription/log prefix.
	LogEpoch() string
}

// Outbox is the frame sink. Every frame the runtime emits goes through it in
// order. The concrete implementation serialises writes (protocol.FrameWriter).
type Outbox interface {
	WriteMessage(protocol.Message) error
}

// Host is the reverse channel to the host. It is implemented by the runtime
// itself (the frame reader routes answers back through HandleResponse); the
// interface stays so tests can substitute a fake.
type Host interface {
	Request(ctx context.Context, msg protocol.Message) (json.RawMessage, error)
	NextPrefsID() protocol.RequestID
}

// ModelTransport opens a streaming request; it is modelclient.Transport.
type ModelTransport = modelclient.Transport

// FS is the filesystem port used by the Read/Write/Edit tools. The concrete
// implementation is adapters/fsx.Port.
type FS interface {
	ToolFS() tools.FS
}

// Workspace is the resolved working directory and identity key.
type Workspace struct {
	Path string
	Key  string
}

// Provider is one model the agent can run: its provider/model ids, the endpoint
// and the outbound header values. Parsed from provider_config.json.
type Provider struct {
	ProviderID     string
	ProviderName   string
	ProviderKind   string
	ModelID        string
	ModelName      string
	BaseURL        string
	APIKey         string
	ContextWindow  int64
	MaxOutputToken int64
	// ReasoningLevels are the selectable reasoning levels ("none" for the mock).
	ReasoningLevels []string
	// DefaultReasoning is the default level.
	DefaultReasoning string
	// InsecureSkipVerify is set for http (the mock); the transport keeps TLS on.
	HTTP bool
}

// Store is the session persistence port (sessionstore.Store) used by the turn
// loop. It is an alias so the runtime depends only on the port package.
type Store = sessionstore.Store

// ModelClientStream is retained for tests; production builds a modelclient.Client
// per turn from Deps.ModelConfig and Deps.Transport so each turn has its own
// status sink.
type ModelClientStream interface {
	Stream(ctx context.Context, req modelclient.Request) *modelclient.StreamHandle
}

// Deps are the injected collaborators the runtime needs. Everything the runtime
// cannot construct itself (I/O, ids, time, the model client) is here.
type Deps struct {
	Clock     Clock
	IDs       IDs
	Outbox    Outbox
	Store     Store
	FS        FS
	Transport ModelTransport
	// ModelConfig is the model client configuration (provider identity, retry).
	ModelConfig modelclient.Config
	// Provider is the model resolved from the provider config.
	Provider Provider
	// HeaderInput are the static outbound header values (attribution).
	HeaderInput BuildHeadersInput
	// CWD is the process working directory; the workspace defaults to it.
	CWD string
	// Home is the user home directory (proofed for .nex paths / prompt memory).
	Home string
	// Shell is the env shell name (from $SHELL basename or the prefs reply).
	Shell string
	// Platform / OSVersion are the process platform facts.
	Platform  string
	OSVersion string
	// PlatformArch is the Node-style arch name ("x64"/"arm64").
	PlatformArch string
	// ToolDefs are the default tool definitions sent to the model.
	Tools []ToolDef
	// Now is a convenience for tests; Clock.NowMs is authoritative.
	Now func() int64
}

// BuildHeadersInput mirrors the openai adapter's HeaderInput without importing
// it (avoids a cycle): the adapter converts.
type BuildHeadersInput struct {
	UserAgent      string
	RuntimeVersion string
	AppVersion     string
	Title          string
	ReleaseChannel string
	ClientLanguage string
	ClientTimezone string
	NexAgent       string
	Platform       string
	OSCategory     string
	OSVersion      string
	HTTPReferer    string
}

// ToolDef is the model-facing tool definition (raw JSON) the runtime sends.
type ToolDef struct {
	Name string
	JSON json.RawMessage
}
