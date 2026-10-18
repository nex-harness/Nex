package taskadapter

import (
	"fmt"
)

// cas.go — the host-side v4 CAS command submission. Ports nexV4HostCommand.ts
// (guards C1–C8).
//
// The host has no local v4 projection, so it cannot know the current conversation
// revision. The protocol's answer to that is: a stale ACK always carries
// `revisionAtDecision`, so the host re-submits against that and converges. The first
// attempt probes with baseRevision=0 and the typical path takes exactly one stale.
//
// The important consequence for stale-run protection (C8): a command carrying a stale
// baseRevision is refused AT THE CLI BOUNDARY and never executes. Convergence is a
// WRITE retry, not a bypass — nothing about the stale command's effect becomes visible.

// CommandAckStatus is the v4 ACK's status union.
type CommandAckStatus string

const (
	AckAccepted  CommandAckStatus = "accepted"
	AckDuplicate CommandAckStatus = "duplicate"
	AckNoop      CommandAckStatus = "noop"
	AckRejected  CommandAckStatus = "rejected"
	AckStale     CommandAckStatus = "stale"
	AckFailed    CommandAckStatus = "failed"
)

// CommandAck is the subset of the v4 ACK this port reads.
type CommandAck struct {
	CommandID string
	Status    CommandAckStatus
	// ReasonCode is carried through verbatim so callers branch on the code, never on
	// the message text.
	ReasonCode string
	Message    string
	// RevisionAtDecision is the server's current revision at the moment it refused.
	// A stale ACK is REQUIRED to carry it; without it convergence would be impossible.
	RevisionAtDecision int64
	Result             map[string]any
}

// commandsRequiringBaseRevision is COMMANDS_REQUIRING_BASE_REVISION: the CAS command
// types that must carry a baseRevision.
var commandsRequiringBaseRevision = map[string]bool{
	"switchModelConfig":       true,
	"switchCollaborationMode": true,
}

// rowTargetingCommands is ROW_TARGETING_COMMANDS: the types that must also carry a
// baseLogEpoch.
var rowTargetingCommands = map[string]bool{}

// CommandRequest is one CAS submission.
type CommandRequest struct {
	// Type is the command type.
	Type string
	// SessionID is the target session (nil-able for createSession).
	SessionID string
	// BaseRevision is required for a CAS command (C1).
	BaseRevision *int64
	// BaseLogEpoch is required for a row-targeting command (C2).
	BaseLogEpoch string
	// CommandID is the idempotency key. A stale verdict never enters the idempotency
	// table, so each retry MUST use a fresh one (C6).
	CommandID string
	// ClientID identifies the submitting end.
	ClientID string
	Payload  map[string]any
}

// CommandRejectedError is NexV4CommandRejectedError: the structured refusal the UI
// branches on by code.
type CommandRejectedError struct {
	CommandType string
	Ack         CommandAck
	Context     string
}

func (e *CommandRejectedError) Error() string {
	return fmt.Sprintf("v4 command %s %s (%s): %s — %s",
		e.CommandType, e.Ack.Status, e.Ack.ReasonCode, e.Ack.Message, e.Context)
}

// Code is the stable discriminator.
func (e *CommandRejectedError) Code() string { return "NEX_V4_COMMAND_REJECTED" }

// ValidateEnvelope ports the two createHostCommandEnvelope guards (C1, C2).
//
// A CAS command without a baseRevision is a WRITE WITH NO GUARD. Failing it here, at
// construction, is what keeps a caller from "fixing" the error by simply sending the
// command — the revision has to come from somewhere.
func ValidateEnvelope(request CommandRequest) error {
	if commandsRequiringBaseRevision[request.Type] && request.BaseRevision == nil {
		return fmt.Errorf("command %s 是 CAS 命令，必须携带 baseRevision", request.Type)
	}
	if rowTargetingCommands[request.Type] && request.BaseLogEpoch == "" {
		return fmt.Errorf("command %s 是 row target 命令，必须携带 baseLogEpoch", request.Type)
	}
	return nil
}

// AssertAckOK ports assertV4CommandAckOk (C3, C4).
//
// accepted / duplicate / noop are all idempotent success. `duplicate` matters most: a
// retried commandId replays the first verdict, and treating that as a failure would
// turn every mobile retry into a user-visible error even though the write landed.
func AssertAckOK(commandType string, ack CommandAck, contextMessage string) (CommandAck, error) {
	switch ack.Status {
	case AckAccepted, AckDuplicate, AckNoop:
		return ack, nil
	}
	return ack, &CommandRejectedError{CommandType: commandType, Ack: ack, Context: contextMessage}
}

// CASOptions configures SubmitCAS.
type CASOptions struct {
	// Type is the command type.
	Type string
	// SessionID is the target session.
	SessionID string
	// Payload is the command payload, forwarded unchanged.
	Payload map[string]any
	// ContextMessage names the business operation in a refusal.
	ContextMessage string
	// MaxAttempts defaults to 4.
	MaxAttempts int
	// NewCommandID mints an idempotency key per attempt (C6).
	NewCommandID func(attempt int) string
}

// DefaultCASAttempts is sendHostCasCommandV4's default maxAttempts.
const DefaultCASAttempts = 4

// SubmitCAS ports sendHostCasCommandV4 (C5–C7).
//
// Three rules, and the second one is the one that is easy to get wrong:
//
//   - a stale ACK is NOT a failure. It is information: the server tells us its current
//     revision, and we converge by re-submitting against it (C5). The old facade never
//     carried a revision at all, so for that caller a stale means "someone else moved
//     the config concurrently", not "conflict" — retrying IS the faithful translation.
//   - each attempt uses a NEW commandId (C6). A stale verdict does not enter the
//     idempotency table, so reusing the id would replay the stale verdict forever.
//   - attempts are bounded (C7). Pathological concurrency must fail, not loop.
func SubmitCAS(send func(CommandRequest) (CommandAck, error), opts CASOptions) (CommandAck, error) {
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultCASAttempts
	}
	// The probe revision is 0: the host has no projection to read, and 0 is the value
	// that will be stale on any conversation that has had any config change.
	baseRevision := int64(0)
	var lastAck CommandAck
	haveLast := false

	for attempt := 0; attempt < maxAttempts; attempt++ {
		request := CommandRequest{
			Type:         opts.Type,
			SessionID:    opts.SessionID,
			BaseRevision: &baseRevision,
			Payload:      opts.Payload,
		}
		if opts.NewCommandID != nil {
			request.CommandID = opts.NewCommandID(attempt)
		}
		ack, err := send(request)
		if err != nil {
			return ack, err
		}
		if ack.Status != AckStale {
			return AssertAckOK(opts.Type, ack, opts.ContextMessage)
		}
		lastAck = ack
		haveLast = true
		// 「stale ACK 必带 revisionAtDecision」— converge on the server's own truth.
		baseRevision = ack.RevisionAtDecision
	}

	if !haveLast {
		lastAck = CommandAck{Status: AckStale, RevisionAtDecision: baseRevision}
	}
	return lastAck, &CommandRejectedError{
		CommandType: opts.Type,
		Ack:         lastAck,
		Context:     opts.ContextMessage + "（连续 stale，放弃重试）",
	}
}
