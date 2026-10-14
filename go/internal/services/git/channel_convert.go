// channel_convert.go holds the rpc.Value converters for the git channel
// (step 3.3): summary, file changes, diffs and branch comparisons into the
// wire shape, plus the param-decoding helpers. Split out of channel.go to
// stay under the 400-line archcheck limit.
package git

import (
	"github.com/tylinux/nex/go/internal/rpc"
)

func summaryToValue(summary GitRepositorySummary) rpc.Value {
	watchPaths := make([]any, len(summary.AutoRefreshWatchPaths))
	for i, entry := range summary.AutoRefreshWatchPaths {
		watchPaths[i] = rpc.NewObject("path", entry.Path, "recursive", entry.Recursive)
	}
	return rpc.NewObject(
		"workspacePath", summary.WorkspacePath,
		"repoRoot", summary.RepoRoot,
		"workspaceInRepoPath", summary.WorkspaceInRepoPath,
		"autoRefreshWatchPaths", watchPaths,
		"branchName", nullableString(summary.BranchName),
		"trackingBranchName", nullableString(summary.TrackingBranchName),
		"headRefType", string(summary.HeadRefType),
		"ahead", summary.Ahead,
		"behind", summary.Behind,
		"isDirty", summary.IsDirty,
		"isGitAvailable", summary.IsGitAvailable,
		"isRepository", summary.IsRepository,
	)
}

func fileChangesToValue(changes []GitFileChange) rpc.Value {
	out := make([]any, len(changes))
	for i, change := range changes {
		obj := rpc.NewObject(
			"path", change.Path,
			"repoRelativePath", change.RepoRelativePath,
			"workspaceRelativePath", change.WorkspaceRelativePath,
			"kind", string(change.Kind),
			"section", string(change.Section),
			"added", change.Added,
			"removed", change.Removed,
			"isStaged", change.IsStaged,
			"isUntracked", change.IsUntracked,
			"isConflicted", change.IsConflicted,
		)
		// TS copies `x: entry.x ?? undefined` — undefined keys vanish in
		// JSON.stringify, so they are omitted here (insertion position between
		// workspaceRelativePath and kind is preserved by Set).
		if change.X != "" {
			obj.Set("x", change.X)
		}
		if change.Y != "" {
			obj.Set("y", change.Y)
		}
		out[i] = obj
	}
	return out
}

func diffResultToValue(result GitDiffResult) rpc.Value {
	obj := rpc.NewObject(
		"path", result.Path,
		"availability", string(result.Availability),
		"patch", nullableString(result.Patch),
		"beforeContent", nullableString(result.BeforeContent),
		"afterContent", nullableString(result.AfterContent),
	)
	if result.Summary != nil {
		obj.Set("summary", *result.Summary)
	}
	return obj
}

func branchComparisonToValue(comparison GitBranchComparison) rpc.Value {
	return rpc.NewObject(
		"baseRef", nullableString(comparison.BaseRef),
		"headRef", nullableString(comparison.HeadRef),
		"comparisonLabel", nullableString(comparison.ComparisonLabel),
		"changes", fileChangesToValue(comparison.Changes),
	)
}

// ---------------------------------------------------------------------------
// params decoding
// ---------------------------------------------------------------------------

func requireParams(args []rpc.Value, method string) (map[string]any, error) {
	if len(args) == 0 || args[0] == nil {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	params, _ := args[0].(map[string]any)
	return params, nil
}

func requireString(params map[string]any, key, method string) (string, error) {
	if value, ok := params[key].(string); ok {
		return value, nil
	}
	return "", &rpc.Error{Message: "Method not found: " + method}
}

func requireStrings(params map[string]any, key, method string) ([]string, error) {
	raw, ok := params[key].([]any)
	if !ok {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func optionalString(params map[string]any, key string) string {
	if value, ok := params[key].(string); ok {
		return value
	}
	return ""
}

func optionalStringPtr(params map[string]any, key string) *string {
	if value, ok := params[key].(string); ok {
		return &value
	}
	return nil
}

func optionalStrings(params map[string]any, key string) []string {
	raw, ok := params[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func optionalBool(params map[string]any, key string) bool {
	value, _ := params[key].(bool)
	return value
}

func optionalBoolDefault(params map[string]any, key string, fallback bool) bool {
	if value, ok := params[key].(bool); ok {
		return value
	}
	return fallback
}

func optionalIntPtr(params map[string]any, key string) *int {
	switch value := params[key].(type) {
	case int:
		return &value
	case int64:
		v := int(value)
		return &v
	case float64:
		v := int(value)
		return &v
	}
	return nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func decodeDiffQuery(params map[string]any, method string) (GetDiffParams, error) {
	workspacePath, err := requireString(params, "workspacePath", method)
	if err != nil {
		return GetDiffParams{}, err
	}
	path, err := requireString(params, "path", method)
	if err != nil {
		return GetDiffParams{}, err
	}
	query := GetDiffParams{WorkspacePath: workspacePath, Path: path}
	if staged, ok := params["staged"].(bool); ok {
		query.Staged = &staged
	}
	if sourceID, ok := params["sourceId"].(string); ok {
		cast := GitChangeSourceId(sourceID)
		query.SourceID = &cast
	}
	return query, nil
}

func decodePathMutation(params map[string]any, method string) (string, []string, error) {
	workspacePath, err := requireString(params, "workspacePath", method)
	if err != nil {
		return "", nil, err
	}
	paths, err := requireStrings(params, "paths", method)
	if err != nil {
		return "", nil, err
	}
	return workspacePath, paths, nil
}

func decodeConversationContext(raw any) *GitCommitMessageConversationContext {
	params, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	messagesRaw, ok := params["messages"].([]any)
	if !ok {
		return nil
	}
	messages := make([]GitCommitMessageConversationMessage, 0, len(messagesRaw))
	for _, item := range messagesRaw {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		messages = append(messages, GitCommitMessageConversationMessage{Role: role, Content: content})
	}
	context := &GitCommitMessageConversationContext{Messages: messages}
	if sessionID, ok := params["sessionId"].(string); ok {
		context.SessionID = sessionID
	}
	if omitted, ok := params["omittedMessageCount"].(int); ok {
		context.OmittedMessageCount = omitted
	} else if omitted, ok := params["omittedMessageCount"].(float64); ok {
		context.OmittedMessageCount = int(omitted)
	}
	return context
}
