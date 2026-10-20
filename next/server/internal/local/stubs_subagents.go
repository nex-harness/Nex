package local

// `subagents` stub (docs/next/channels-research.md §9).
//
// The slash-command menu lists subagents; an empty catalog with
// userScopeAvailable:false keeps the menu quiet.

func newSubagentsStub() *Stub {
	return NewStub(map[string]string{
		"list": `{"agents":[],"userAgents":[],"pluginAgents":[],"capability":{"userScopeAvailable":false}}`,
	})
}
