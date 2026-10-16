package proto

// wireassembler.go is the assembler state machine: Accept and the helpers that
// decide one wire frame's fate. The lifecycle side (timeouts, discard, abort,
// stats) and the bookkeeping side (release/settle) are in their own files.
//
// The invariants, in the order Accept enforces them — the order is load-bearing:
//
//  1. Ordinals gate first, before any envelope/base64 work. A late fragment of an
//     already-settled frame must never free the assembly of the newer frame that
//     replaced it.
//  2. A recovery frame supersedes an older in-progress online assembly silently:
//     reporting that old assembly as superseded would make the decoder fail-close
//     and drop the very recovery frame that was arriving. Any other supersede is a
//     fault.
//  3. Budgets (physical frame size, fragment count, logical bytes, staged decode
//     bytes, concurrency) release the route's staged bytes and raise a typed fault
//     rather than growing.
//  4. Only after crc32 + UTF-8 + JSON + envelope match + payload schema all pass
//     is one logical frame emitted, exactly once.
//
// The per-route ordinal tombstone is not an LRU: a settled ordinal is never
// forgotten on its own, so a very old replay cannot resurrect a settled frame.

import (
	"bytes"
	"encoding/json"
	"math"
	"unicode/utf8"

	"github.com/tylinux/nex/go/internal/protocol"
)

// NewAssembler constructs an Assembler.
func NewAssembler(schema FrameSchema, opts AssemblerOptions) *Assembler {
	if opts.TimeoutMs <= 0 {
		opts.TimeoutMs = defaultTimeoutMs
	}
	return &Assembler{
		schema:               schema,
		assemblies:           map[string]*fragmentAssembly{},
		settledByRoute:       map[string]settled{},
		maxAssemblyBytes:     hardBound(opts.MaxAssemblyBytes, protocol.LogicalFrameAssemblyMaxBytes),
		maxFragments:         hardBound(opts.MaxFragments, protocol.LogicalFrameAssemblyMaxFragments),
		maxConcurrent:        hardBound(opts.MaxConcurrentAssemblies, defaultMaxConcurrent),
		maxStagedDecodedByte: hardBound(opts.MaxStagedDecodedBytes, defaultMaxStaged),
		timeoutMs:            opts.TimeoutMs,
		maxPhysicalBytes:     hardBound(opts.MaxPhysicalFrameBytes, protocol.MaxFrameBytes),
	}
}

// hardBound clamps a configured limit to the protocol maximum, defaulting to the
// maximum when zero.
//
// The TS constructor THROWS on a non-positive or non-finite limit. A Go
// constructor has no way to return an error without changing every call site for
// a case that only a misconfiguration can produce, so this treats any value ≤ 0
// as "unset". That is the safe direction: it cannot produce an unbounded
// assembler, which is the only failure that would be unsafe.
func hardBound(v, maximum int) int {
	if v <= 0 || v > maximum {
		return maximum
	}
	return v
}

