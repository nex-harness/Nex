// wirecandidate.go parses one physical v4 wire frame into the candidate shape
// TopicWireFrameAssembler accepts. It is deliberately separate from the assembler
// state machine: the parsing rules (which keys exist, which are numbers) are the
// topicWireFrameCandidateSchema contract, and the assembler's rules are about
// ordinals, budgets and release. Mixing them is what produced the TS file's
// "passthrough, service boundary validates only what routing needs" behaviour in
// the first place.
//
// The outer parse is deliberately permissive: the service boundary only routes on
// (topic, subscriptionId) and must not drop a frame that carries an invalid
// deliveryKind, a broken checksum or an unknown key — those become typed faults
// from the owned assembler. A frame that cannot be routed at all (no topic) is the
// one thing it rejects.
package proto

import (
	"encoding/json"
	"strings"

	"github.com/tylinux/nex/go/internal/protocol"
)

// Candidate is one physical frame. Every field is "as sent": the assembler, not
// the parser, decides what a wrong value means.
type Candidate struct {
	Kind                string
	DeliveryKind        string // raw; "" when absent, invalid values kept as-is
	HasDeliveryKind     bool
	LogicalFrameID      string
	LogicalFrameOrdinal int64
	// OrdinalIsNumber records that logicalFrameOrdinal parsed as a JSON number,
	// which Number.isSafeInteger in the TS requires.
	OrdinalIsNumber bool
	Topic           string
	SubscriptionID  string

	// Fragment-only payload.
	FragmentIndex    int64
	FragmentCount    int64
	LogicalBytes     int64
	ChecksumRaw      json.RawMessage
	DataBase64       string
	HasFragmentIndex bool
	HasFragmentCount bool
	HasLogicalBytes  bool
	HasDataBase64    bool

	// Complete-only payload.
	Frame    json.RawMessage
	HasFrame bool

	// KeysExtra is every key outside the kind's allowed set. It is the
	// only-keys rule from the TS assembler: one extra key is a metadata mismatch,
	// not something to ignore, and the order does not matter because the check is
	// a membership test.
	KeysExtra []string
	// rawJSON is the exact bytes received. Measurement runs on these, never on a
	// re-serialisation, because both the byte count and the JSON escapes are part
	// of what the physical frame budget is defined over.
	rawJSON []byte
}

// completeWireKeys / fragmentWireKeys mirror COMPLETE_WIRE_KEYS and
// FRAGMENT_WIRE_KEYS in wire-assembler.ts. A key outside the set is a metadata
// mismatch, not something to ignore.
var completeWireKeys = keySet(
	"wireVersion", "kind", "deliveryKind", "logicalFrameId", "logicalFrameOrdinal",
	"topic", "subscriptionId", "frame",
)

var fragmentWireKeys = keySet(
	"wireVersion", "kind", "deliveryKind", "logicalFrameId", "logicalFrameOrdinal",
	"topic", "subscriptionId", "fragmentIndex", "fragmentCount", "logicalBytes",
	"checksum", "dataBase64",
)

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// ParseCandidate decodes one wire frame JSON. The returned error is only for a
// frame that is not a JSON object at all — every other defect (a bad
// deliveryKind, an unknown key, a non-numeric ordinal) is returned with the
// offending values intact, so the ASSEMBLER can raise the typed fault the
// consumer routes on. Dropping it here would make the store wait forever on a
// frame that can never arrive, which is exactly what the service-boundary
// candidate schema in the TS is designed to avoid.
func ParseCandidate(raw []byte) (Candidate, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return Candidate{}, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return Candidate{}, errNotWireObject
	}
	c := Candidate{rawJSON: raw}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return Candidate{}, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return Candidate{}, errNotWireObject
		}
		switch key {
		case "kind":
			c.Kind = readString(dec)
		case "deliveryKind":
			c.DeliveryKind, c.HasDeliveryKind = readOptionalString(dec)
		case "logicalFrameId":
			c.LogicalFrameID = readString(dec)
		case "logicalFrameOrdinal":
			c.LogicalFrameOrdinal, c.OrdinalIsNumber = readOptionalInt(dec)
		case "topic":
			c.Topic = readString(dec)
		case "subscriptionId":
			c.SubscriptionID = readString(dec)
		case "frame":
			c.Frame, c.HasFrame, err = readOptionalRaw(dec)
			if err != nil {
				return Candidate{}, err
			}
		case "fragmentIndex":
			c.FragmentIndex, c.HasFragmentIndex = readOptionalInt(dec)
		case "fragmentCount":
			c.FragmentCount, c.HasFragmentCount = readOptionalInt(dec)
		case "logicalBytes":
			c.LogicalBytes, c.HasLogicalBytes = readOptionalInt(dec)
		case "checksum":
			c.ChecksumRaw, _, err = readOptionalRaw(dec)
			if err != nil {
				return Candidate{}, err
			}
		case "dataBase64":
			c.DataBase64, c.HasDataBase64 = readOptionalString(dec)
		default:
			// Unknown key: drain its value so the decode stays aligned. The key
			// itself is recorded so the assembler can raise the metadata fault.
			c.KeysExtra = append(c.KeysExtra, key)
			if err := drainValue(dec); err != nil {
				return Candidate{}, err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return Candidate{}, err
	}
	allowed := completeWireKeys
	if c.Kind == "fragment" {
		allowed = fragmentWireKeys
	}
	for _, k := range c.KeysExtra {
		if allowed[k] {
			c.KeysExtra = removeString(c.KeysExtra, k)
		}
	}
	return c, nil
}

