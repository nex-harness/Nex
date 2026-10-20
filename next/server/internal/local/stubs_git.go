package local

// `git` stub (docs/next/channels-research.md §9).
//
// The Git pane refreshes on mount; an "unavailable" summary keeps it in the
// empty state instead of surfacing an error. TODO(local-channels): forward git
// calls to the workspace's runner once it exposes a git surface.

func newGitStub() *Stub {
	return newStubWithErrors(map[string]string{
		"refresh": `{
  "summary": {
    "workspacePath": "",
    "repoRoot": "",
    "workspaceInRepoPath": "",
    "autoRefreshWatchPaths": [],
    "branchName": null,
    "trackingBranchName": null,
    "headRefType": "branch",
    "ahead": 0,
    "behind": 0,
    "isDirty": false,
    "isGitAvailable": false,
    "isRepository": false
  },
  "identity": null,
  "unstagedChanges": [],
  "stagedChanges": [],
  "branchComparison": null
}`,
	}, map[string]string{
		"commit":                "git is not available on the nex-next-server host",
		"push":                  "git is not available on the nex-next-server host",
		"generateCommitMessage": "git is not available on the nex-next-server host",
	})
}
