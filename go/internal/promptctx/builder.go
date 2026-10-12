package promptctx

import (
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

//go:embed prompts/*
var promptFS embed.FS

func prompt(name string) string {
	b, err := promptFS.ReadFile("prompts/" + name)
	if err != nil {
		panic(err) // embedded at build time; a missing file is a programming error
	}
	return string(b)
}

const (
	defaultSkillMetadataBudget = 20_000
	maxDescriptionChars        = 250
	skillToolName              = "Skill"
	memoryRootSentinel         = "\x00MEMORY_ROOT\x00"
	memoryIndexLineLimit       = 200
	memoryIndexCharLimit       = 25_000
	metaUserIntro              = "As you answer the user's questions, you can use the following context:"
	metaUserOutro              = "      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task."
)

func section(name, source string, target Target, hint CacheHint, content string) Section {
	return Section{Name: name, Source: source, Target: target, Hint: hint, Content: content}
}

func (e EnvInfo) isGitRepository() bool {
	if e.IsGitRepository != nil {
		return *e.IsGitRepository
	}
	if e.GitStatus != "" {
		return e.GitStatus != "not_repo"
	}
	return e.GitBranch != ""
}

func envInfoContent(e EnvInfo, m *Model) string {
	yesNo := "no"
	if e.isGitRepository() {
		yesNo = "yes"
	}
	lines := []string{
		"# Environment",
		"You have been invoked in the following environment:",
		"- Primary working directory: " + e.Cwd,
		"- Is a git repository: " + yesNo,
		"- Platform: " + e.Platform,
		"- Shell: " + e.Shell,
		"- OS Version: " + e.OSVersion,
	}
	if m != nil {
		lines = append(lines, fmt.Sprintf("- You are powered by the model named %s/%s.", m.ProviderID, m.ModelID))
	}
	return strings.Join(lines, "\n")
}

func gitSystemContext(e EnvInfo) (string, bool) {
	if !e.isGitRepository() {
		return "", false
	}
	lines := []string{"gitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation."}
	if e.GitBranch != "" {
		lines = append(lines, "", "Current branch: "+e.GitBranch)
	}
	if e.GitMainBranch != "" {
		lines = append(lines, "", "Main branch (you will usually use this for PRs): "+e.GitMainBranch)
	}
	if e.GitUser != "" {
		lines = append(lines, "", "Git user: "+e.GitUser)
	}
	status := "(unknown)"
	switch {
	case len(e.GitStatusLines) > 0:
		status = strings.Join(e.GitStatusLines, "\n")
	case e.GitStatus == "dirty":
		status = "(dirty)"
	case e.GitStatus == "clean":
		status = "(clean)"
	}
	lines = append(lines, "", "Status:\n"+status)
	lines = append(lines, "", "Recent commits:\n"+strings.Join(e.RecentCommits, "\n"))
	return strings.Join(lines, "\n"), true
}

var collator = collate.New(language.English)

func skillDisplayName(s Skill) string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

func bareAliasSuffix(s Skill) string {
	if s.QualifiedName != "" && s.QualifiedName != s.Name {
		return " (also loadable as " + s.Name + ")"
	}
	return ""
}

func skillsContent(skills []Skill, budget int) string {
	head := []string{"The following skills are available for use with the Skill tool:", ""}
	sorted := append([]Skill(nil), skills...)
	// String.prototype.localeCompare with the default (ICU en-US) collation.
	sort.SliceStable(sorted, func(i, j int) bool {
		return collator.CompareString(skillDisplayName(sorted[i]), skillDisplayName(sorted[j])) < 0
	})
	lines := append([]string(nil), head...)
	for _, s := range sorted {
		desc := s.Description
		if s.WhenToUse != "" {
			desc = s.Description + " - " + s.WhenToUse
		}
		if jsLen(desc) > maxDescriptionChars {
			desc = jsSlice(desc, 0, maxDescriptionChars-1) + "..."
		}
		lines = append(lines, fmt.Sprintf("- %s: %s%s (file: %s)", skillDisplayName(s), desc, bareAliasSuffix(s), s.Path))
	}
	if full := strings.Join(lines, "\n"); jsLen(full) <= budget {
		return full
	}
	names := append([]string(nil), head...)
	for _, s := range sorted {
		names = append(names, fmt.Sprintf("- %s%s (file: %s)", skillDisplayName(s), bareAliasSuffix(s), s.Path))
	}
	return strings.Join(names, "\n")
}

func formatBytes(v int) string {
	kb := float64(v) / 1024
	trim := func(s string) string { return strings.TrimSuffix(s, ".0") }
	switch {
	case kb < 1:
		return fmt.Sprintf("%d bytes", v)
	case kb < 1024:
		return trim(toFixed1(kb)) + "KB"
	}
	mb := kb / 1024
	if mb < 1024 {
		return trim(toFixed1(mb)) + "MB"
	}
	return trim(toFixed1(mb/1024)) + "GB"
}

// formatMemoryIndex ports formatMemoryIndexContent (without the markdown comment stripping, below).
func formatMemoryIndex(content string) string {
	trimmed := jsTrim(content)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	lineCount, charCount := len(lines), jsLen(trimmed)
	lineTrunc, charTrunc := lineCount > memoryIndexLineLimit, charCount > memoryIndexCharLimit
	if !lineTrunc && !charTrunc {
		return trimmed
	}
	truncated := trimmed
	if lineTrunc {
		truncated = strings.Join(lines[:memoryIndexLineLimit], "\n")
	}
	if jsLen(truncated) > memoryIndexCharLimit {
		finalNewline := jsLastIndexOf(truncated, '\n', memoryIndexCharLimit)
		cut := memoryIndexCharLimit
		if finalNewline > 0 {
			cut = finalNewline
		}
		truncated = jsSlice(truncated, 0, cut)
	}
	var size string
	switch {
	case charTrunc && !lineTrunc:
		size = fmt.Sprintf("%s (limit: %s) — index entries are too long", formatBytes(charCount), formatBytes(memoryIndexCharLimit))
	case lineTrunc && !charTrunc:
		size = fmt.Sprintf("%d lines (limit: %d)", lineCount, memoryIndexLineLimit)
	default:
		size = fmt.Sprintf("%d lines and %s", lineCount, formatBytes(charCount))
	}
	return truncated + "\n\n> WARNING: MEMORY.md is " + size + ". Only part of it was loaded. Keep index entries to one line under ~200 chars; move detail into topic files."
}

func instructionContent(sources []InstructionSource) string {
	var parts []string
	for _, s := range sources {
		body := s.Content
		if s.Truncated {
			body += "\n\n[File truncated: " + s.FileName + "]"
		}
		body = jsTrim(body)
		if body == "" {
			continue
		}
		scope := "workspace instructions"
		if s.Scope == "user" {
			scope = "user default instructions"
		}
		parts = append(parts, strings.Join([]string{fmt.Sprintf("Contents of %s (%s):", s.FilePath, scope), "", body}, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

func requestUserContext(c Config) (string, bool) {
	var sections []string
	if ic := instructionContent(c.Instructions); ic != "" {
		sections = append(sections, ic)
	}
	if c.MemoryRoot != "" && c.MemoryIndexContent != nil {
		if formatted := formatMemoryIndex(stripFrontmatter(*c.MemoryIndexContent)); formatted != "" {
			sections = append(sections, strings.Join([]string{
				fmt.Sprintf("Contents of %s (user's auto-memory, persists across conversations):", filepath.Join(c.MemoryRoot, "MEMORY.md")),
				"", formatted}, "\n"))
		}
	}
	if len(sections) == 0 {
		return "", false
	}
	return strings.Join([]string{
		"# agentsMd",
		"Codebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written.",
		"", strings.Join(sections, "\n\n")}, "\n"), true
}

// Build assembles sections in the order of ContextBuilder.build().
func Build(c Config) Result {
	var secs []Section
	style := c.OutputStyle != nil && jsTrim(c.OutputStyle.Prompt) != ""
	custom := jsTrim(c.CustomSystemPrompt)

	secs = append(secs, section("CLI Prefix", "cli_prefix", TargetSystem, HintStable, prompt("cli_prefix.txt")))
	switch {
	case custom != "":
		secs = append(secs, section("Custom System Prompt", "custom_system_prompt", TargetSystem, HintStable, "\n"+custom))
	case style:
		secs = append(secs, section("Agent Identity", "identity", TargetSystem, HintStable, prompt("identity_output_style.txt")))
	default:
		secs = append(secs, section("Agent Identity", "identity", TargetSystem, HintStable, prompt("identity_default.txt")))
	}

	if custom == "" {
		if c.Surface == "nex_desktop" {
			secs = append(secs, section("Nex Desktop Context", "desktop_context", TargetSystem, HintStable, prompt("desktop.txt")))
		}
		secs = append(secs, section("Dynamic Behavior", "dynamic_behavior", TargetSystem, HintDynamic, prompt("dynamic_behavior.txt")))
		if c.hasTool(skillToolName) && len(c.Skills) > 0 {
			secs = append(secs, section("Session-specific guidance", "session_guidance", TargetSystem, HintDynamic, prompt("session_guidance_skill.txt")))
		}
		if c.MemoryRoot != "" {
			secs = append(secs, section("Memory", "memory", TargetSystem, HintDynamic, strings.ReplaceAll(prompt("memory.tmpl"), memoryRootSentinel, c.MemoryRoot)))
		}
		secs = append(secs, section("Environment Info", "env_info", TargetSystem, HintDynamic, envInfoContent(c.Env, c.Model)))
		if style {
			secs = append(secs, section("Output Style", "output_style", TargetSystem, HintDynamic, "# Output Style: "+c.OutputStyle.Name+"\n"+jsTrim(c.OutputStyle.Prompt)))
		}
		secs = append(secs, section("Context Management", "context_management", TargetSystem, HintDynamic, prompt("context_management.txt")))
		if g, ok := gitSystemContext(c.Env); ok {
			secs = append(secs, section("System Context", "system_context", TargetSystem, HintDynamic, g))
		}
	}

	if len(c.Skills) > 0 && (!c.GuidanceToolsSet || c.hasTool(skillToolName)) {
		budget := c.SkillMetadataBudget
		if budget == 0 {
			budget = defaultSkillMetadataBudget
		}
		secs = append(secs, section("Skills", "skills", TargetMetaUser, HintDynamic, skillsContent(c.Skills, budget)))
	}
	if rc, ok := requestUserContext(c); ok {
		secs = append(secs, section("Request User Context", "request_user_context", TargetMetaUser, HintDynamic, rc))
	}
	if c.CurrentDate != "" {
		secs = append(secs, section("Current Date", "current_date", TargetMetaUser, HintDynamic, "# currentDate\nToday's date is "+c.CurrentDate+"."))
	}

	ordered := orderForInjection(secs)
	res := Result{Sections: ordered}
	res.SystemMessages, res.MergedSystem = assembleSystem(ordered)
	res.MetaUser = assembleMetaUser(ordered)
	return res
}

// hasTool reports whether the runtime tool table lists name. An absent table
// (GuidanceToolsSet false) means "unknown", which the TypeScript treats as present
// for the Skill gate in session guidance only when the table exists.
func (c Config) hasTool(name string) bool {
	if !c.GuidanceToolsSet {
		return false
	}
	for _, t := range c.GuidanceToolNames {
		if t == name {
			return true
		}
	}
	return false
}

func orderForInjection(in []Section) []Section {
	var out []Section
	for _, want := range []struct {
		t Target
		h CacheHint
	}{{TargetSystem, HintStable}, {TargetSystem, HintDynamic}, {TargetMetaUser, HintStable}, {TargetMetaUser, HintDynamic}} {
		for _, s := range in {
			if s.Target == want.t && s.Hint == want.h {
				out = append(out, s)
			}
		}
	}
	return out
}

func joinContent(secs []Section) string {
	parts := make([]string, len(secs))
	for i, s := range secs {
		parts[i] = s.Content
	}
	return strings.Join(parts, "\n\n")
}

func assembleSystem(secs []Section) ([]string, string) {
	var cli, stable, dynamic []Section
	for _, s := range secs {
		if s.Target != TargetSystem {
			continue
		}
		switch {
		case s.Source == "cli_prefix":
			cli = append(cli, s)
		case s.Hint == HintStable:
			stable = append(stable, s)
		case s.Hint == HintDynamic:
			dynamic = append(dynamic, s)
		}
	}
	var msgs []string
	if c := joinContent(cli); c != "" {
		msgs = append(msgs, c)
	}
	if c := joinContent(stable); c != "" {
		msgs = append(msgs, c)
	}
	if c := joinContent(dynamic); c != "" {
		msgs = append(msgs, "\n\n"+c)
	}
	return msgs, strings.Join(msgs, "")
}

func assembleMetaUser(secs []Section) []MetaUserAttachment {
	var skills, rest []Section
	for _, s := range secs {
		if s.Target != TargetMetaUser {
			continue
		}
		if s.Source == "skills" {
			skills = append(skills, s)
		} else {
			rest = append(rest, s)
		}
	}
	var out []MetaUserAttachment
	if c := joinContent(skills); c != "" {
		out = append(out, MetaUserAttachment{Source: "skills_listing", Content: c})
	}
	if len(rest) > 0 {
		out = append(out, MetaUserAttachment{Source: "context_prefix", Content: strings.Join([]string{metaUserIntro, joinContent(rest), "", metaUserOutro}, "\n")})
	}
	return out
}