// Accept runs one physical frame through the state machine and returns the
// resulting events (an expire batch first, then at most one completion or fault).
func (a *Assembler) Accept(wire Candidate, nowMs int64) []Event {
	events := a.Expire(nowMs)
	key := routeKey(wire.Topic, wire.SubscriptionID)

	if !safeInteger(wire.LogicalFrameOrdinal) || wire.LogicalFrameOrdinal < 1 {
		return append(events, a.fault(wire, ReasonAssemblyMetadataMismatch))
	}

	// Ordinal eviction runs before envelope/base64 work: a late bad fragment from
	// an old frame must not free the newer frame currently assembling.
	if s, ok := a.settledByRoute[key]; ok {
		if wire.LogicalFrameOrdinal < s.logicalFrameOrdinal {
			return events
		}
		if wire.LogicalFrameOrdinal == s.logicalFrameOrdinal {
			if wire.LogicalFrameID != s.logicalFrameID {
				events = append(events, a.fault(wire, ReasonAssemblyOrdinalConflict))
			}
			return events
		}
	}

	deliveryKind, kindOK := deliveryKindValid(wire.DeliveryKind, wire.HasDeliveryKind)
	if deliveryKind == protocol.DeliveryRecovery {
		// An older online assembly can expire in the same call the recovery
		// frame's first fragment arrives. Reporting that stale fault would make
		// the consumer fail-close and lose the recovery frame we just accepted.
		events = dropStaleFaults(events, wire)
	}

	current := a.assemblies[key]
	if current != nil {
		switch {
		case wire.LogicalFrameOrdinal < current.logicalFrameOrdinal:
			return events
		case wire.LogicalFrameOrdinal == current.logicalFrameOrdinal:
			if wire.LogicalFrameID != current.logicalFrameID {
				a.release(key, current)
				a.settle(key, current.logicalFrameID, current.logicalFrameOrdinal)
				events = append(events, a.fault(wire, ReasonAssemblyOrdinalConflict))
				return events
			}
		default: // a strictly newer ordinal supersedes
			a.release(key, current)
			a.settle(key, current.logicalFrameID, current.logicalFrameOrdinal)
			// Only an authoritative recovery may supersede without a fault.
			if !kindOK || deliveryKind != protocol.DeliveryRecovery {
				events = append(events, a.faultOnAssembly(current, ReasonAssemblySuperseded))
			}
		}
	}

	if !kindOK {
		a.releaseAndSettle(key, wire)
		return append(events, a.fault(wire, ReasonAssemblyMetadataMismatch))
	}

	if wire.Kind == "complete" {
		return append(events, a.acceptComplete(key, wire, deliveryKind))
	}
	return append(events, a.acceptFragment(key, wire, deliveryKind, nowMs)...)
}

// safeInteger mirrors Number.isSafeInteger, which the wire schema requires of
// logicalFrameOrdinal. Go int64 already covers the safe range; the check exists
// because JSON can carry a number wider than int64 (rejected) or a float (rejected
// at parse), so reaching here means "is a non-negative-safe integer".
func safeInteger(n int64) bool { return n <= math.MaxInt64 && n >= math.MinInt64 }

func dropStaleFaults(events []Event, wire Candidate) []Event {
	out := events[:0]
	for _, e := range events {
		if e.IsFault && e.Fault.Topic == wire.Topic && e.Fault.SubscriptionID == wire.SubscriptionID &&
			e.Fault.LogicalFrameOrdinal < wire.LogicalFrameOrdinal {
			continue
		}
		out = append(out, e)
	}
	return out
}

// acceptComplete handles a whole logical frame carried in one physical frame.
func (a *Assembler) acceptComplete(key string, wire Candidate, kind protocol.DeliveryKind) Event {
	if len(wire.KeysExtra) > 0 || !wire.HasFrame {
		a.releaseAndSettle(key, wire)
		return a.fault(wire, ReasonAssemblyMetadataMismatch)
	}
	if wire.measuredBytes() > a.maxPhysicalBytes {
		a.releaseAndSettle(key, wire)
		return a.fault(wire, ReasonAssemblyEnvelopeTooLarge)
	}
	if active := a.assemblies[key]; active != nil {
		a.release(key, active)
		a.settle(key, active.logicalFrameID, active.logicalFrameOrdinal)
		return a.faultOnAssembly(active, ReasonAssemblyMetadataMismatch)
	}
	if len(wire.Frame) > a.maxAssemblyBytes {
		a.settle(key, wire.LogicalFrameID, wire.LogicalFrameOrdinal)
		return a.fault(wire, ReasonAssemblyTooLarge)
	}
	if !frameMatchesEnvelope(wire.Frame, wire.Topic, wire.SubscriptionID) {
		a.settle(key, wire.LogicalFrameID, wire.LogicalFrameOrdinal)
		return a.fault(wire, ReasonAssemblyMetadataMismatch)
	}
	parsed, ok := a.schema(wire.Frame)
	if !ok {
		a.settle(key, wire.LogicalFrameID, wire.LogicalFrameOrdinal)
		return a.fault(wire, ReasonAssemblyInvalidPayload)
	}
	a.settle(key, wire.LogicalFrameID, wire.LogicalFrameOrdinal)
	return Event{Frame: parsed, DeliveryKind: kind}
}