type wireParseError string

func (e wireParseError) Error() string { return string(e) }

const errNotWireObject = wireParseError("wire frame is not a JSON object")

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// readString reads any JSON value and returns it when it is a string, "" otherwise.
// A non-string here is exactly what the assembler's metadata fault reports.
func readString(dec *json.Decoder) string {
	s, _ := readOptionalString(dec)
	return s
}

func readOptionalString(dec *json.Decoder) (string, bool) {
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// readOptionalInt returns the value only when it is a JSON integer; a float with
// a fraction or a non-number yields ok=false, matching the TS typeof checks.
func readOptionalInt(dec *json.Decoder) (int64, bool) {
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return i, true
}

func readOptionalRaw(dec *json.Decoder) (json.RawMessage, bool, error) {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func drainValue(dec *json.Decoder) error {
	var v json.RawMessage
	return dec.Decode(&v)
}

// validateFragmentInner ports hasValidFragmentInner: the fragment branch of the
// wire schema, which the assembler runs before it will even measure the frame.
func (c Candidate) validateFragmentInner() bool {
	if len(c.KeysExtra) > 0 ||
		!c.HasFragmentIndex || !c.HasFragmentCount || !c.HasLogicalBytes || !c.HasDataBase64 {
		return false
	}
	if len(c.ChecksumRaw) == 0 {
		return false
	}
	var checksum map[string]json.RawMessage
	if err := json.Unmarshal(c.ChecksumRaw, &checksum); err != nil || checksum == nil {
		return false
	}
	checksumKeys := keySet("algorithm", "value")
	for k := range checksum {
		if !checksumKeys[k] {
			return false
		}
	}
	var algorithm, value string
	if err := json.Unmarshal(checksum["algorithm"], &algorithm); err != nil {
		return false
	}
	if err := json.Unmarshal(checksum["value"], &value); err != nil {
		return false
	}
	return true
}

// checksumFields returns the algorithm and value of a validated fragment.
func (c Candidate) checksumFields() (string, string) {
	var checksum struct {
		Algorithm string `json:"algorithm"`
		Value     string `json:"value"`
	}
	_ = json.Unmarshal(c.ChecksumRaw, &checksum)
	return checksum.Algorithm, checksum.Value
}

// measuredBytes is measureTopicNotificationEnvelopeBytes(wire).maxBytes: the
// length of the wire JSON as it would travel, used against maxPhysicalFrameBytes.
func (c Candidate) measuredBytes() int {
	return protocol.MeasureEnvelope(len(c.rawJSON)).MaxBytes
}

// raw is the JSON text of the candidate. ParseCandidate keeps it so measurement is
// on the exact bytes received rather than a re-serialisation, which would change
// both the byte count and the escapes.
func (c Candidate) raw() []byte { return c.rawJSON }

// deliveryKindValid ports parseDeliveryKind.
func deliveryKindValid(v string, has bool) (protocol.DeliveryKind, bool) {
	if !has {
		return "", false
	}
	switch v {
	case "initial":
		return protocol.DeliveryInitial, true
	case "online":
		return protocol.DeliveryOnline, true
	case "recovery":
		return protocol.DeliveryRecovery, true
	}
	return "", false
}
