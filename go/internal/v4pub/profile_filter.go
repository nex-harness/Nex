package v4pub

// Delivery profile filtering: the first step of the channel flush pipeline
// (filter -> coalesce -> frame). Recovery replay runs the same functions, so a
// reconnect re-delivers deterministically.
//
// Closure invariant (pinned by a golden test): every event filtered out by a
// profile must eventually be closed out by an unfilterable event. A row.delta
// for inputText that is filtered away must be followed by a row.upserted that
// carries the full inputText when the tool input is finalised, or replayable
// subscribers end up missing data.

// FilterConversationRowsForProfile returns the rows visible under the profile.
// HookInvocationRow converged into a client-safe summary shared by desktop and
// mobile, so there is currently no profile-specific row; the boundary stays for
// future controlled rows.
func FilterConversationRowsForProfile(rows []JSONValue, _ DeliveryProfile) []JSONValue {
	return append([]JSONValue{}, rows...)
}

// FilterConversationDeltasForProfile filters a delta sequence by the profile's
// streaming-path switches. row.delta is filtered by streaming capability;
// structural whole rows are the same product fact in both profiles.
func FilterConversationDeltasForProfile(deltas []Delta, profile DeliveryProfile) []Delta {
	out := make([]Delta, 0, len(deltas))
	for _, d := range deltas {
		if d.Op == OpRowDelta && !profile.streamPath(d.Path) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (p DeliveryProfile) streamPath(path string) bool {
	switch path {
	case "text":
		return p.Text
	case "inputText":
		return p.InputText
	case "output.text":
		return p.OutputText
	case "summaryText":
		return p.SummaryText
	}
	return false
}
