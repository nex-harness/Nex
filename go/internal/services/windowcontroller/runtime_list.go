package windowcontroller

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

// ListTaskList answers a list query across every scope that resolves.
//
// Two paths, exactly as the TS has them:
//
//   - WITH search: each online source runs its own indexed search and the
//     controller enriches every hit with the live/projected facts it already
//     holds. The search bypasses the membership filter because the reader already
//     applied the bucket; the controller only adds the source-relative fields.
//   - WITHOUT search: the answer is the PROJECTION, filtered by membership. This
//     is the whole point of the frozen snapshot — a source that is momentarily
//     unreadable still shows its last trusted rows rather than vanishing.
func (rt *Runtime) ListTaskList(ctx context.Context, query ListQuery) (ListResult, error) {
	sources := rt.resolveQuerySources(query.WorkspaceScopes)

	// Refresh every resolved source first, in parallel, tolerating per-source
	// failure: one source's read error must not empty another's projection.
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func(s *ResolvedSource) {
			defer wg.Done()
			if err := rt.refreshSource(ctx, s, false); err != nil {
				rt.report(s.Scope, "refresh", err)
			}
		}(src)
	}
	wg.Wait()

	if search := strings.TrimSpace(query.Search); search != "" {
		return rt.searchList(ctx, query, sources, search)
	}
	return rt.projectedList(query, sources), nil
}

// searchList runs the per-source search path.
func (rt *Runtime) searchList(ctx context.Context, query ListQuery, sources []*ResolvedSource, search string) (ListResult, error) {
	items := make([]ListItem, 0, 16)
	for _, src := range sources {
		if src.Availability != AvailabilityOnline || src.Reader == nil {
			continue
		}
		scoped := query
		scoped.WorkspaceScopes = []SourceScope{src.Scope}
		scoped.Search = search
		// The reader applies the bucket and the limit; the controller enriches.
		scoped.Limit = nil
		result, err := src.Reader.SearchTasks(ctx, scoped, src.Scope)
		if err != nil {
			rt.report(src.Scope, "search", err)
			continue
		}
		for _, hit := range result.Items {
			if hit.Meta == nil {
				continue
			}
			meta := *hit.Meta
			meta.WorkspacePath = src.Scope.WorkspacePath
			if src.Scope.WorkspaceIdentity != "" {
				identity := src.Scope.WorkspaceIdentity
				meta.WorkspaceIdentity = &identity
			} else {
				meta.WorkspaceIdentity = nil
			}
			item := ListItem{
				TaskMeta:           meta,
				SourceAvailability: AvailabilityOnline,
				LiveStatus:         defaultLiveStatus(&meta),
				SearchSnippets:     hit.SearchSnippets,
			}
			if src.Scope.Kind == KindRemote {
				item.RemoteSessionID = src.Scope.RemoteSessionID
			}
			if row := rt.projection.findRow(AddressOf(src.Scope, meta.TaskID)); row != nil {
				item.TaskMeta = row.Meta
				item.LiveStatus = row.LiveStatus
				item.Activity = row.Activity
			}
			items = append(items, item)
		}
	}
	return finishList(items, query), nil
}

// projectedList answers from the projection, filtered by membership.
func (rt *Runtime) projectedList(query ListQuery, sources []*ResolvedSource) ListResult {
	selected := make(map[string]struct{}, len(sources))
	for _, src := range sources {
		selected[SourceKey(src.Scope)] = struct{}{}
	}
	rows := rt.projection.allRows()
	items := make([]ListItem, 0, len(rows))
	for _, row := range rows {
		if _, ok := selected[SourceKey(row.Address.Scope())]; !ok {
			continue
		}
		if !MatchesMembershipKind(row.Membership, query.Kind) {
			continue
		}
		item := ListItem{
			TaskMeta:           row.Meta,
			RemoteSessionID:    row.Address.RemoteSessionID,
			SourceAvailability: row.SourceAvailability,
			LiveStatus:         row.LiveStatus,
			Activity:           row.Activity,
			SearchSnippets:     row.SearchSnippets,
		}
		items = append(items, item)
	}
	return finishList(items, query)
}

// resolveQuerySources resolves the query's scopes, de-duplicated by source key and
// keeping first-seen order.
func (rt *Runtime) resolveQuerySources(scopes []SourceScope) []*ResolvedSource {
	seen := map[string]struct{}{}
	out := make([]*ResolvedSource, 0, len(scopes))
	for _, scope := range scopes {
		resolved := rt.resolveSource(scope)
		if resolved == nil {
			continue
		}
		key := SourceKey(resolved.Scope)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, resolved)
	}
	return out
}

// finishList sorts, counts and applies the limit. The sort is by the requested
// time field descending with the task id ascending as the tiebreak, so the order
// is total and reproducible.
func finishList(items []ListItem, query ListQuery) ListResult {
	sortBy := query.SortBy
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		leftAt, rightAt := left.UpdatedAt, right.UpdatedAt
		if sortBy == "created" {
			leftAt, rightAt = left.CreatedAt, right.CreatedAt
		}
		if leftAt != rightAt {
			return leftAt > rightAt
		}
		return left.TaskID < right.TaskID
	})
	total := len(items)
	if query.Limit != nil && *query.Limit >= 0 && *query.Limit < total {
		items = items[:*query.Limit]
	}
	return ListResult{Items: items, Total: total, HasMore: total > len(items)}
}
