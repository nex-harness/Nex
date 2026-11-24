package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nex-harness/nex/pkg/ai"
)

// IgnoreMatcher implements lightweight .gitignore rule evaluation for workspace traversal.
type IgnoreMatcher struct {
	rules []ignoreRule
}

type ignoreRule struct {
	negate   bool
	dirOnly  bool
	anchored bool
	re       *regexp.Regexp
}

// LoadIgnoreMatcher parses root .gitignore (if present) plus standard VCS ignores (.git).
func LoadIgnoreMatcher(rootDir string) *IgnoreMatcher {
	m := &IgnoreMatcher{}
	m.addRule(".git/")
	if rootDir != "" {
		if raw, err := os.ReadFile(filepath.Join(rootDir, ".gitignore")); err == nil {
			for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
				m.addRule(line)
			}
		}
	}
	return m
}

func (m *IgnoreMatcher) addRule(rawLine string) {
	line := strings.TrimSpace(rawLine)
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}
	var r ignoreRule
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = strings.TrimPrefix(line, "!")
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if strings.HasPrefix(line, "/") {
		r.anchored = true
		line = strings.TrimPrefix(line, "/")
	} else if strings.Contains(line, "/") {
		r.anchored = true
	}

	reStr := globToRegex(line, r.anchored)
	if compiled, err := regexp.Compile(reStr); err == nil {
		r.re = compiled
		m.rules = append(m.rules, r)
	}
}

// IsIgnored reports whether slash-normalized relative path relSlash is ignored.
func (m *IgnoreMatcher) IsIgnored(relSlash string, isDir bool) bool {
	relSlash = strings.TrimPrefix(filepath.ToSlash(relSlash), "./")
	if relSlash == "" || relSlash == "." {
		return false
	}
	if relSlash == ".git" || strings.HasPrefix(relSlash, ".git/") {
		return true
	}
	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			// Still check if an ancestor directory matches dirOnly rule
			parts := strings.Split(relSlash, "/")
			matchedAncestor := false
			for i := 1; i < len(parts); i++ {
				anc := strings.Join(parts[:i], "/")
				if r.re.MatchString(anc) {
					matchedAncestor = true
					break
				}
			}
			if matchedAncestor {
				ignored = !r.negate
			}
			continue
		}
		if r.re.MatchString(relSlash) {
			ignored = !r.negate
		}
	}
	return ignored
}

func globToRegex(pat string, anchored bool) string {
	var sb strings.Builder
	if anchored {
		sb.WriteString("^")
	} else {
		sb.WriteString("(?:^|/)")
	}
	i := 0
	for i < len(pat) {
		if i+1 < len(pat) && pat[i] == '*' && pat[i+1] == '*' {
			if i+2 < len(pat) && pat[i+2] == '/' {
				sb.WriteString("(?:.*/)?")
				i += 3
				continue
			}
			sb.WriteString(".*")
			i += 2
			continue
		}
		switch pat[i] {
		case '*':
			sb.WriteString("[^/]*")
		case '?':
			sb.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			sb.WriteByte('\\')
			sb.WriteByte(pat[i])
		default:
			sb.WriteByte(pat[i])
		}
		i++
	}
	sb.WriteString("(?:$|/)")
	return sb.String()
}

