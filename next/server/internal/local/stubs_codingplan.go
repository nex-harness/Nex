package local

// `coding-plan-subscription` stub (docs/next/channels-research.md §9).
//
// Root loads the dynamic-workflow client config at startup; "disabled" is the
// documented default and leaves the feature off.

func newCodingPlanStub() *Stub {
	return NewStub(map[string]string{
		"getDynamicWorkflowClientConfig": `{"mode":"disabled","enabled":false,"source":"default"}`,
	})
}