// acceptFragment handles one physical fragment and returns 0 or 1 event.
func (a *Assembler) acceptFragment(key string, wire Candidate, kind protocol.DeliveryKind, nowMs int64) []Event {
	if !wire.validateFragmentInner() {
		a.releaseAndSettle(key, wire)
		return []Event{a.fault(wire, ReasonAssemblyMetadataMismatch)}
	}
	if wire.measuredBytes() > a.maxPhysicalBytes {
		a.releaseAndSettle(key, wire)
		return []Event{a.fault(wire, ReasonAssemblyEnvelopeTooLarge)}
	}
	if wire.FragmentCount > int64(a.maxFragments) {
		a.releaseAndSettle(key, wire)
		return []Event{a.fault(wire, ReasonFragmentCountExceeded)}
	}
	if wire.LogicalBytes > int64(a.maxAssemblyBytes) {
		a.releaseAndSettle(key, wire)
		return []Event{a.fault(wire, ReasonAssemblyTooLarge)}
	}
	algorithm, checksumValue := wire.checksumFields()
	if !fragmentMetaConsistent(wire, algorithm, checksumValue) {
		a.releaseAndSettle(key, wire)
		return []Event{a.fault(wire, ReasonAssemblyMetadataMismatch)}
	}

	decoded, ok := protocol.DecodeBase64(wire.DataBase64)
	if !ok {
		a.releaseAndSettle(key, wire)
		return []Event{a.fault(wire, ReasonAssemblyInvalidBase64)}
	}

	assembly := a.assemblies[key]
	if assembly != nil {
		if assembly.fragmentCount != int(wire.FragmentCount) ||
			assembly.deliveryKind != kind ||
			assembly.logicalBytes != int(wire.LogicalBytes) ||
			assembly.checksumValue != checksumValue {
			a.release(key, assembly)
			a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
			return []Event{a.fault(wire, ReasonAssemblyMetadataMismatch)}
		}
	} else {
		if len(a.assemblies) >= a.maxConcurrent {
			a.settle(key, wire.LogicalFrameID, wire.LogicalFrameOrdinal)
			return []Event{a.fault(wire, ReasonAssemblyConcurrentLimit)}
		}
		if a.stagedDecodedBytes+len(decoded) > a.maxStagedDecodedByte {
			a.settle(key, wire.LogicalFrameID, wire.LogicalFrameOrdinal)
			return []Event{a.fault(wire, ReasonAssemblyBudgetExceeded)}
		}
		assembly = &fragmentAssembly{
			deliveryKind:        kind,
			logicalFrameID:      wire.LogicalFrameID,
			logicalFrameOrdinal: wire.LogicalFrameOrdinal,
			topic:               wire.Topic,
			subscriptionID:      wire.SubscriptionID,
			fragmentCount:       int(wire.FragmentCount),
			logicalBytes:        int(wire.LogicalBytes),
			checksumValue:       checksumValue,
			fragments:           make([][]byte, wire.FragmentCount),
			firstSeenAt:         nowMs,
		}
		a.assemblies[key] = assembly
	}

	// Duplicate fragment: identical bytes are a benign retransmit, different bytes
	// are a conflict that must free the route.
	if previous := assembly.fragments[wire.FragmentIndex]; previous != nil {
		if !bytes.Equal(previous, decoded) {
			a.release(key, assembly)
			a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
			return []Event{a.fault(wire, ReasonAssemblyFragmentConflict)}
		}
		return nil
	}
	if a.stagedDecodedBytes+len(decoded) > a.maxStagedDecodedByte {
		a.release(key, assembly)
		a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
		return []Event{a.fault(wire, ReasonAssemblyBudgetExceeded)}
	}
	if assembly.decodedBytes+len(decoded) > assembly.logicalBytes {
		a.release(key, assembly)
		a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
		return []Event{a.fault(wire, ReasonAssemblyLengthMismatch)}
	}
	assembly.fragments[wire.FragmentIndex] = decoded
	assembly.receivedCount++
	assembly.decodedBytes += len(decoded)
	a.stagedDecodedBytes += len(decoded)
	if assembly.receivedCount != assembly.fragmentCount {
		return nil
	}

	return a.finishAssembly(key, assembly)
}

