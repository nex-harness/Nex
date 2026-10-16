package proto

// clienttypes.go is the protocol client's vocabulary: the method names it sends
// and routes, the error types a caller branches on, the transport and handler
// contracts, and the per-call options. They are data rather than behaviour, and
// splitting them out keeps client.go to the correlation rules that actually need
// to be read as one piece.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tylinux/nex/go/internal/protocol"
)

// Method names the client sends or routes. The value is the wire contract.
const (
	MethodRuntimeCapabilities = "runtime/capabilities"
	NotifyStorageState        = "startup/storageState"

	InteractionRequestPermission             = "interaction/requestPermission"
	InteractionRequestUserInput              = "interaction/requestUserInput"
	InteractionRequestProviderRuntimeHeaders = "interaction/requestProviderRuntimeHeaders"
	InteractionRequestOfficialMcpAuthHeaders = "interaction/requestOfficialMcpAuthHeaders"
	InteractionBrowserList                   = "interaction/browserList"
	InteractionBrowserExecute                = "interaction/browserExecute"
)

// InteractionMethods is every agent→host request the client routes. Each is an
// agent request that needs a host answer: permission prompts, user input, browser
// control, provider runtime headers and official MCP auth headers.
var InteractionMethods = []string{
	InteractionRequestPermission,
	InteractionRequestUserInput,
	InteractionRequestProviderRuntimeHeaders,
	InteractionRequestOfficialMcpAuthHeaders,
	InteractionBrowserList,
	InteractionBrowserExecute,
}

// IsInteractionMethod reports whether a method is one of the routed interactions.
func IsInteractionMethod(method string) bool {
	for _, m := range InteractionMethods {
		if m == method {
			return true
		}
	}
	return false
}

// DefaultRequestTimeout is DEFAULT_NEX_PROTOCOL_REQUEST_TIMEOUT_MS: three
// minutes, long enough for a full model turn.
const DefaultRequestTimeout = 3 * time.Minute

// Errors the client itself produces.
var (
	// ErrDisposed is returned when a request is issued on a disposed client.
	ErrDisposed = errors.New("Nex Protocol client is disposed")
	// ErrTimeout marks a request that exceeded its own deadline. It is a distinct
	// type because the manager reports it separately from transport failures.
	ErrTimeout = errors.New("Nex Protocol request timed out")
	// ErrCapabilityUnsupported is proto.independentPlanUnsupported: the agent
	// answered runtime/capabilities but declined the capability.
	ErrCapabilityUnsupported = errors.New("proto.independentPlanUnsupported")
)

// TimeoutError carries the method, request id and budget of a timed-out call.
type TimeoutError struct {
	Method    string
	RequestID string
	Timeout   time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("Nex Protocol request timed out: %s", e.Method)
}

func (e *TimeoutError) Unwrap() error { return ErrTimeout }

// RequestError is a JSON-RPC error answer. CodeMethodNotFound on an interaction
// method is the normal "this build cannot ask" degradation, which the agent
// already handles.
type RequestError struct {
	Method  string
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("%s failed: rpc error %d: %s", e.Method, e.Code, e.Message)
}

// IsMethodNotFound reports whether the agent answered method-not-found, the signal
// the host reads as "capability absent".
func IsMethodNotFound(err error) bool {
	var re *RequestError
	if errors.As(err, &re) {
		return re.Code == protocol.CodeMethodNotFound
	}
	return false
}

// Lifecycle classifies a request for the process manager's health accounting.
type Lifecycle int

const (
	// LifecycleOperation is a business request: it participates in runtime health
	// and keeps the process alive.
	LifecycleOperation Lifecycle = iota
	// LifecycleObservation is a read-only probe. It must not keep a process alive
	// and must not be escalated into a process recycle on timeout.
	LifecycleObservation
)

// CallOptions are the per-call knobs.
type CallOptions struct {
	Lifecycle Lifecycle
	// Timeout overrides the client default for this call.
	Timeout time.Duration
	// Validate checks the result before it reaches the caller; a false return
	// rejects the call, as the TS result zod schema does.
	Validate func(result json.RawMessage) (any, error)
}

// CallResult is what a resolved request carries.
type CallResult struct {
	Method    string
	RequestID string
	Value     any
	Raw       json.RawMessage
}

// Transport is the byte boundary: one message out, one frame stream in.
// agentproc's process client satisfies it over stdio; tests satisfy it in memory.
type Transport interface {
	// Send writes one encoded protocol message.
	Send(msg protocol.Message) error
	// Frames delivers every stdout line the agent produced, closed when the
	// protocol leg ends (process exit, stdout EOF, fatal parse error).
	Frames() <-chan string
	// CloseReason reports why the leg ended. It is meaningful once Frames closed.
	CloseReason() string
	// Close ends the leg. Close calls it after rejecting its waiters.
	Close()
}

// Notification is a notification frame with its params kept raw.
type Notification struct {
	Method string
	Params json.RawMessage
	Trace  *protocol.Trace
}

// AgentRequest is an agent→host request. The client never answers these itself;
// it hands them to a registered handler, or answers method-not-found.
type AgentRequest struct {
	ID     protocol.RequestID
	Method string
	Params json.RawMessage
	Trace  *protocol.Trace
}

// InteractionHandler answers one agent→host interaction request. It returns the
// result JSON for the agent, or an error to answer with a JSON-RPC error (a
// *RequestError carries a chosen code).
type InteractionHandler func(ctx context.Context, req AgentRequest) (json.RawMessage, error)

// RequestTimeoutEvent reports a business request that timed out.
type RequestTimeoutEvent struct {
	Method    string
	RequestID string
	Timeout   time.Duration
}

type pendingCall struct {
	method string
	// id is the decimal correlation key; num is the same value as the integer the
	// request frame carries. Both are captured under the lock, so nothing outside
	// the critical section has to read the shared id counter.
	id          string
	num         int64
	observation bool
	timeout     time.Duration
	validate    func(json.RawMessage) (any, error)
	result      chan callOutcome
	timer       *time.Timer
	// suspended is set when a startup/storageState frame paused the watchdog,
	// which is what the TS does with clearTimeout + resumeTimeout.
	suspended bool
}

type callOutcome struct {
	res *CallResult
	err error
}

// Options configure a client.
type Options struct {
	// RequireStorageStartup holds the first turn until startup/storageState says
	// ready. It comes from the agent command's supportsStorageStartup.
	RequireStorageStartup bool
	// FirstStatusTimeout overrides FIRST_STATUS_TIMEOUT_MS.
	FirstStatusTimeout time.Duration
	// RequestTimeout overrides DEFAULT_NEX_PROTOCOL_REQUEST_TIMEOUT_MS.
	RequestTimeout time.Duration
}

// encodeParams serialises request/notification params. A nil params is omitted by
// the caller, so the agent sees exactly the keys the caller passed.
func encodeParams(params any) (json.RawMessage, error) { return json.Marshal(params) }

// encodeResult serialises a response result. A nil result is the JSON null the
// protocol requires for an answer with no payload.
func encodeResult(result any) (json.RawMessage, error) {
	if result == nil {
		return json.RawMessage("null"), nil
	}
	return json.Marshal(result)
}
