package promptctx

// Target is where a section is injected; CacheHint orders sections in each target.
type Target string
type CacheHint string

const (
	TargetSystem   Target    = "system"
	TargetMetaUser Target    = "meta_user"
	HintStable     CacheHint = "stable"
	HintDynamic    CacheHint = "dynamic"
)

// Section mirrors ContextSection. Chars and Tokens are computed from Content the
// way the TypeScript does (UTF-16 length, estimateTokens).
type Section struct {
	Name    string
	Source  string
	Target  Target
	Hint    CacheHint
	Content string
}

func (s Section) Chars() int  { return jsLen(s.Content) }
func (s Section) Tokens() int { return estimateTokens(s.Content) }

// EnvInfo is the subset of EnvInfo the prompt renders.
type EnvInfo struct {
	Cwd             string
	Platform        string
	Shell           string
	OSVersion       string
	IsGitRepository *bool
	GitStatus       string // "", "clean", "dirty", "not_repo"
	GitBranch       string
	GitMainBranch   string
	GitUser         string
	GitStatusLines  []string
	RecentCommits   []string
}

// Model is the model the next request runs against.
type Model struct{ ProviderID, ModelID string }

// Skill is the subset of SkillMetadata the listing renders.
type Skill struct {
	Name          string
	QualifiedName string
	Description   string
	WhenToUse     string
	Path          string
}

// InstructionSource is one AGENTS.md-style file.
type InstructionSource struct {
	Scope     string // "user" | "workspace"
	FilePath  string
	FileName  string
	Content   string
	Truncated bool
}

// OutputStyle replaces the identity intro line and adds an "Output Style" section.
type OutputStyle struct{ Name, Prompt string }

// Config mirrors the ContextBuilderConfig fields the Go agent uses today.
// Workflow-actor identity is not ported (Phase 4 workflow work).
type Config struct {
	Env                 EnvInfo
	Model               *Model
	Surface             string // "terminal" | "nex_desktop"
	CurrentDate         string
	Instructions        []InstructionSource
	MemoryRoot          string
	MemoryIndexContent  *string
	Skills              []Skill
	SkillMetadataBudget int // 0 = default 20000
	CustomSystemPrompt  string
	OutputStyle         *OutputStyle
	GuidanceToolNames   []string
	GuidanceToolsSet    bool // false = the table is absent (tests / old callers)
}

// MetaUserAttachment is a block placed in the leading user message.
type MetaUserAttachment struct{ Source, Content string }

// Result is what Build returns.
type Result struct {
	Sections       []Section
	SystemMessages []string // as separate provider messages, before OpenAI merging
	MergedSystem   string   // OpenAI-compatible: leading system messages concatenated
	MetaUser       []MetaUserAttachment
}
