package projection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Event is the projection input: one authoritative SessionEvent from the CLI
// event log (apps/nex-cli/packages/contracts/src/events/session.events.ts).
//
// The sequenceNumber is an INPUT to the projection — another component allocates
// it. Row createdAtSeq and snapshot seq echo it. The TypeScript projection reads
// ts via `event.timestamp.getTime()`; we carry Unix milliseconds and set a UTC
// time for parity.
type Event struct {
	ID             string
	SessionID      string
	TurnID         string
	HasTurnID      bool
	Type           string
	TimestampMs    int64
	TraceID        string
	SequenceNumber int64
	Payload        json.RawMessage
}

// Time returns the event timestamp as a time.Time (UTC), matching the TS Date.
func (e Event) Time() time.Time {
	return time.UnixMilli(e.TimestampMs).UTC()
}

// eventWire is the JSON shape accepted by LoadEvents, used by tests and by any
// adapter that reads recorded events.
type eventWire struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"sessionId"`
	TurnID         *string         `json:"turnId"`
	Type           string          `json:"type"`
	TimestampMs    int64           `json:"timestampMs"`
	TraceID        string          `json:"traceId"`
	SequenceNumber int64           `json:"sequenceNumber"`
	Payload        json.RawMessage `json:"payload"`
}

// ParseEvent decodes one event from its wire JSON. Passing a raw event keeps the
// projection free of any storage concern; adapters own decoding.
func ParseEvent(data []byte) (Event, error) {
	var w eventWire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&w); err != nil {
		return Event{}, fmt.Errorf("projection: decode event: %w", err)
	}
	ev := Event{
		ID:             w.ID,
		SessionID:      w.SessionID,
		Type:           w.Type,
		TimestampMs:    w.TimestampMs,
		TraceID:        w.TraceID,
		SequenceNumber: w.SequenceNumber,
		Payload:        w.Payload,
	}
	if w.TurnID != nil {
		ev.TurnID = *w.TurnID
		ev.HasTurnID = true
	}
	return ev, nil
}

// payloadMap decodes the event payload into a generic map for field access. The
// projection reads a small, fixed set of scalar fields; unmodeled subtrees stay
// as json.RawMessage.
func (e Event) payloadMap() (map[string]json.RawMessage, error) {
	if len(e.Payload) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		return nil, fmt.Errorf("projection: decode payload: %w", err)
	}
	return m, nil
}

// payloadString reads a string field; absent or null yields ("", false). A field
// that is present but not a string is treated as absent, matching the TS
// `typeof x === "string"` guards.
func payloadString(m map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := m[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func payloadBool(m map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := m[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

func payloadInt(m map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := m[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}

// bytesReader is a tiny alias kept local so json decoding does not pull bytes
// across the package surface.
func bytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }
