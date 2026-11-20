// Package prompts assembles the ZCode-grade System Prompt and meta context for Nex
// sessions. The cache-critical "system_prompt" section is a pure function of the
// session role: it contains no timestamps, git output, or workspace state, so it is
// byte-identical across turns and stays warm in Anthropic ephemeral / OpenAI
// Responses prefix caches. Everything volatile lives in the "meta_context" section.
package prompts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
)

// Section categories recognised by the Context Windows telemetry breakdown.
const (
	CategorySystemPrompt = "system_prompt"
	CategorySkills       = "skills"
	CategoryMCPTools     = "mcp_tools"
	CategoryMetaContext  = "meta_context"
)

// Limits keep prompt assembly bounded even on adversarial workspaces.
const (
	maxSkills                = 64
	maxSkillDescriptionRunes = 200
	maxSkillsSectionBytes    = 16 * 1024
	maxAgentsMDBytes         = 64 * 1024
	maxGitStatusBytes        = 4 * 1024
	maxGitStatusLines        = 40
	gitCommandTimeout        = time.Second
)

// SkillInfo is a compact, deterministic description of an available skill.
type SkillInfo struct {
	Name        string
	Description string
	Path        string // absolute path to the skill's SKILL.md
}

// GitInfo captures the bounded, fast git snapshot shown in meta context.
type GitInfo struct {
	IsRepo bool
	Branch string
	Status string
	Log    string
}

// SessionOptions bundles every input needed to assemble session sections.
//
// Only SubagentType influences the cached core "system_prompt" section. All other
// state is emitted in "meta_context" so the cached prefix remains stable.
type SessionOptions struct {
	RunnerID string
	WorkDir  string
	Shell    string // resolved shell path; detected when empty
	Platform string // GOOS; defaults to runtime.GOOS
	Arch     string // GOARCH; defaults to runtime.GOARCH
	Now      time.Time

	Skills         []SkillInfo
	Memories       []string
	MCPNamespaces  string
	SubagentType   string // "" = main agent; "explore"/"worker" = subagent role
	CustomPrompt   string // optional agent-profile instructions appended to the core
	PermissionMode string // "full_access" | "interactive" | "autonomous" | "read_only" | "plan"

	AgentsMDGlobalPath    string // defaults to ~/.nex/AGENTS.md
	AgentsMDWorkspacePath string // defaults to multi-level discovery from <WorkDir> up to git root

	// Git overrides auto-detection when non-nil (used by tests).
	Git *GitInfo
}

// coreIdentity is the stable main-agent system prompt. It must never include
// volatile values (time, git state, environment); cache correctness depends on it.
const coreIdentity = `You are Nex, an interactive software engineering agent.

You are an agent working with the user toward their goals, using your own judgment along the way.

# Security and safety
Assist with authorized security testing, defensive security, CTF challenges, and educational contexts. Refuse requests to build or improve malware, exploit code intended for real-world attack, destructive or denial-of-service tooling, or to gain unauthorized access to systems or data. When a request is ambiguous, prefer the defensive or educational interpretation and briefly state what you will and will not do.

# Harness
- Output outside tool use is rendered as GitHub-flavored Markdown.
- Tools run behind a user-selected permission mode. A denied call means the user declined it — adjust your approach instead of retrying the same call verbatim. Hooks may intercept tool calls; treat hook output as user feedback.
- Prefer dedicated file/search tools (` + "`read`, `edit`, `write`, `grep`, `glob`" + `) over shell commands when one fits. Independent tool calls can run in parallel in a single response.
- For multi-step tasks, use ` + "`todo_write`" + ` to maintain a clear working plan rendered live in the user's Progress panel. Keep one item ` + "`in_progress`" + ` at a time and mark items ` + "`completed`" + ` when done.
- Reference code as ` + "`file_path:line_number`" + ` — it is clickable.

# Context management
- Write code that reads like the surrounding code: match its comment density, naming, and idiom.
- Use neutral ` + "`they/them`" + ` pronouns when a person's pronouns haven't been stated.
- Confirm before hard-to-reverse or outward-facing actions unless explicitly authorized. Before deleting or overwriting, look at the target. Report outcomes faithfully: never hedge verified results or hide failing test output.
- When you have enough information to act, act. Do not re-derive established facts or narrate unused options; give a clear recommendation.`

// StableCorePrompt returns the byte-stable main-agent system prompt.
func StableCorePrompt() string { return coreIdentity }

// BuildSessionSections assembles the full ordered section list for a session.
// It dispatches to the subagent prompt builder when opts.SubagentType is set.
func BuildSessionSections(opts SessionOptions) []ai.SystemSection {
	return BuildSystemSections(opts)
}

// BuildSystemSections assembles the full ordered section list. The returned
// system_prompt section depends only on the session role, so repeated calls with
// different times/git state produce byte-identical system_prompt content.
func BuildSystemSections(opts SessionOptions) []ai.SystemSection {
	sections := []ai.SystemSection{coreSection(opts)}

	if skills := buildSkillsSection(opts.Skills); skills != "" {
		sections = append(sections, ai.SystemSection{
			Category: CategorySkills,
			Name:     "available_skills",
			Content:  skills,
		})
	}

	if mcp := strings.TrimSpace(opts.MCPNamespaces); mcp != "" {
		sections = append(sections, ai.SystemSection{
			Category: CategoryMCPTools,
			Name:     "mcp_namespaces",
			Content:  "# MCP namespaces\n\n" + mcp,
		})
	}

	if meta := buildMetaContext(opts); meta != "" {
		sections = append(sections, ai.SystemSection{
			Category: CategoryMetaContext,
			Name:     "environment",
			Content:  meta,
		})
	}

	return sections
}

// coreSection returns the cached system_prompt section for the active role.
func coreSection(opts SessionOptions) ai.SystemSection {
	content := StableCorePrompt()
	if role := strings.TrimSpace(opts.SubagentType); role != "" {
		content = SubagentSystemPrompt(role)
	}
	if custom := strings.TrimSpace(opts.CustomPrompt); custom != "" {
		content += "\n\n# Agent profile\n" + custom
	}
	return ai.SystemSection{
		Category:     CategorySystemPrompt,
		Name:         "core",
		Content:      content,
		CacheControl: true,
	}
}

