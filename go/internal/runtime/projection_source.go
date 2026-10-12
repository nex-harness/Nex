package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex/go/internal/projection"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// projectionSource adapts *projection.Projection to v4pub.SnapshotSource. The
// projection's public API returns ordered deltas as *obj; the publisher wants
// []v4pub.Delta, so this adapter serialises each delta and re-parses it. The
// projection carries the transport sequence number in event.SequenceNumber, so
// Seq() reflects whatever the runtime fed it.
type projectionSource struct {
	p       *projection.Projection
	lastSeq int64
}

func newProjectionSource(p *projection.Projection) *projectionSource {
	return &projectionSource{p: p}
}

func (s *projectionSource) Seq() int64 { return s.lastSeq }

func (s *projectionSource) SnapshotRaw() json.RawMessage {
	raw, err := s.p.Snapshot()
	if err != nil {
		// A snapshot serialisation failure would corrupt the topic; surface it by
		// returning an empty object (the publisher only measures/emits it).
		return json.RawMessage(`{}`)
	}
	return raw
}

func (s *projectionSource) WindowRowCount() int {
	raw, err := s.p.Snapshot()
	if err != nil {
		return 0
	}
	var snap struct {
		Rows struct {
			Window []json.RawMessage `json:"window"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return 0
	}
	return len(snap.Rows.Window)
}

// ApplyIngest decodes one transport event and applies it, returning the deltas.
func (s *projectionSource) ApplyIngest(event json.RawMessage) ([]v4pub.Delta, error) {
	ev, err := projection.ParseEvent(event)
	if err != nil {
		return nil, fmt.Errorf("projection source: %w", err)
	}
	s.lastSeq = ev.SequenceNumber
	deltas, err := s.p.Apply(ev)
	if err != nil {
		return nil, err
	}
	out := make([]v4pub.Delta, 0, len(deltas))
	for _, d := range deltas {
		raw, err := d.MarshalJSON()
		if err != nil {
			return nil, err
		}
		parsed, err := v4pub.ParseDelta(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

// WorkflowRunsRaw is nil: the projection does not model workflow runs in the
// ported scenarios.
func (s *projectionSource) WorkflowRunsRaw() json.RawMessage { return nil }