// finishAssembly concatenates a complete set of fragments and validates crc32,
// UTF-8, JSON, envelope match and payload schema, emitting the single logical
// frame only when all of them pass.
func (a *Assembler) finishAssembly(key string, assembly *fragmentAssembly) []Event {
	a.release(key, assembly)
	if assembly.decodedBytes != assembly.logicalBytes {
		a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
		return []Event{a.faultOnAssembly(assembly, ReasonAssemblyLengthMismatch)}
	}
	logical := make([]byte, 0, assembly.decodedBytes)
	for _, fragment := range assembly.fragments {
		if fragment == nil {
			a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
			return []Event{a.faultOnAssembly(assembly, ReasonAssemblyLengthMismatch)}
		}
		logical = append(logical, fragment...)
	}
	if protocol.CRC32Hex(logical) != assembly.checksumValue {
		a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
		return []Event{a.faultOnAssembly(assembly, ReasonAssemblyChecksumMismatch)}
	}
	// TextDecoder with {fatal:true} rejects malformed UTF-8; Go's utf8.Valid is the
	// same acceptance rule (no replacement characters allowed through).
	if !utf8.Valid(logical) {
		a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
		return []Event{a.faultOnAssembly(assembly, ReasonAssemblyInvalidUtf8)}
	}
	var value json.RawMessage
	if !json.Valid(logical) {
		a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
		return []Event{a.faultOnAssembly(assembly, ReasonAssemblyInvalidJson)}
	}
	value = logical
	if !frameMatchesEnvelope(value, assembly.topic, assembly.subscriptionID) {
		a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
		return []Event{a.faultOnAssembly(assembly, ReasonAssemblyMetadataMismatch)}
	}
	parsed, ok := a.schema(value)
	if !ok {
		a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
		return []Event{a.faultOnAssembly(assembly, ReasonAssemblyInvalidPayload)}
	}
	a.settle(key, assembly.logicalFrameID, assembly.logicalFrameOrdinal)
	return []Event{{Frame: parsed, DeliveryKind: assembly.deliveryKind}}
}

// fragmentMetaConsistent ports the integer/range/checksum-format guard that runs
// after the per-kind schema pass and before base64 decode.
func fragmentMetaConsistent(wire Candidate, algorithm, checksumValue string) bool {
	count, index, logicalBytes := wire.FragmentCount, wire.FragmentIndex, wire.LogicalBytes
	if count < 1 || index < 0 || index >= count || logicalBytes < 1 ||
		count > logicalBytes || algorithm != "crc32" || !isHex8(checksumValue) {
		return false
	}
	return true
}

func isHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// frameMatchesEnvelope ports frameMatchesEnvelope: the reassembled logical frame
// must carry the same topic and subscriptionId as its wire header.
func frameMatchesEnvelope(frame []byte, topic, subscriptionID string) bool {
	var value struct {
		Topic          *string `json:"topic"`
		SubscriptionID *string `json:"subscriptionId"`
	}
	if err := json.Unmarshal(frame, &value); err != nil {
		return false
	}
	return value.Topic != nil && value.SubscriptionID != nil &&
		*value.Topic == topic && *value.SubscriptionID == subscriptionID
}
