// channel.go adapts the git Service to internal/rpc's Channel interface under
// the "git" channel name (ServiceChannels.Git). One params object per call,
// results shaped exactly like the TS serialization (nulls for null, absent
// keys for undefined).
package git

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
)

// ChannelName is ServiceChannels.Git.
const ChannelName = "git"

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one IGitService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	params, err := requireParams(args, method)
	if err != nil {
		return nil, err
	}
	switch method {
	case "getRepositorySummary":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		summary, err := c.Service.GetRepositorySummary(ctx, workspacePath)
		if err != nil {
			return nil, err
		}
		return summaryToValue(summary), nil
	case "getWorkspaceRepositoryInfo":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		info, err := c.Service.GetWorkspaceRepositoryInfo(ctx, workspacePath)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject(
			"workspacePath", info.WorkspacePath,
			"kind", string(info.Kind),
			"isGitAvailable", info.IsGitAvailable,
		), nil
	case "getLocalBranches":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		result, err := c.Service.GetLocalBranches(ctx, workspacePath)
		if err != nil {
			return nil, err
		}
		branches := make([]any, len(result.Branches))
		for i, branch := range result.Branches {
			branches[i] = rpc.NewObject(
				"name", branch.Name,
				"isCurrent", branch.IsCurrent,
				"upstreamName", nullableString(branch.UpstreamName),
				"commitHash", nullableString(branch.CommitHash),
				"commitTimestampMs", nullableInt64(branch.CommitTimestampMs),
			)
		}
		return rpc.NewObject(
			"headRefType", string(result.HeadRefType),
			"currentBranchName", nullableString(result.CurrentBranchName),
			"branches", branches,
		), nil
	case "getCommitGraph":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		result, err := c.Service.GetCommitGraph(ctx, workspacePath, optionalIntPtr(params, "maxCount"), optionalIntPtr(params, "skip"))
		if err != nil {
			return nil, err
		}
		commits := make([]any, len(result.Commits))
		for i, commit := range result.Commits {
			refs := make([]any, len(commit.Refs))
			for j, ref := range commit.Refs {
				refs[j] = rpc.NewObject("name", ref.Name, "kind", string(ref.Kind))
			}
			commits[i] = rpc.NewObject(
				"hash", commit.Hash,
				"parents", commit.Parents,
				"refs", refs,
				"subject", commit.Subject,
				"authorName", nullableString(commit.AuthorName),
				"authoredAtMs", nullableInt64(commit.AuthoredAtMs),
			)
		}
		return rpc.NewObject("commits", commits, "hasMore", result.HasMore), nil
	case "switchBranch":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		target, err := requireString(params, "targetBranchName", method)
		if err != nil {
			return nil, err
		}
		return c.mutationResult(ctx, func() (GitBranchMutationResult, error) {
			return c.Service.SwitchBranch(ctx, workspacePath, target)
		})
	case "createBranchAndSwitch":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		branchName, err := requireString(params, "branchName", method)
		if err != nil {
			return nil, err
		}
		startPoint := optionalStringPtr(params, "startPoint")
		return c.mutationResult(ctx, func() (GitBranchMutationResult, error) {
			return c.Service.CreateBranchAndSwitch(ctx, workspacePath, branchName, startPoint)
		})
	case "getChanges":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		sourceID, err := requireString(params, "sourceId", method)
		if err != nil {
			return nil, err
		}
		changes, err := c.Service.GetChanges(ctx, workspacePath, GitChangeSourceId(sourceID))
		if err != nil {
			return nil, err
		}
		return fileChangesToValue(changes), nil
	case "getIgnoredPaths":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		paths, err := requireStrings(params, "paths", method)
		if err != nil {
			return nil, err
		}
		return c.Service.GetIgnoredPaths(ctx, workspacePath, paths)
	case "getDiff":
		query, err := decodeDiffQuery(params, method)
		if err != nil {
			return nil, err
		}
		result, err := c.Service.GetDiff(ctx, query)
		if err != nil {
			return nil, err
		}
		return diffResultToValue(result), nil
	case "getBranchComparison":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		comparison, err := c.Service.GetBranchComparison(ctx, workspacePath)
		if err != nil {
			return nil, err
		}
		return branchComparisonToValue(comparison), nil
	case "stagePaths":
		workspacePath, paths, err := decodePathMutation(params, method)
		if err != nil {
			return nil, err
		}
		return nil, c.Service.StagePaths(ctx, workspacePath, paths)
	case "unstagePaths":
		workspacePath, paths, err := decodePathMutation(params, method)
		if err != nil {
			return nil, err
		}
		return nil, c.Service.UnstagePaths(ctx, workspacePath, paths)
	case "discardPaths":
		workspacePath, paths, err := decodePathMutation(params, method)
		if err != nil {
			return nil, err
		}
		return nil, c.Service.DiscardPaths(ctx, workspacePath, paths, optionalBool(params, "staged"))
	case "generateCommitMessage":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		request := GenerateCommitMessageRequest{
			WorkspacePath:           workspacePath,
			WorkspaceIdentity:       optionalString(params, "workspaceIdentity"),
			Locale:                  optionalString(params, "locale"),
			CurrentSessionFilePaths: optionalStrings(params, "currentSessionFilePaths"),
			ConversationContext:     decodeConversationContext(params["conversationContext"]),
		}
		if includeUnstaged, ok := params["includeUnstaged"].(bool); ok {
			request.IncludeUnstaged = includeUnstaged
			request.IncludeUnstagedSet = true
		}
		result, err := c.Service.GenerateCommitMessage(ctx, request)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject(
			"message", result.Message,
			"providerId", result.ProviderID,
			"model", result.Model,
		), nil
	case "commit":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		message, err := requireString(params, "message", method)
		if err != nil {
			return nil, err
		}
		result, err := c.Service.Commit(ctx, CommitRequest{
			WorkspacePath: workspacePath,
			Message:       message,
			Paths:         optionalStrings(params, "paths"),
			StagedOnly:    optionalBool(params, "stagedOnly"),
		})
		if err != nil {
			return nil, err
		}
		return rpc.NewObject(
			"commitHash", result.CommitHash,
			"summary", summaryToValue(result.Summary),
		), nil
	case "push":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		result, err := c.Service.Push(ctx, workspacePath)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject(
			"branchName", nullableString(result.BranchName),
			"trackingBranchName", nullableString(result.TrackingBranchName),
			"remoteName", nullableString(result.RemoteName),
			"setUpstream", result.SetUpstream,
			"summary", summaryToValue(result.Summary),
		), nil
	case "getIdentity":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		identity, err := c.Service.GetIdentity(ctx, workspacePath)
		if err != nil {
			return nil, err
		}
		obj := rpc.NewObject(
			"userName", nullableString(identity.UserName),
			"userEmail", nullableString(identity.UserEmail),
			"nameSource", nullableString(identity.NameSource),
			"emailSource", nullableString(identity.EmailSource),
		)
		if identity.ScopeLabel != nil {
			obj.Set("scopeLabel", *identity.ScopeLabel)
		}
		return obj, nil
	case "refresh":
		workspacePath, err := requireString(params, "workspacePath", method)
		if err != nil {
			return nil, err
		}
		result, err := c.Service.Refresh(ctx, RefreshRequest{
			WorkspacePath:           workspacePath,
			IncludeIdentity:         optionalBool(params, "includeIdentity"),
			IncludeBranchComparison: optionalBool(params, "includeBranchComparison"),
		})
		if err != nil {
			return nil, err
		}
		var identity any
		if result.Identity != nil {
			identity = rpc.NewObject(
				"userName", nullableString(result.Identity.UserName),
				"userEmail", nullableString(result.Identity.UserEmail),
				"nameSource", nullableString(result.Identity.NameSource),
				"emailSource", nullableString(result.Identity.EmailSource),
				"scopeLabel", nullableString(result.Identity.ScopeLabel),
			)
		}
		var comparison any
		if result.BranchComparison != nil {
			comparison = branchComparisonToValue(*result.BranchComparison)
		}
		return rpc.NewObject(
			"summary", summaryToValue(result.Summary),
			"identity", identity,
			"unstagedChanges", fileChangesToValue(result.UnstagedChanges),
			"stagedChanges", fileChangesToValue(result.StagedChanges),
			"branchComparison", comparison,
		), nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: IGitService has no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

func (c *Channel) mutationResult(ctx context.Context, call func() (GitBranchMutationResult, error)) (rpc.Value, error) {
	result, err := call()
	if err != nil {
		return nil, err
	}
	issues := make([]any, len(result.Issues))
	for i, issue := range result.Issues {
		issueObj := rpc.NewObject(
			"code", issue.Code,
			"message", issue.Message,
		)
		if len(issue.Paths) > 0 {
			issueObj.Set("paths", issue.Paths)
		}
		if issue.Detail != nil {
			issueObj.Set("detail", *issue.Detail)
		}
		issues[i] = issueObj
	}
	return rpc.NewObject(
		"ok", result.OK,
		"action", string(result.Action),
		"branchName", nullableString(result.BranchName),
		"didChange", result.DidChange,
		"created", result.Created,
		"summary", summaryToValue(result.Summary),
		"issues", issues,
	), nil
}

// summaryToValue serializes GitRepositorySummary. TS always emits x/y on file
// changes (undefined when absent) but the summary has no optional members, so
// every key is present.
