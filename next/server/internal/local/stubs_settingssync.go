package local

// `settings-sync` stub (docs/next/channels-research.md §9).
//
// The onboarding dialog is mounted unconditionally; getFirstRunPromptState
// returning {handled:true} makes it return early and never open. The server has
// no Claude config to import, so migration is reported unsupported.

func newSettingsSyncStub() *Stub {
	return newStubWithErrors(map[string]string{
		"getFirstRunPromptState":             `{"handled":true}`,
		"getClaudeAgentsFileMigrationStatus": `{"supported":false,"sourceExists":false,"targetExists":false,"sourcePath":"","targetPath":"","unavailableReason":"missingSource"}`,
	}, map[string]string{
		"copyClaudeAgentsFileToNexAgentsFile": "settings sync is not supported by nex-next-server",
	})
}
