package local

// `commands` stub (docs/next/config-mcp-skills.md P2).
//
// The settings page lists the user's command files; the MVP maps slash
// commands onto pi prompt templates (served through `skills`/the `/` menu), so
// the list is empty and writes are rejected instead of pretending to persist.

func newCommandsStub() *Stub {
	return newStubWithErrors(map[string]string{
		"list":                            `{"commands":[]}`,
		"getPrimaryUserCommandsDirectory": `{"path":"","exists":false}`,
	}, map[string]string{
		"write":      "command files are not persisted by nex-next-server; upload a prompt template instead",
		"update":     "command files are not persisted by nex-next-server; upload a prompt template instead",
		"delete":     "command files are not persisted by nex-next-server; upload a prompt template instead",
		"setEnabled": "command files are not persisted by nex-next-server; upload a prompt template instead",
	})
}
