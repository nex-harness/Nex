// Package v4pub implements the v4 conversation topic publisher: the CLI-side
// authority for the "conversation/<sessionId>" topic. It owns the bounded
// in-memory delta log (logEpoch + retention window), the subscribe(base)
// resume/snapshot decision, and the per-subscriber flush pipeline
// (profile filter -> coalesce -> framing).
//
// Scope boundary (mirrors conversation-topic-publisher.ts):
//   - This package only does the authoritative "event -> frame" bookkeeping. It
//     performs no network I/O and holds no timers: the host drives flush timing
//     using the delivery profile's flushWindowMs (tests call Flush manually).
//     The publisher is therefore a pure, testable state machine.
//   - Recovery and live streaming share one pipeline. A resume's initial frame
//     is the (base.seq, current] slice of the retained log, filtered by the
//     subscriber profile and then coalesced, so the
//     "snapshot(W) + stream == full replay" golden property covers recovery.
//   - Re-subscribe = replace: a second subscribe from the same connectionId
//     retires the old subscription and clears its flush buffer; the old
//     subscriptionId stops producing frames and the client drops stale
//     generations by subscriptionId.
//
// The event projection (SessionEvent -> []Delta) is Agent B's product
// projection. This package treats deltas and snapshots as opaque JSON: it
// carries them, coalesces adjacent ones, and never inspects their inner shape,
// except for the closed set of op discriminators and two state keys
// (workflowRuns, revision) that the coalescing rules constrain.
package v4pub

import "fmt"

// V4WireProtocolVersion is V4_WIRE_PROTOCOL_VERSION (shared/nex-protocol-v4/core.ts).
const V4WireProtocolVersion = 3

// Limit constants copied from PROTOCOL_V4_LIMITS in
// shared/nex-protocol-v4/core.ts. The vector test compares them with the values
// the generator read from the TypeScript module, so a change on either side
// fails a test instead of drifting silently.
const (
	MaxFrameBytes                    = 1024 * 1024
	LogicalFrameAssemblyMaxBytes     = 16 * 1024 * 1024
	LogicalFrameAssemblyMaxFragments = 1024
	SubscriberBufferMaxOps           = 500
	SubscriberBufferMaxBytes         = 1024 * 1024
	EventRetentionPerSession         = 2000
	SnapshotTailWindowRows           = 60
	// WorkflowRunsLegacyMaxActors/Nodes are WORKFLOW_RUNS_LEGACY_LIMITS: the
	// per-table bounds an old consumer's binary enforces. A frame that exceeds
	// them fails to parse and is dropped whole, so every frame for a legacy
	// subscriber must be clamped first.
	WorkflowRunsLegacyMaxActors = 256
	WorkflowRunsLegacyMaxNodes  = 256
)

// DeliveryProfile is the flush-pipeline parameter set of one subscriber. It
// lives only on the CLI side; client code never sees a profile variable.
type DeliveryProfile struct {
	DesktopOnlyRows      bool
	FlushWindowMs        int
	Text                 bool
	InputText            bool
	OutputText           bool
	SummaryText          bool
	StreamOutputCapBytes int
	ToolProgress         bool
}

// DeliveryProfileName selects one of the two profiles.
type DeliveryProfileName string

const (
	// ProfileContinuous is the desktop-continuous link (flushWindowMs 30).
	ProfileContinuous DeliveryProfileName = "continuous"
	// ProfileReplayable is the web-remote-replayable link (flushWindowMs 150).
	ProfileReplayable DeliveryProfileName = "replayable"
)

// DeliveryProfiles mirrors DELIVERY_PROFILES from core.ts.
var DeliveryProfiles = map[DeliveryProfileName]DeliveryProfile{
	ProfileContinuous: {
		DesktopOnlyRows:      true,
		FlushWindowMs:        30,
		Text:                 true,
		InputText:            true,
		OutputText:           true,
		SummaryText:          true,
		StreamOutputCapBytes: 262144,
		ToolProgress:         false,
	},
	ProfileReplayable: {
		DesktopOnlyRows:      false,
		FlushWindowMs:        150,
		Text:                 true,
		InputText:            false,
		OutputText:           false,
		SummaryText:          false,
		StreamOutputCapBytes: 0,
		ToolProgress:         true,
	},
}

// ProfileForClientMode maps the trusted clientMode to a profile name, as
// v4-gateway.subscribeReserved does (desktop-continuous -> continuous,
// everything else -> replayable).
func ProfileForClientMode(clientMode string) DeliveryProfileName {
	if clientMode == "desktop-continuous" {
		return ProfileContinuous
	}
	return ProfileReplayable
}

// DeliveryKind is the authoritative purpose of a physical frame. Consumers must
// not guess it from RPC timing.
type DeliveryKind string

const (
	DeliveryInitial  DeliveryKind = "initial"
	DeliveryOnline   DeliveryKind = "online"
	DeliveryRecovery DeliveryKind = "recovery"
)

// AckMode is the subscribe verdict.
type AckMode string

const (
	ModeSnapshot AckMode = "snapshot"
	ModeResume   AckMode = "resume"
)

// Limits is the publisher's own budget set, resolved from the protocol maxima.
type Limits struct {
	// Retention is the event retention window in entries.
	Retention int
	// SubscriberBufferMaxOps caps coalesced ops per subscriber buffer.
	SubscriberBufferMaxOps int
	// SubscriberBufferMaxBytes caps the UTF-8 bytes of the deltas payload.
	SubscriberBufferMaxBytes int
}

// DefaultLimits returns the protocol defaults.
func DefaultLimits() Limits {
	return Limits{
		Retention:                EventRetentionPerSession,
		SubscriberBufferMaxOps:   SubscriberBufferMaxOps,
		SubscriberBufferMaxBytes: SubscriberBufferMaxBytes,
	}
}

// ProjectionPayloadTooLargeError is raised when a candidate projection exceeds
// the logical frame assembly limit. It mirrors ProjectionPayloadTooLargeError.
type ProjectionPayloadTooLargeError struct {
	ReasonCode   string
	LogicalBytes int
}

func (e *ProjectionPayloadTooLargeError) Error() string {
	return fmt.Sprintf(
		"conversation projection exceeds %d bytes (reason %s, %d bytes)",
		LogicalFrameAssemblyMaxBytes, e.ReasonCode, e.LogicalBytes,
	)
}

// NewProjectionPayloadTooLargeError builds the error with its fixed reason code.
func NewProjectionPayloadTooLargeError(logicalBytes int) *ProjectionPayloadTooLargeError {
	return &ProjectionPayloadTooLargeError{
		ReasonCode:   "proto.payloadTooLarge",
		LogicalBytes: logicalBytes,
	}
}

// nonNegativeHardBound resolves an optional limit: a nil value means "use the
// maximum"; a value is floored and capped at the maximum. A negative value is a
// programming error surfaced as an error, matching the RangeError the TypeScript
// helper throws.
func nonNegativeHardBound(value *int, maximum int, name string) (int, error) {
	resolved := maximum
	if value != nil {
		resolved = *value
	}
	if resolved < 0 {
		return 0, fmt.Errorf("%s must be a non-negative finite number", name)
	}
	if resolved > maximum {
		return maximum, nil
	}
	return resolved, nil
}
