// monotonicity.go: the review monotonicity single-arbiter, porting
// workspace-hook-review-monotonicity.ts verbatim. The "reviewFlowId /
// generation is only monotonic within one Runtime controller" rule was
// previously re-implemented in three consumers and diverged; this pure
// function is the one place that decides. New mirror consumers must call it,
// never hand-write the comparison.
package hooktrust

// ReviewIdentity ports WorkspaceHookReviewIdentity: only these three fields
// participate in monotonicity; payload fields do not.
type ReviewIdentity struct {
	ReviewFlowID  string
	Generation    int64
	InteractionID string
}

// ReviewVerdict ports WorkspaceHookReviewMonotonicityVerdict.
type ReviewVerdict string

const (
	// NoCurrent: no current authority; the candidate takes over.
	NoCurrent ReviewVerdict = "no_current"
	// SameFlowAdvance: same flow, higher generation — accept (supersede).
	SameFlowAdvance ReviewVerdict = "same_flow_advance"
	// SameFlowReplay: same flow, same generation and interaction — idempotent
	// replay, ignore.
	SameFlowReplay ReviewVerdict = "same_flow_replay"
	// SameFlowConflict: same flow and generation but different interaction —
	// a controller-monotonicity violation, ignore.
	SameFlowConflict ReviewVerdict = "same_flow_conflict"
	// SameFlowStale: same flow, lower generation — late arrival, ignore.
	SameFlowStale ReviewVerdict = "same_flow_stale"
	// CrossFlow: different flow (Runtime generation change); generations are
	// not comparable across flows — the caller decides by its own epoch
	// evidence (projection drops, prompt-turn defers, store accepts).
	CrossFlow ReviewVerdict = "cross_flow"
)

// VerdictReviewRequest ports verdictWorkspaceHookReviewRequest.
func VerdictReviewRequest(current *ReviewIdentity, candidate ReviewIdentity) ReviewVerdict {
	if current == nil {
		return NoCurrent
	}
	if candidate.ReviewFlowID != current.ReviewFlowID {
		return CrossFlow
	}
	if candidate.Generation > current.Generation {
		return SameFlowAdvance
	}
	if candidate.Generation < current.Generation {
		return SameFlowStale
	}
	if candidate.InteractionID == current.InteractionID {
		return SameFlowReplay
	}
	return SameFlowConflict
}
