package v4pub

import (
	"encoding/json"
	"fmt"
)

// ParseDelta decodes one ConversationDelta from its JSON text. The raw bytes are
// kept verbatim for emission; the typed fields are only those the coalescing
// rules read.
func ParseDelta(raw json.RawMessage) (Delta, error) {
	var envelope struct {
		Op       string          `json:"op"`
		RowID    *int64          `json:"rowId"`
		FromRow  *int64          `json:"fromRowId"`
		Path     string          `json:"path"`
		Append   string          `json:"append"`
		RunID    string          `json:"runId"`
		Revision *int64          `json:"revision"`
		Row      json.RawMessage `json:"row"`
		Patch    json.RawMessage `json:"patch"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Delta{}, err
	}
	d := Delta{Op: DeltaOp(envelope.Op), Path: envelope.Path, Append: envelope.Append,
		RunID: envelope.RunID, Raw: append(json.RawMessage(nil), raw...)}
	switch d.Op {
	case OpRowAppended, OpRowUpserted:
		var row struct {
			RowID *int64 `json:"rowId"`
		}
		if err := json.Unmarshal(envelope.Row, &row); err != nil {
			return Delta{}, err
		}
		if row.RowID == nil {
			return Delta{}, fmt.Errorf("v4pub: %s row has no rowId", d.Op)
		}
		d.RowID = *row.RowID
	case OpRowRemoved:
		if envelope.FromRow == nil {
			return Delta{}, fmt.Errorf("v4pub: row.removed has no fromRowId")
		}
		d.FromRow = *envelope.FromRow
	case OpRowDelta:
		if envelope.RowID == nil {
			return Delta{}, fmt.Errorf("v4pub: row.delta has no rowId")
		}
		d.RowID = *envelope.RowID
	case OpStateUpdated:
		var patch struct {
			Revision     *int64          `json:"revision"`
			WorkflowRuns json.RawMessage `json:"workflowRuns"`
		}
		if err := json.Unmarshal(envelope.Patch, &patch); err != nil {
			return Delta{}, err
		}
		d.PatchRevision = patch.Revision
		if len(patch.WorkflowRuns) > 0 {
			d.HasWorkflowRuns = true
			d.WorkflowRuns = patch.WorkflowRuns
		}
	case OpWorkflowRunUpdated:
		if envelope.Revision == nil {
			return Delta{}, fmt.Errorf("v4pub: workflowRun.updated has no revision")
		}
		d.Revision = *envelope.Revision
		var wu struct {
			Run           json.RawMessage   `json:"run"`
			Cleared       []string          `json:"cleared"`
			RemovedActors []json.RawMessage `json:"removedActors"`
			RemovedNodes  []json.RawMessage `json:"removedNodes"`
			Actors        []json.RawMessage `json:"actors"`
			Nodes         []json.RawMessage `json:"nodes"`
		}
		if err := json.Unmarshal(raw, &wu); err != nil {
			return Delta{}, err
		}
		d.Run = wu.Run
		d.Cleared = wu.Cleared
		d.RemovedActors = parseEntryRefs(wu.RemovedActors)
		d.RemovedNodes = parseEntryRefs(wu.RemovedNodes)
		d.Actors = wu.Actors
		d.Nodes = wu.Nodes
	case OpWorkflowRunRemoved:
		if envelope.Revision == nil {
			return Delta{}, fmt.Errorf("v4pub: workflowRun.removed has no revision")
		}
		d.Revision = *envelope.Revision
	default:
		return Delta{}, fmt.Errorf("v4pub: unknown delta op %q", envelope.Op)
	}
	return d, nil
}

func parseEntryRefs(raw []json.RawMessage) []RunEntryRef {
	if len(raw) == 0 {
		return nil
	}
	out := make([]RunEntryRef, 0, len(raw))
	for _, r := range raw {
		var e struct {
			SiteID  string `json:"siteId"`
			Ordinal int64  `json:"ordinal"`
		}
		if err := json.Unmarshal(r, &e); err != nil {
			continue
		}
		out = append(out, RunEntryRef{SiteID: e.SiteID, Ordinal: e.Ordinal})
	}
	return out
}
