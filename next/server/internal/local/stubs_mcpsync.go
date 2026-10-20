package local

// `mcp-sync` stub (docs/next/channels-research.md §9).
//
// The UI loads user MCP config on workspace ready; an empty list is enough to
// keep the MCP settings list quiet. Workspace MCP execution belongs to the
// runner later.

func newMcpSyncStub() *Stub {
	return NewStub(map[string]string{
		"loadMcpFromUserDirectory":       `{"servers":[]}`,
		"listWorkspaceMcpServerStatuses": `{"statuses":{}}`,
		"listLocalUserMcpCandidates":     `{"servers":[],"candidates":[]}`,
		"listRemoteUserMcpStatuses":      `{"statuses":[]}`,
		"exportMcpServers":               `{"content":""}`,
		"checkRemoteUserMcpWriteAccess":  `{"allowed":false}`,
	})
}
