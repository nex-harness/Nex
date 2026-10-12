package tools

// Metadata mirrors the subset of ToolMetadata the port asserts (README tools.md).
type Metadata struct {
	Name            string
	ReadOnly        bool
	Destructive     bool
	ConcurrentSafe  bool
	TimeoutMs       int
	MaxOutputBytes  int
	SideEffectScope string
	RiskLevel       string
	NeedsApproval   bool
	Permission      string
}

// ResultBudget mirrors ToolResultBudget (strategy + preview direction + bytes).
type ResultBudget struct {
	MaxInlineBytes int
	MaxModelBytes  int
	Strategy       string
	PreviewDir     string
	PreviewBytes   int
}

// Entry is the static description of a ported tool.
type Entry struct {
	Metadata     Metadata
	ResultBudget ResultBudget
}

// Registry returns the Read, Write and Edit tool descriptors. The values mirror
// the TypeScript tool entries; meta_test.go checks them against the generated
// vectors, so a drift on either side fails a test.
func Registry() []Entry {
	return []Entry{
		{
			Metadata:     Metadata{"Read", true, false, true, 30000, ReadMaxFileSizeBytes, "none", "low", false, "read"},
			ResultBudget: ResultBudget{ReadMaxFileSizeBytes, ReadMaxFileSizeBytes, "truncate", "head", ReadMaxFileSizeBytes},
		},
		{
			Metadata:     Metadata{"Write", false, false, false, 30000, 1000000, "workspace", "medium", true, "edit"},
			ResultBudget: ResultBudget{1000000, 100000, "truncate", "head", 100000},
		},
		{
			Metadata:     Metadata{"Edit", false, false, false, 30000, 1000000, "workspace", "medium", true, "edit"},
			ResultBudget: ResultBudget{1000000, 100000, "truncate", "head", 100000},
		},
	}
}

// ResultBudgetFor returns the budget of a ported tool, or the zero value.
func ResultBudgetFor(name string) ResultBudget {
	for _, e := range Registry() {
		if e.Metadata.Name == name {
			return e.ResultBudget
		}
	}
	return ResultBudget{}
}
