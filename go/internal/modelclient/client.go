package modelclient

import (
	"context"
	"time"
)

// Transport is the interface the model client uses to perform one HTTP attempt.
// The net/http implementation lives in internal/adapters/model/openai; keeping it
// behind an interface lets the retry loop, idle timeout and classification be
// tested against a fake.
type Transport interface {
	// Do sends req and returns the response. The returned ResponseStream must be
	// closed by the caller.
	Do(ctx context.Context, req Request) (*Response, error)
}

// Request is one model request attempt. Body is the exact bytes produced by
// go/internal/modelreq plus any history extensions; Headers are the full outbound
// header set. TraceID/RequestID/SessionID are attribution inputs the caller
// supplies.
type Request struct {
	// URL is the absolute chat-completions URL.
	URL string
	// Headers are the full outbound headers (including authorization and
	// attribution headers).
	Headers map[string]string
	// Body is the request body bytes.
	Body []byte
}

// Response is a transport response.
type Response struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// StatusText is the HTTP reason phrase.
	StatusText string
	// Headers are the response headers.
	Headers map[string]string
	// BodyText is the entire response body as text. For non-2xx responses the
	// transport reads it eagerly so the classifier can build an APICallError.
	BodyText string
	// Stream is non-nil for a successful streaming response.
	Stream ResponseStream
	// RequestURL and RequestBody are echoed for APICallError context.
	RequestBody []byte
}

// ResponseStream reads the SSE body incrementally and reports a terminal error
// (including mid-body disconnects).
type ResponseStream interface {
	// Read returns the next chunk; it returns io.EOF at the end of the body and a
	// non-nil error on a transport failure.
	Read([]byte) (int, error)
	// Close releases the underlying body.
	Close() error
}

// Clock abstracts time so idle timeouts and backoff are deterministic in tests.
type Clock interface {
	Now() time.Time
	// Sleep blocks for d or until ctx is done, returning ctx.Err() when cancelled.
	Sleep(ctx context.Context, d time.Duration) error
	// After reports a channel that fires after d.
	After(d time.Duration) <-chan time.Time
}

// realClock is the production clock.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
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
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RealClock returns the production clock.
func RealClock() Clock { return realClock{} }

// TraceContext carries the attribution ids threaded into headers and telemetry.
type TraceContext struct {
	TraceID         string
	RequestID       string
	SessionID       string
	TurnID          string
	QueryID         string
	QuerySource     string
	SpanID          string
	ParentSpanID    string
	ParentSessionID string
	ToolCallID      string
}

// Attempt describes one physical model request attempt.
type Attempt struct {
	Number int
	// MaxAttempts is the total attempt budget (0 = unbounded).
	MaxAttempts int
	RequestID   string
}

// StatusEvent is the telemetry transition the Retrier emits, mirroring the
// model.request.status kind in the wire protocol. The lead's turn loop turns
// these into v4/telemetry/event frames.
type StatusEvent struct {
	Status                string // model_request_started | model_request_completed | model_request_failed | model_retry_scheduled | model_stream_stalled
	Attempt               int
	MaxAttempts           int
	RequestID             string
	ProviderID            string
	ModelID               string
	ProviderKind          string
	TraceID               string
	SessionID             string
	TurnID                string
	QueryID               string
	QuerySource           string
	Transport             string
	DurationMs            *int64
	Reason                string
	RetryReason           string
	Message               string
	ErrorCode             string
	StatusCode            *int
	Retryable             bool
	RetryAfterMs          *int64
	DelayMs               *int64
	NextAttempt           int
	FinishReason          string
	Usage                 *Usage
	StreamOutputCommitted bool
	IdleMs                *int64
	TimeoutMs             *int64
}

// StatusSink receives the attempt transitions. It is the seam the turn loop uses
// to emit S13's model.request.status sequence.
type StatusSink interface {
	Publish(StatusEvent)
}

// Config is the model client configuration.
type Config struct {
	ProviderID   string
	ModelID      string
	ProviderKind string
	Transport    string // "sse" or "http"
	Retry        ResolvedRetryOptions
	RetryBudget  string
	IdleTimeout  int // ms
	// StreamIdleTimeoutRetryNumber is added to the idle window per retry.
	StreamIdleTimeoutRetryNumber *int
	// NewDecoder builds the per-attempt stream decoder. Nil means the
	// chat-completions decoder, which is every call site that existed before the
	// Responses dialect existed; the dialect chooses the decoder, so nothing here
	// has to know which provider is on the wire.
	NewDecoder DecoderFactory
	Clock      Clock
	Rand       RandFunc
}

// Client performs a streaming chat-completions request with the agent's retry,
// idle-timeout and classification behavior.
type Client struct {
	cfg       Config
	transport Transport
	status    StatusSink
}

// NewClient builds a client.
func NewClient(cfg Config, transport Transport, status StatusSink) *Client {
	if cfg.Clock == nil {
		cfg.Clock = RealClock()
	}
	return &Client{cfg: cfg, transport: transport, status: status}
}

// decoderFactoryFor returns the configured per-attempt decoder factory, or the
// chat-completions one when the caller did not choose a dialect.
func decoderFactoryFor(cfg Config) DecoderFactory {
	if cfg.NewDecoder == nil {
		return chatDecoderFactory
	}
	return cfg.NewDecoder
}

// NewDecoderFunc is a factory the dialect packages (adapters/model/openairesponses,
// adapters/model/anthropic) hand back so cmd/nex-agent can inject the right
// decoder into Config.NewDecoder without an import cycle.
//
// The ProviderKind seam is what cmd/nex-agent/runtime.go switches on:
// "openai-responses" picks the openairesponses decoder, "anthropic" picks the
// anthropic decoder, anything else stays on the chat-completions decoder.
// The packages are imported by their blank identifier in cmd/nex-agent so the
// dialect registry lives only in main, not in modelclient itself.

// attemptOutcome is the result of one attempt for the retry loop.
type attemptOutcome struct {
	retry    bool
	delayMs  int64
	failure  Failure
	terminal error
}
