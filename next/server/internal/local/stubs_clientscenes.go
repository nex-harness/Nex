package local

// `client-scenes` stub (docs/next/channels-research.md §9).
//
// Suggested prompts in the composer; an empty list logs nothing and renders no
// suggestions.

func newClientScenesStub() *Stub {
	return NewStub(map[string]string{
		"list": `{"code":0,"msg":"","data":[]}`,
	})
}