// buildSkillsSection renders the deterministic "Available skills" catalog.
func buildSkillsSection(skills []SkillInfo) string {
	if len(skills) == 0 {
		return ""
	}

	cleaned := make([]SkillInfo, 0, len(skills))
	for _, sk := range skills {
		name := strings.TrimSpace(sk.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(strings.ReplaceAll(sk.Description, "\n", " "))
		desc = truncateRunes(desc, maxSkillDescriptionRunes)
		if desc == "" {
			desc = "(no description)"
		}
		cleaned = append(cleaned, SkillInfo{Name: name, Description: desc, Path: strings.TrimSpace(sk.Path)})
	}
	if len(cleaned) == 0 {
		return ""
	}

	sort.Slice(cleaned, func(i, j int) bool {
		if cleaned[i].Name == cleaned[j].Name {
			return cleaned[i].Path < cleaned[j].Path
		}
		return cleaned[i].Name < cleaned[j].Name
	})

	var sb strings.Builder
	sb.WriteString("Available skills:\n")
	shown := 0
	for _, sk := range cleaned {
		if shown >= maxSkills || sb.Len() >= maxSkillsSectionBytes {
			break
		}
		if sk.Path != "" {
			sb.WriteString("- " + sk.Name + ": " + sk.Description + " (file: " + sk.Path + ")\n")
		} else {
			sb.WriteString("- " + sk.Name + ": " + sk.Description + "\n")
		}
		shown++
	}
	if remaining := len(cleaned) - shown; remaining > 0 {
		sb.WriteString("- ... " + itoa(remaining) + " more skills omitted\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// buildMetaContext renders the volatile environment, git snapshot, and AGENTS.md
// instructions. This section is intentionally separate from the cached core.
func buildMetaContext(opts SessionOptions) string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	platform := opts.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	arch := opts.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	shell := strings.TrimSpace(opts.Shell)
	if shell == "" {
		shell = tools.DefaultShellName()
	}
	workDir := strings.TrimSpace(opts.WorkDir)

	gitInfo := opts.Git
	if gitInfo == nil {
		detected := ReadGitSnapshot(workDir)
		gitInfo = &detected
	}

	var sb strings.Builder
	sb.WriteString("# Environment\n")
	sb.WriteString("- Runner ID: " + fallback(opts.RunnerID, "(unknown)") + "\n")
	sb.WriteString("- Primary working directory: " + fallback(workDir, "(not set)") + "\n")
	if gitInfo.IsRepo {
		sb.WriteString("- Is a git repository: Yes\n")
	} else {
		sb.WriteString("- Is a git repository: No\n")
	}
	sb.WriteString("- Platform: " + platform + "\n")
	sb.WriteString("- Shell: " + shell + "\n")
	sb.WriteString("- OS/Arch: " + platform + "/" + arch + "\n")
	if mode := strings.TrimSpace(opts.PermissionMode); mode != "" {
		sb.WriteString("- Permission mode: " + mode + "\n")
	}
	sb.WriteString("- Today's date: " + now.Format("2006-01-02"))

	if mode := strings.TrimSpace(opts.PermissionMode); mode == "read_only" || mode == "plan" {
		sb.WriteString("\n\n# Plan Mode (Read-Only)\n")
		sb.WriteString("This session is currently in read-only Plan Mode (`" + mode + "`).\n")
		sb.WriteString("- Mutating operations (`write`, `edit`, and state-changing `bash` commands) are blocked by the harness.\n")
		sb.WriteString("- Use read-only tools (`read`, `grep`, `glob`, `websearch`, `webfetch`, and read-only `bash` inspection commands like `git status`, `git diff`, `git log`) to explore the codebase thoroughly.\n")
		sb.WriteString("- If key requirements or architectural choices are ambiguous, use `ask_user_question` to clarify before finalizing your plan.\n")
		sb.WriteString("- When your implementation plan is ready, call `exit_plan_mode` with a structured Markdown plan citing exact `file_path:line_number` references, trade-offs, and verification steps, then wait for user approval.")
	}

	if len(opts.Memories) > 0 {
		sb.WriteString("\n\n# Long-term memories\n")
		sb.WriteString("Persistent preferences and project facts recalled from prior sessions (update or add new ones via `memory_save` when the user shares durable preferences or architectural decisions):\n")
		memBudget := 8 * 1024
		for _, m := range opts.Memories {
			line := "- " + strings.TrimSpace(m) + "\n"
			if len(line) > memBudget {
				break
			}
			sb.WriteString(line)
			memBudget -= len(line)
		}
	}

	if gitInfo.IsRepo {
		sb.WriteString("\n\n# gitStatus\n")
		sb.WriteString("Current branch: " + fallback(strings.TrimSpace(gitInfo.Branch), "(unknown)") + "\n")
		if status := strings.TrimRight(gitInfo.Status, "\n"); status != "" {
			sb.WriteString("Status:\n" + truncateUTF8Lines(status, maxGitStatusBytes, maxGitStatusLines) + "\n")
		} else {
			sb.WriteString("Status: (clean)\n")
		}
		if log := strings.TrimRight(gitInfo.Log, "\n"); log != "" {
			sb.WriteString("Recent commits:\n" + truncateUTF8Lines(log, maxGitStatusBytes/2, 5))
		} else {
			sb.WriteString("Recent commits: (none)")
		}
	}

	if instructions := buildInstructions(opts); instructions != "" {
		sb.WriteString("\n\n" + instructions)
	}

	return strings.TrimRight(sb.String(), "\n")
}

// buildInstructions loads user-global (~/.nex/AGENTS.md) and multi-level workspace
// AGENTS.md / CLAUDE.md files walking from the Git root down to WorkDir (outer-to-inner).
// Total workspace instructions are capped at maxAgentsMDBytes (64KB).
func buildInstructions(opts SessionOptions) string {
	globalPath := strings.TrimSpace(opts.AgentsMDGlobalPath)
	if globalPath == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			globalPath = filepath.Join(home, ".nex", "AGENTS.md")
		}
	}
	global := readCapped(globalPath, maxAgentsMDBytes)

	type docEntry struct {
		label   string
		content string
	}
	var workspaceDocs []docEntry
	remaining := maxAgentsMDBytes

	if explicit := strings.TrimSpace(opts.AgentsMDWorkspacePath); explicit != "" {
		if content := readCapped(explicit, remaining); content != "" {
			workspaceDocs = append(workspaceDocs, docEntry{label: filepath.Base(explicit), content: content})
		}
	} else if workDir := strings.TrimSpace(opts.WorkDir); workDir != "" {
		dirs := instructionSearchDirs(workDir)
		seen := make(map[string]bool)
		if globalPath != "" {
			if abs, err := filepath.Abs(globalPath); err == nil {
				seen[abs] = true
			}
		}
		for _, dir := range dirs {
			if remaining <= 0 {
				break
			}
			for _, fname := range []string{"AGENTS.md", "CLAUDE.md"} {
				if remaining <= 0 {
					break
				}
				candidate := filepath.Join(dir, fname)
				abs, err := filepath.Abs(candidate)
				if err != nil {
					abs = candidate
				}
				if seen[abs] {
					continue
				}
				content := readCapped(candidate, remaining)
				if content == "" {
					continue
				}
				seen[abs] = true
				remaining -= len(content)
				label := fname
				if rel, rErr := filepath.Rel(workDir, candidate); rErr == nil && rel != "" && rel != fname {
					label = filepath.ToSlash(rel)
				}
				workspaceDocs = append(workspaceDocs, docEntry{label: label, content: content})
			}
		}
	}

	if global == "" && len(workspaceDocs) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Instructions\n\n")
	sb.WriteString("Codebase and user instructions are shown below. Be sure to adhere to these instructions.")
	if global != "" {
		sb.WriteString("\n\n## Global instructions (~/.nex/AGENTS.md)\n\n" + global)
	}
	for _, doc := range workspaceDocs {
		sb.WriteString("\n\n## Workspace instructions (" + doc.label + ")\n\n" + doc.content)
	}
	return sb.String()
}

// findGitRoot walks upward from workDir looking for a .git directory or file (worktree).
func findGitRoot(workDir string) string {
	dir, err := filepath.Abs(workDir)
	if err != nil {
		dir = filepath.Clean(workDir)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// instructionSearchDirs returns directories from the Git root down to workDir (outer-to-inner),
// or just [workDir] if workDir is not inside a Git repository.
func instructionSearchDirs(workDir string) []string {
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		absWork = filepath.Clean(workDir)
	}
	gitRoot := findGitRoot(absWork)
	if gitRoot == "" {
		return []string{absWork}
	}
	var rev []string
	cur := absWork
	for {
		rev = append(rev, cur)
		if cur == gitRoot {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	// Reverse to outer-to-inner order (repo root first, active workDir last)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// ReadGitSnapshot performs the bounded (<1s per command) git probe used by meta
// context. It never returns an error: missing git or a non-repo yields IsRepo=false.
func ReadGitSnapshot(workDir string) GitInfo {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return GitInfo{}
	}
	if findGitRoot(workDir) == "" {
		return GitInfo{}
	}
	info := GitInfo{IsRepo: true}
	info.Branch = runGit(workDir, "rev-parse", "--abbrev-ref", "HEAD")
	info.Status = runGit(workDir, "status", "--short")
	info.Log = runGit(workDir, "log", "-n", "5", "--oneline")
	if strings.TrimSpace(info.Branch) == "" && info.Status == "" && info.Log == "" {
		// A .git entry that git refuses to read is not a usable repository.
		return GitInfo{}
	}
	return info
}

func runGit(workDir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workDir
	// Keep the probe side-effect free and locale-stable.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

func readCapped(path string, maxBytes int) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return ""
	}
	return strings.TrimSpace(truncateUTF8(string(raw), maxBytes))
}

func fallback(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "..."
}

// truncateUTF8 cuts s to at most maxBytes without splitting a UTF-8 rune.
func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n... [truncated]"
}

// truncateUTF8Lines bounds wrapped command output by bytes and line count.
func truncateUTF8Lines(s string, maxBytes, maxLines int) string {
	lines := strings.Split(s, "\n")
	if maxLines > 0 && len(lines) > maxLines {
		omitted := len(lines) - maxLines
		lines = append(lines[:maxLines], "... ["+itoa(omitted)+" more lines omitted]")
	}
	return truncateUTF8(strings.Join(lines, "\n"), maxBytes)
}

// itoa avoids pulling strconv in for a handful of small non-negative integers.
func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
