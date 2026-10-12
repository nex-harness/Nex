package v4pub

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Delta is one entry of the closed ConversationDelta union. The publisher
// carries the payload as opaque JSON and only interprets the fields that the
// coalescing rules constrain: the op discriminator, rowId/fromRowId/path,
// runId/revision, and the state patch's workflowRuns and revision keys.
type Delta struct {
	Op      DeltaOp
	RowID   int64  // row.appended/row.upserted: row.rowId; row.delta: rowId
	FromRow int64  // row.removed: fromRowId
	Path    string // row.delta: path
	Append  string // row.delta: append
	RunID   string // workflowRun.updated/removed: runId
	// Revision is workflowRun.updated/removed: revision (absolute watermark).
	Revision int64
	// PatchRevision is state.updated: patch.revision when present.
	PatchRevision *int64
	// HasWorkflowRuns is state.updated: patch.workflowRuns is present.
	HasWorkflowRuns bool
	// WorkflowRuns is state.updated: patch.workflowRuns verbatim.
	WorkflowRuns json.RawMessage

	// WU/Header/WU fields for workflowRun.updated (opaque where noted).
	Run           json.RawMessage // header patch (run key)
	Cleared       []string        // header keys that became absent
	RemovedActors []RunEntryRef
	RemovedNodes  []RunEntryRef
	Actors        []json.RawMessage
	Nodes         []json.RawMessage

	// Raw is the whole delta exactly as JSON.stringify would render it; frames
	// emit this verbatim.
	Raw json.RawMessage
}

// DeltaOp is the ConversationDelta discriminator.
type DeltaOp string

// The seven ops of the closed union.
const (
	OpRowAppended        DeltaOp = "row.appended"
	OpRowUpserted        DeltaOp = "row.upserted"
	OpRowRemoved         DeltaOp = "row.removed"
	OpRowDelta           DeltaOp = "row.delta"
	OpStateUpdated       DeltaOp = "state.updated"
	OpWorkflowRunUpdated DeltaOp = "workflowRun.updated"
	OpWorkflowRunRemoved DeltaOp = "workflowRun.removed"
)

// RunEntryRef identifies an evicted actor/node entry: (siteId, ordinal).
type RunEntryRef struct {
	SiteID  string
	Ordinal int64
}

// Key is workflowRunEntryKey: siteId + "\0" + ordinal, so ("a",12) and ("a1",2)
// cannot collide.
func (r RunEntryRef) Key() string {
	return fmt.Sprintf("%s\x00%d", r.SiteID, r.Ordinal)
}

// MarshalJSON emits the delta verbatim.
func (d Delta) MarshalJSON() ([]byte, error) { return append([]byte(nil), d.Raw...), nil }

// Clone returns a shallow copy of the delta.
func (d Delta) Clone() Delta { return d }

// rawObject is an ordered JSON object with raw member values.
type rawObject = orderedRawObject

func decodeRawObject(raw json.RawMessage) (*orderedRawObject, error) {
	o := newOrderedRawObject()
	if len(raw) == 0 || string(raw) == "null" {
		return o, nil
	}
	if err := json.Unmarshal(raw, &o.values); err != nil {
		return o, err
	}
	o.keys = objectKeyOrder(raw)
	return o, nil
}

// objectKeyOrder returns the top-level object keys in the order they appear in
// the raw JSON text.
func objectKeyOrder(raw []byte) []string {
	var keys []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // '{'
		return nil
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			break
		}
		if k, ok := keyTok.(string); ok {
			keys = append(keys, k)
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			break
		}
	}
	return keys
}
