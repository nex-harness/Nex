package windowcontroller

import (
	"sort"

	"github.com/tylinux/nex/go/internal/adapters/taskindex"
)

// projection.go owns the per-source membership + overlay state and turns it into
// rows. It holds the three invariants that make the wire consistent:
//
//   - A row's address and meta agree on identity by construction: the row is
//     built with AddressOf(scope, meta.TaskID) and normalizeMeta pins the meta's
//     path/identity to the scope, so windowHostControllerTaskRowSchema's
//     identity refinement can never be violated by the producer.
//   - defaultLiveStatus never returns running/waiting. The absence of an overlay
//     means there is no PROVABLE runtime right now; a running/interaction status
//     read out of SQLite may be from the previous process exit, so it must not be
//     re-interpreted as a live fact. Only the sessions-index overlay may assert
//     running/waiting.
//   - statusFromActivity lets an overlay's phase OVERRIDE the persisted meta
//     status, which is how a session that finished in the agent but whose index
//     write has not landed yet still projects as completed.

type taskMembership struct {
	meta           *taskindex.TaskMeta
	membership     Membership
	searchSnippets []string
}

type sourceState struct {
	scope           SourceScope
	availability    string
	connectionState string
	memberships     map[string]taskMembership
	overlays        map[string]SessionOverlay
	rows            map[string]TaskRow
}

// taskMeta is the meta the projection stores. It is a pointer so "a row exists"
// and "the row carries a meta" are the same statement: the controller never
// synthesizes a row without a meta.
type taskMeta = taskindex.TaskMeta

// defaultLiveStatus ports defaultLiveStatus: completed/error from the persisted
// status, everything else idle. See the package rule above.
func defaultLiveStatus(meta *taskindex.TaskMeta) string {
	if meta == nil {
		return LiveIdle
	}
	if meta.Status != nil {
		switch *meta.Status {
		case "completed":
			return LiveCompleted
		case "error":
			return LiveError
		}
	}
	return LiveIdle
}

// statusFromActivity ports statusFromActivity: the phase an overlay asserts wins
// over the persisted meta status. "draft" asserts nothing, so the meta status is
// kept (a draft session is not a live running fact).
func statusFromActivity(activity *TaskActivity) *string {
	if activity == nil {
		return nil
	}
	var status string
	switch activity.Phase {
	case "prewarming", "running":
		status = "running"
	case "completedSuccess", "completedInterrupted":
		status = "completed"
	case "error":
		status = "error"
	case "draft":
		return nil
	default:
		return nil
	}
	return &status
}

// buildRows recomputes a source's rows from its memberships + overlays. It is a
// pure function of the two maps, which is what makes replaceSourceSessionOverlays
// (overlays only) and replaceSourceSnapshot (both) share one code path and one
// comparison.
func buildRows(src *sourceState) map[string]TaskRow {
	rows := make(map[string]TaskRow, len(src.memberships))
	for _, membership := range src.memberships {
		if membership.meta == nil {
			continue
		}
		overlay, hasOverlay := src.overlays[membership.meta.TaskID]
		meta := *membership.meta
		if hasOverlay {
			// An overlay title replaces the persisted title only when the user did
			// not override it, OR the overlay itself carries a custom title (which
			// IS a user override expressed by the agent). This mirrors
			// shouldUseOverlayTitle.
			useOverlayTitle := overlay.HasTitle &&
				(!meta.TitleOverridden || overlay.TitleSource == "custom")
			if useOverlayTitle {
				meta.Title = overlay.Title
			}
			if overlay.TitleSource == "custom" {
				meta.TitleOverridden = true
			}
			if overlay.UpdatedAt != nil {
				meta.UpdatedAt = *overlay.UpdatedAt
			}
			if status := statusFromActivity(overlay.Activity); status != nil {
				meta.Status = status
			}
		}
		addr := AddressOf(src.scope, meta.TaskID)
		// Without an overlay the row falls back to the PERSISTED status, which can
		// only ever be completed/error/idle — never a live running or waiting fact.
		// With an overlay, the overlay is the authority (it may also say idle).
		live := defaultLiveStatus(&meta)
		if hasOverlay && overlay.LiveStatus != "" {
			live = overlay.LiveStatus
		}
		row := TaskRow{
			Address:            addr,
			Meta:               meta,
			Membership:         membership.membership,
			SourceAvailability: src.availability,
			LiveStatus:         live,
			SearchSnippets:     membership.searchSnippets,
		}
		if hasOverlay && overlay.Activity != nil {
			row.Activity = overlay.Activity
		}
		rows[AddressTaskKey(addr)] = row
	}
	return rows
}

// allRows flattens every source's rows into the deterministic snapshot order:
// updatedAt descending, then the task key ascending as the tiebreak. The sort
// must be total and stable across processes, so the task key (not the map
// iteration order) breaks the tie.
func allRows(sources map[string]*sourceState) []TaskRow {
	out := make([]TaskRow, 0, 8)
	for _, src := range sources {
		for _, row := range src.rows {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Meta.UpdatedAt != out[j].Meta.UpdatedAt {
			return out[i].Meta.UpdatedAt > out[j].Meta.UpdatedAt
		}
		return AddressTaskKey(out[i].Address) < AddressTaskKey(out[j].Address)
	})
	return out
}

// taskRowEqual is the isDeepStrictEqual on a TaskRow the delta computation needs.
// It compares the fields the wire exposes. A membership, availability, status,
// title or activity change is an upsert; anything else is not.
func taskRowEqual(left, right TaskRow) bool {
	if left.SourceAvailability != right.SourceAvailability ||
		left.LiveStatus != right.LiveStatus ||
		left.Membership != right.Membership {
		return false
	}
	if !addressEqual(left.Address, right.Address) {
		return false
	}
	if !metaEqual(&left.Meta, &right.Meta) {
		return false
	}
	if (left.Activity == nil) != (right.Activity == nil) {
		return false
	}
	if left.Activity != nil && !activityEqual(*left.Activity, *right.Activity) {
		return false
	}
	return snippetsEqual(left.SearchSnippets, right.SearchSnippets)
}

func addressEqual(a, b TaskAddress) bool {
	return a == b
}

func activityEqual(a, b TaskActivity) bool {
	return a.Phase == b.Phase && a.LastActivityAt == b.LastActivityAt &&
		a.HasBackgroundWork == b.HasBackgroundWork &&
		jsonEqual(a.PendingInteract, b.PendingInteract) &&
		jsonEqual(a.WorkflowActivity, b.WorkflowActivity)
}

func snippetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