func compilePathGlob(pat string) (*regexp.Regexp, error) {
	pat = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(pat)), "./")
	var sb strings.Builder
	sb.WriteString("^")
	i := 0
	for i < len(pat) {
		if i+1 < len(pat) && pat[i] == '*' && pat[i+1] == '*' {
			if i+2 < len(pat) && pat[i+2] == '/' {
				sb.WriteString("(?:.*/)?")
				i += 3
				continue
			}
			sb.WriteString(".*")
			i += 2
			continue
		}
		switch pat[i] {
		case '*':
			sb.WriteString("[^/]*")
		case '?':
			sb.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			sb.WriteByte('\\')
			sb.WriteByte(pat[i])
		default:
			sb.WriteByte(pat[i])
		}
		i++
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// --- Glob Tool ---

type globArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// GlobTool finds files matching glob patterns while respecting .gitignore.
type GlobTool struct{}

func NewGlobTool() *GlobTool { return &GlobTool{} }

func (t *GlobTool) Name() string                      { return "glob" }
func (t *GlobTool) Category() string                  { return "system" }
func (t *GlobTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (t *GlobTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "glob",
		Category:    "system",
		Description: "Find files by glob pattern (for example `**/*.go` or `pkg/**/*.ts`). Fast, pure-Go traversal that automatically respects `.gitignore` and skips `.git`.\n\n- Use `glob` to locate files; prefer it over shell `find`.\n- Add `path` to scope the search to a subdirectory, and `limit` to cap large result sets.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Glob pattern such as '**/*.go' or '*.md'"},
				"path": {"type": "string", "description": "Optional subdirectory to search within"},
				"limit": {"type": "integer", "description": "Maximum matches to return (default 200)"}
			},
			"required": ["pattern"]
		}`),
	}
}

func (t *GlobTool) Execute(ctx context.Context, workDir string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args globArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid glob arguments: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return Result{Output: "pattern is required", IsError: true}, nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 200
	}

	searchRoot := workDir
	if searchRoot == "" {
		searchRoot = "."
	}
	if args.Path != "" {
		searchRoot = ResolvePath(workDir, args.Path)
	}

	re, err := compilePathGlob(args.Pattern)
	if err != nil {
		return Result{Output: "invalid glob pattern: " + err.Error(), IsError: true}, nil
	}
	baseRe, _ := compilePathGlob(args.Pattern)

	ign := LoadIgnoreMatcher(searchRoot)
	var matches []string

	err = filepath.WalkDir(searchRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(searchRoot, p)
		if err != nil || rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if ign.IsIgnored(relSlash, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		matched := re.MatchString(relSlash)
		if !matched && !strings.Contains(args.Pattern, "/") && baseRe != nil {
			matched = baseRe.MatchString(d.Name())
		}
		if matched {
			matches = append(matches, relSlash)
			if len(matches) >= limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return Result{Output: "glob canceled", IsError: true}, err
	}

	sort.Strings(matches)
	if len(matches) == 0 {
		return Result{Output: "(no matching files)"}, nil
	}
	return Result{Output: strings.Join(matches, "\n")}, nil
}

// --- Grep Tool ---

const (
	grepDefaultLimit   = 80
	grepMaxLineChars   = 240
	grepMaxFileBytes   = 4 * 1024 * 1024
)

type grepArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path,omitempty"`
	Include         string `json:"include,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	OutputMode      string `json:"output_mode,omitempty"` // "content" | "files_with_matches" | "count"
	Limit           int    `json:"limit,omitempty"`
}

// GrepTool searches text files using ripgrep (`rg`) when installed or pure-Go regular expressions as fallback,
// while skipping binaries and .gitignore entries.
type GrepTool struct{}

func NewGrepTool() *GrepTool { return &GrepTool{} }

func (t *GrepTool) Name() string                      { return "grep" }
func (t *GrepTool) Category() string                  { return "system" }
func (t *GrepTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (t *GrepTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "grep",
		Category:    "system",
		Description: "Search file contents with a regular expression (automatically accelerated with ripgrep when available, with pure-Go fallback). Automatically skips binary files, large files, `.git`, and `.gitignore` paths.\n\n- Prefer `grep` over shell `grep`/`rg`.\n- Use `output_mode: \"files_with_matches\"` first when locating files so results stay compact; use `output_mode: \"content\"` (default) for matching lines (each truncated at 240 chars) or `\"count\"` for per-file hit counts.\n- Use `include` to filter by filename (for example `*.go`), `path` to scope to a directory or single file, and `case_insensitive` when casing should not matter.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Regular expression pattern to search for"},
				"path": {"type": "string", "description": "Optional directory or single file path to search"},
				"include": {"type": "string", "description": "Optional filename filter glob such as '*.go'"},
				"case_insensitive": {"type": "boolean", "description": "Enable case-insensitive matching"},
				"output_mode": {"type": "string", "enum": ["content", "files_with_matches", "count"], "description": "Output mode: 'content' (matching lines, default), 'files_with_matches' (file paths only), or 'count' (match count per file)"},
				"limit": {"type": "integer", "description": "Maximum matching lines/files to return (default 80)"}
			},
			"required": ["pattern"]
		}`),
	}
}

func truncateGrepLine(line string, maxChars int) string {
	line = strings.TrimRight(line, "\r\n")
	if maxChars <= 0 || utf8.RuneCountInString(line) <= maxChars {
		return line
	}
	runes := []rune(line)
	return string(runes[:maxChars]) + "..."
}

func formatGrepDisplayPath(workDir, target string, isDir bool, rawPath string) string {
	base := target
	if !isDir {
		if workDir != "" {
			if r, err := filepath.Rel(workDir, target); err == nil {
				return filepath.ToSlash(r)
			}
		}
		return filepath.Base(target)
	}
	if r, err := filepath.Rel(base, rawPath); err == nil {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(rawPath)
}

func tryRipgrep(
	ctx context.Context,
	workDir string,
	target string,
	isDir bool,
	args grepArgs,
	outputMode string,
	limit int,
) (results []string, truncated bool, ok bool) {
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		return nil, false, false
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmdArgs := []string{"--no-heading", "--color=never", "--no-require-git", "--max-filesize=2M"}
	switch outputMode {
	case "files_with_matches":
		cmdArgs = append(cmdArgs, "--files-with-matches")
	case "count":
		cmdArgs = append(cmdArgs, "--count", "--with-filename")
	default:
		cmdArgs = append(cmdArgs, "--with-filename", "--line-number", "--no-column")
	}
	if args.CaseInsensitive {
		cmdArgs = append(cmdArgs, "-i")
	}
	if args.Include != "" {
		cmdArgs = append(cmdArgs, "-g", args.Include)
	}
	cmdArgs = append(cmdArgs, "--", args.Pattern, target)

	cmd := exec.CommandContext(cmdCtx, rgPath, cmdArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, false
	}
	if err := cmd.Start(); err != nil {
		return nil, false, false
	}

	sc := bufio.NewScanner(stdout)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 2*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r\n")
		if line == "" {
			continue
		}
		var formatted string
		switch outputMode {
		case "files_with_matches":
			formatted = formatGrepDisplayPath(workDir, target, isDir, line)
		case "count":
			if idx := strings.LastIndexByte(line, ':'); idx > 0 {
				rel := formatGrepDisplayPath(workDir, target, isDir, line[:idx])
				formatted = rel + line[idx:]
			} else {
				formatted = line
			}
		default:
			// rg format: <path>:<line>:<text>
			if idx1 := strings.IndexByte(line, ':'); idx1 > 0 {
				rest := line[idx1+1:]
				if idx2 := strings.IndexByte(rest, ':'); idx2 > 0 {
					rel := formatGrepDisplayPath(workDir, target, isDir, line[:idx1])
					lineNum := rest[:idx2]
					content := truncateGrepLine(rest[idx2+1:], grepMaxLineChars)
					formatted = fmt.Sprintf("%s:%s: %s", rel, lineNum, content)
				} else {
					formatted = truncateGrepLine(line, grepMaxLineChars)
				}
			} else {
				formatted = truncateGrepLine(line, grepMaxLineChars)
			}
		}
		if len(results) >= limit {
			truncated = true
			cancel()
			break
		}
		results = append(results, formatted)
	}

	waitErr := cmd.Wait()
	if truncated {
		return results, true, true
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return nil, false, false
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, false, true
		}
		return nil, false, false
	}
	return results, false, true
}

func (t *GrepTool) Execute(ctx context.Context, workDir string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args grepArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid grep arguments: " + err.Error(), IsError: true}, nil
	}
	if args.Pattern == "" {
		return Result{Output: "pattern is required", IsError: true}, nil
	}
	outputMode := strings.ToLower(strings.TrimSpace(args.OutputMode))
	if outputMode == "" {
		outputMode = "content"
	}
	switch outputMode {
	case "content", "files_with_matches", "count":
	default:
		return Result{Output: fmt.Sprintf("invalid output_mode %q: must be 'content', 'files_with_matches', or 'count'", args.OutputMode), IsError: true}, nil
	}

	limit := args.Limit
	if limit <= 0 {
		limit = grepDefaultLimit
	}

	target := workDir
	if target == "" {
		target = "."
	}
	if args.Path != "" {
		target = ResolvePath(workDir, args.Path)
	}

	info, err := os.Stat(target)
	if err != nil {
		return Result{Output: fmt.Sprintf("cannot access %s: %v", target, err), IsError: true}, nil
	}

	// Fast path: ripgrep (`rg`) when installed on the runner host.
	if rgResults, rgTruncated, ok := tryRipgrep(ctx, workDir, target, info.IsDir(), args, outputMode, limit); ok {
		if len(rgResults) == 0 {
			return Result{Output: "(no matches found)"}, nil
		}
		joined := strings.Join(rgResults, "\n")
		if rgTruncated {
			joined += fmt.Sprintf("\n\n[... results truncated at limit %d; narrow with path, include, or output_mode=\"files_with_matches\"]", limit)
		}
		inline, spillPath := SpillLargeOutput("grep", joined, defaultSpillMaxChars)
		var meta map[string]any
		if spillPath != "" {
			meta = map[string]any{"spill_path": spillPath, "total_matches": len(rgResults)}
		}
		return Result{Output: inline, Metadata: meta}, nil
	}

	// Pure-Go fallback when `rg` is absent or returned an error.
	expr := args.Pattern
	if args.CaseInsensitive && !strings.HasPrefix(expr, "(?i)") {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return Result{Output: "invalid regex pattern: " + err.Error(), IsError: true}, nil
	}

	var includeRe *regexp.Regexp
	if args.Include != "" {
		includeRe, err = compilePathGlob(args.Include)
		if err != nil {
			return Result{Output: "invalid include glob: " + err.Error(), IsError: true}, nil
		}
	}

	var results []string
	var truncated bool
	searchSingleFile := func(absFile, displayRel string) {
		f, err := os.Open(absFile)
		if err != nil {
			return
		}
		defer f.Close()

		// Sniff first 4KB for binary NUL byte
		var sniff [4096]byte
		n, _ := f.Read(sniff[:])
		if bytes.IndexByte(sniff[:n], 0) >= 0 {
			return
		}
		if _, err := f.Seek(0, 0); err != nil {
			return
		}

		sc := bufio.NewScanner(f)
		buf := make([]byte, 64*1024)
		sc.Buffer(buf, 2*1024*1024)
		lineNo := 0
		matchCount := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if !re.MatchString(line) {
				continue
			}
			switch outputMode {
			case "files_with_matches":
				if len(results) >= limit {
					truncated = true
					return
				}
				results = append(results, displayRel)
				return
			case "count":
				matchCount++
			default:
				if len(results) >= limit {
					truncated = true
					return
				}
				results = append(results, fmt.Sprintf("%s:%d: %s", displayRel, lineNo, truncateGrepLine(line, grepMaxLineChars)))
			}
		}
		if outputMode == "count" && matchCount > 0 {
			if len(results) >= limit {
				truncated = true
				return
			}
			results = append(results, fmt.Sprintf("%s:%d", displayRel, matchCount))
		}
	}

	if !info.IsDir() {
		searchSingleFile(target, formatGrepDisplayPath(workDir, target, false, target))
	} else {
		ign := LoadIgnoreMatcher(target)
		_ = filepath.WalkDir(target, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			rel, err := filepath.Rel(target, p)
			if err != nil || rel == "." {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			if ign.IsIgnored(relSlash, d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			if fi, err := d.Info(); err == nil && fi.Size() > grepMaxFileBytes {
				return nil
			}
			if includeRe != nil && !includeRe.MatchString(d.Name()) && !includeRe.MatchString(relSlash) {
				return nil
			}
			searchSingleFile(p, relSlash)
			if truncated {
				return fs.SkipAll
			}
			return nil
		})
	}

	if len(results) == 0 {
		return Result{Output: "(no matches found)"}, nil
	}
	joined := strings.Join(results, "\n")
	if truncated {
		joined += fmt.Sprintf("\n\n[... results truncated at limit %d; narrow with path, include, or output_mode=\"files_with_matches\"]", limit)
	}
	inline, spillPath := SpillLargeOutput("grep", joined, defaultSpillMaxChars)
	var meta map[string]any
	if spillPath != "" {
		meta = map[string]any{"spill_path": spillPath, "total_matches": len(results)}
	}
	return Result{Output: inline, Metadata: meta}, nil
}
