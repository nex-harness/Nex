package local

// `system` stub (docs/next/channels-research.md §9).
//
// info() is used by the directory browser and the workspace home-path header.
// Real host facts would leak the server's filesystem; the MVP reports a neutral
// root so the web picker still opens.

func newSystemStub() *Stub {
	return newStubWithErrors(map[string]string{
		"info":                         `{"homedir":"/","platform":"linux"}`,
		"listIntegratedTerminalShells": emptyList,
	}, map[string]string{
		"probeIntranet": "intranet probe is not supported by nex-next-server",
	})
}
