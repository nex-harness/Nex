package local

// `file` stub (docs/next/channels-research.md §7).
//
// The MVP web flow only touches this channel for the fallback conversation
// workspace and the folder picker; real filesystem access belongs to the
// runner that owns the workspace. TODO(local-channels): route file calls to
// the workspace's runner once the runner contract exposes them.

func newFileStub() *Stub {
	return NewStub(map[string]string{
		"ensureConversationWorkspace": `{"path":"/workspace/conversation","created":false,"workspacePurpose":"conversation"}`,
		"readdir":                     emptyList,
		"searchWorkspaceFiles":        emptyList,
		"listWorkspaceFilesLength":    `0`,
		"listWorkspaceFilesRange":     emptyList,
	})
}
