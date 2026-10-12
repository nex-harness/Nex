package protocol

import (
	"fmt"
	"strconv"
)

// EncodeError carries the reason codes of TopicWireFrameEncodingError. Callers
// and tests compare Reason, which are the same strings the host and UI read.
type EncodeError struct{ Reason string }

func (e *EncodeError) Error() string { return e.Reason }

const (
	ReasonFrameAssemblyTooLarge     = "proto.frameAssemblyTooLarge"
	ReasonFrameEnvelopeTooLarge     = "proto.frameEnvelopeTooLarge"
	ReasonFrameFragmentCountExceed  = "proto.frameFragmentCountExceeded"
	reasonInvalidLimitPhysicalFrame = "proto.invalidLimit.maxPhysicalFrameBytes"
	reasonInvalidLimitAssembly      = "proto.invalidLimit.maxAssemblyBytes"
)

// DeliveryKind is the reservation purpose of a physical frame.
type DeliveryKind string

const (
	DeliveryInitial  DeliveryKind = "initial"
	DeliveryOnline   DeliveryKind = "online"
	DeliveryRecovery DeliveryKind = "recovery"
)

// EncodeOptions mirrors EncodeTopicWireFramesOptions. Zero limits mean "use the
// protocol maximum" (TypeScript uses undefined for that; an explicit 0 there is
// an error, here it is simply the default, which is the only deliberate
// difference). Negative limits are rejected.
type EncodeOptions struct {
	DeliveryKind          DeliveryKind
	Topic                 string
	SubscriptionID        string
	LogicalFrameID        string
	LogicalFrameOrdinal   int64
	MaxPhysicalFrameBytes int
	MaxAssemblyBytes      int
}

// Wire is one physical frame: compact JSON ready to be used as the params of a
// v4/conversation/frame notification.
type Wire struct {
	Fragment bool
	JSON     []byte
}

func hardBound(v, maximum int, reason string) (int, error) {
	if v < 0 {
		return 0, &EncodeError{Reason: reason}
	}
	if v == 0 || v > maximum {
		return maximum, nil
	}
	return v, nil
}

// head renders the fields every wire frame starts with, in the key order the
// TypeScript object literals use (JSON.stringify keeps insertion order).
func (o EncodeOptions) head(kind string) []byte {
	b := make([]byte, 0, 160)
	b = append(b, `{"wireVersion":`...)
	b = strconv.AppendInt(b, V4WireProtocolVersion, 10)
	b = append(b, `,"kind":`...)
	b = appendJSString(b, kind)
	b = append(b, `,"deliveryKind":`...)
	b = appendJSString(b, string(o.DeliveryKind))
	b = append(b, `,"logicalFrameId":`...)
	b = appendJSString(b, o.LogicalFrameID)
	b = append(b, `,"logicalFrameOrdinal":`...)
	b = strconv.AppendInt(b, o.LogicalFrameOrdinal, 10)
	b = append(b, `,"topic":`...)
	b = appendJSString(b, o.Topic)
	b = append(b, `,"subscriptionId":`...)
	b = appendJSString(b, o.SubscriptionID)
	return b
}

func (o EncodeOptions) complete(frameJSON []byte) []byte {
	b := o.head("complete")
	b = append(b, `,"frame":`...)
	b = append(b, frameJSON...)
	return append(b, '}')
}

// fragmentPrefix/suffix surround the base64 data string. Splitting them lets the
// size search compute a candidate's length without allocating the data.
func (o EncodeOptions) fragmentPrefix(index, count, logicalBytes int, checksum string) []byte {
	b := o.head("fragment")
	b = append(b, `,"fragmentIndex":`...)
	b = strconv.AppendInt(b, int64(index), 10)
	b = append(b, `,"fragmentCount":`...)
	b = strconv.AppendInt(b, int64(count), 10)
	b = append(b, `,"logicalBytes":`...)
	b = strconv.AppendInt(b, int64(logicalBytes), 10)
	b = append(b, `,"checksum":{"algorithm":"crc32","value":"`...)
	b = append(b, checksum...)
	return append(b, `"},"dataBase64":"`...)
}

const fragmentSuffix = `"}`

// EncodeTopicWireFrames splits one logical frame (already serialised JSON, the
// bytes JSON.stringify would give for it) into physical frames that each fit
// the limit once wrapped in the three carriers measured by MeasureEnvelope.
// It ports encodeTopicWireFrames from wire-codec.ts.
func EncodeTopicWireFrames(frameJSON []byte, o EncodeOptions) ([]Wire, error) {
	maxPhysical, err := hardBound(o.MaxPhysicalFrameBytes, MaxFrameBytes, reasonInvalidLimitPhysicalFrame)
	if err != nil {
		return nil, err
	}
	maxAssembly, err := hardBound(o.MaxAssemblyBytes, LogicalFrameAssemblyMaxBytes, reasonInvalidLimitAssembly)
	if err != nil {
		return nil, err
	}
	if len(frameJSON) > maxAssembly {
		return nil, &EncodeError{Reason: ReasonFrameAssemblyTooLarge}
	}

	if whole := o.complete(frameJSON); MeasureEnvelope(len(whole)).MaxBytes <= maxPhysical {
		return []Wire{{JSON: whole}}, nil
	}

	checksum := CRC32Hex(frameJSON)
	logicalBytes := len(frameJSON)
	chunk := o.fragmentByteBudget(logicalBytes, checksum, maxPhysical)
	if chunk < 1 {
		return nil, &EncodeError{Reason: ReasonFrameEnvelopeTooLarge}
	}
	count := (logicalBytes + chunk - 1) / chunk
	if count > LogicalFrameAssemblyMaxFragments {
		return nil, &EncodeError{Reason: ReasonFrameFragmentCountExceed}
	}
	wires := make([]Wire, 0, count)
	for i := 0; i < count; i++ {
		start := i * chunk
		end := min(start+chunk, logicalBytes)
		b := o.fragmentPrefix(i, count, logicalBytes, checksum)
		b = append(b, EncodeBase64(frameJSON[start:end])...)
		b = append(b, fragmentSuffix...)
		if MeasureEnvelope(len(b)).MaxBytes > maxPhysical {
			// The budget search assumed the worst-case index width; exceeding the
			// limit here means the model drifted, so fail closed rather than emit.
			return nil, &EncodeError{Reason: ReasonFrameEnvelopeTooLarge}
		}
		wires = append(wires, Wire{Fragment: true, JSON: b})
	}
	return wires, nil
}

// fragmentByteBudget is findFragmentByteBudget: the largest raw chunk size whose
// worst-case fragment (index and count at their widest, data of that size) still
// fits. The data is base64, which never needs JSON escaping, so the length is
// the prefix plus the encoded size plus the suffix.
func (o EncodeOptions) fragmentByteBudget(logicalBytes int, checksum string, maxPhysical int) int {
	worst := o.fragmentPrefix(max(0, logicalBytes-1), logicalBytes, logicalBytes, checksum)
	fixed := len(worst) + len(fragmentSuffix)
	low, high, best := 1, min(logicalBytes, maxPhysical), 0
	for low <= high {
		candidate := (low + high) / 2
		wireLen := fixed + 4*((candidate+2)/3)
		if MeasureEnvelope(wireLen).MaxBytes <= maxPhysical {
			best, low = candidate, candidate+1
		} else {
			high = candidate - 1
		}
	}
	return best
}

func (w Wire) String() string { return fmt.Sprintf("wire(fragment=%v,%dB)", w.Fragment, len(w.JSON)) }
