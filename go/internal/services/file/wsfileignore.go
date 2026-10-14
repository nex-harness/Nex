package file

// wsfileignore.go ports packages/services/src/file/workspaceFileIgnore.ts —
// the .nexignore single source of truth: loading with the auto-create and
// fail-open degradation chain, the section markers for the settings-page
// transforms, and the atomic write. The gitignore matching itself is the
// ignore.go port; this file only owns file content and sectioning.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IgnoreFileName is WORKSPACE_FILE_SEARCH_IGNORE_FILE_NAME.
const IgnoreFileName = ".nexignore"

const gitignoreFileName = ".gitignore"

// IgnoreRules is WorkspaceFileSearchIgnoreRules.
type IgnoreRules struct {
	matcher *matcher
	// Source is WorkspaceFileSearchIgnoreRulesSource.
	Source string // file | created-from-gitignore | created-from-template | fallback-gitignore | fallback-builtin
}

// builtinIgnoreLines is BUILTIN_IGNORE_LINES.
var builtinIgnoreLines = []string{
	".git/", ".hg/", ".svn/", "node_modules/", "bower_components/", "jspm_packages/",
	"__pycache__/", "site-packages/", "venv/", "coverage/", "htmlcov/", "lcov-report/",
	"cmakefiles/", "cmake-build-*/", "bazel-*/", "pods/", "deriveddata/",
	"storybook-static/", "playwright-report/", "test-results/", "allure-results/",
	"allure-report/", "cdk.out/", "*.egg-info/", "*.dist-info/", "eggs/",
	"pip-wheel-metadata/", "wheels/",
}

var templateHeader = []string{
	"# Nex 工作区文件搜索忽略规则（.nexignore）",
	"# 语法与 .gitignore 一致，只影响 Nex 的 @ 文件候选 / Command Center / 文件树搜索，",
	"# 不影响文件树浏览、上传或 Agent 文件访问。",
	"# 修改 .gitignore 不会自动同步到本文件；可在设置页「从 .gitignore 同步」。",
	"",
}

const (
	syncMarker     = "# ===== ↑ 以上同步自 .gitignore（「从 .gitignore 同步」只重写以上部分）====="
	defaultsMarker = "# ----- ↑ 以上为 Nex 默认排除规则（自定义规则请写在本行下方，不会被同步/恢复改动）-----"
	customHint     = "# 自定义规则写在下方（本行提示可删除）"
)

// buildBuiltinDefaultsSection ports buildBuiltinDefaultsSection: dedupe lines
// the gitignore section already declares (exact line, or ignoring one
// trailing slash).
func buildBuiltinDefaultsSection(gitignoreContent *string) string {
	if gitignoreContent == nil {
		return strings.Join(builtinIgnoreLines, "\n")
	}
	declared := map[string]bool{}
	for _, raw := range splitLines(*gitignoreContent) {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		declared[line] = true
		declared[strings.TrimSuffix(line, "/")] = true
	}
	var out []string
	for _, line := range builtinIgnoreLines {
		bare := strings.TrimSuffix(line, "/")
		if !declared[line] && !declared[bare] {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// buildIgnoreTemplate ports buildWorkspaceFileSearchIgnoreTemplate.
func buildIgnoreTemplate(gitignoreContent *string) string {
	var gitignoreSection string
	if gitignoreContent != nil && len(strings.TrimSpace(*gitignoreContent)) > 0 {
		gitignoreSection = *gitignoreContent
		if !strings.HasSuffix(gitignoreSection, "\n") {
			gitignoreSection += "\n"
		}
	} else {
		gitignoreSection = strings.Join(templateHeader, "\n") + "\n"
	}
	return strings.Join([]string{
		gitignoreSection,
		syncMarker,
		buildBuiltinDefaultsSection(gitignoreContent),
		defaultsMarker,
		customHint,
		"",
	}, "\n")
}

// splitIgnoreSections ports splitWorkspaceFileSearchIgnoreSections: nil when
// either marker is missing (old format / user deletion) — callers then fall
// back to a full rebuild.
type ignoreSections struct {
	gitignoreSection string
	defaultsSection  string
	customSection    string
}

// splitLines ports `content.split(/\r?\n/)`.
func splitLines(content string) []string {
	var out []string
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			line := content[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	if start <= len(content) {
		line := content[start:]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		out = append(out, line)
	}
	return out
}

func splitIgnoreSections(content string) *ignoreSections {
	lines := splitLines(content)
	syncIndex, defaultsIndex := -1, -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == syncMarker && syncIndex == -1 {
			syncIndex = i
		}
		if t == defaultsMarker && defaultsIndex == -1 {
			defaultsIndex = i
		}
	}
	if syncIndex == -1 || defaultsIndex == -1 || defaultsIndex <= syncIndex {
		return nil
	}
	return &ignoreSections{
		gitignoreSection: strings.Join(lines[:syncIndex], "\n"),
		defaultsSection:  strings.TrimSpace(strings.Join(lines[syncIndex+1:defaultsIndex], "\n")),
		customSection:    trimLeadingNewlines(strings.Join(lines[defaultsIndex+1:], "\n")),
	}
}

// collapseTrailingNewlines ports `.replace(/\n+\$/, "\n")`.
func collapseTrailingNewlines(s string) string {
	end := len(s)
	for end > 0 && s[end-1] == '\n' {
		end--
	}
	if end == 0 {
		return ""
	}
	return s[:end] + "\n"
}

// trimLeadingNewlines ports `.replace(/^\n+/, "")`.
func trimLeadingNewlines(s string) string {
	return strings.TrimLeft(s, "\n")
}

// syncFromGitignore ports syncWorkspaceFileSearchIgnoreFromGitignore.
func syncFromGitignore(currentContent string, gitignoreContent *string) string {
	sections := splitIgnoreSections(currentContent)
	if sections == nil {
		return buildIgnoreTemplate(gitignoreContent)
	}
	var gitignoreSection string
	if gitignoreContent != nil && len(strings.TrimSpace(*gitignoreContent)) > 0 {
		gitignoreSection = *gitignoreContent
		if !strings.HasSuffix(gitignoreSection, "\n") {
			gitignoreSection += "\n"
		}
	} else {
		gitignoreSection = strings.Join(templateHeader, "\n") + "\n"
	}
	return collapseTrailingNewlines(strings.Join([]string{
		gitignoreSection,
		syncMarker,
		sections.defaultsSection,
		defaultsMarker,
		sections.customSection,
	}, "\n") + "\n")
}

// resetDefaults ports resetWorkspaceFileSearchIgnoreDefaults.
func resetDefaults(currentContent string, gitignoreContent *string) string {
	sections := splitIgnoreSections(currentContent)
	if sections == nil {
		return buildIgnoreTemplate(gitignoreContent)
	}
	return collapseTrailingNewlines(strings.Join([]string{
		sections.gitignoreSection,
		syncMarker,
		buildBuiltinDefaultsSection(&sections.gitignoreSection),
		defaultsMarker,
		sections.customSection,
	}, "\n") + "\n")
}

// readOptionalFile returns (content, nil) or ("", nil) when missing; other
// errors propagate.
func readOptionalFile(path string) (*string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	s := string(data)
	return &s, nil
}

// atomicWriteIgnoreFile ports atomicWriteIgnoreFile: temp file in the target
// directory (exclusive create, flush, close) then rename.
func atomicWriteIgnoreFile(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// loadIgnoreRules ports loadWorkspaceFileSearchIgnoreRules, including the
// fail-open degradation chain. logger may be nil.
func loadIgnoreRules(rootPath string, logger serviceLogger) (*IgnoreRules, error) {
	ignorePath := filepath.Join(rootPath, IgnoreFileName)

	degrade := func(reason string, err error) (*IgnoreRules, error) {
		gitignore, gerr := readOptionalFile(filepath.Join(rootPath, gitignoreFileName))
		if gerr != nil {
			gitignore = nil
		}
		if gitignore != nil {
			logger.warn(fmt.Sprintf("[workspace-file-ignore] %s，降级为运行时使用 .gitignore 规则", reason), err)
			return &IgnoreRules{matcher: newMatcher().Add(*gitignore), Source: "fallback-gitignore"}, nil
		}
		logger.warn(fmt.Sprintf("[workspace-file-ignore] %s，降级为内置默认忽略规则", reason), err)
		return &IgnoreRules{matcher: newMatcher().Add(buildIgnoreTemplate(nil)), Source: "fallback-builtin"}, nil
	}

	existing, err := readOptionalFile(ignorePath)
	if err != nil {
		return degrade(fmt.Sprintf("读取 %s 失败", IgnoreFileName), err)
	}
	if existing != nil {
		return &IgnoreRules{matcher: newMatcher().Add(*existing), Source: "file"}, nil
	}

	gitignore, gerr := readOptionalFile(filepath.Join(rootPath, gitignoreFileName))
	if gerr != nil {
		gitignore = nil
	}
	initial := buildIgnoreTemplate(gitignore)
	if err := atomicWriteIgnoreFile(ignorePath, initial); err != nil {
		// 创建失败不影响扫描：初始内容确定性已知，内存中直接按初始内容执行。
		logger.warn(fmt.Sprintf("[workspace-file-ignore] 自动创建 %s 失败，降级为运行时使用%s规则",
			IgnoreFileName, gitignoreSourceLabel(gitignore)), err)
		return &IgnoreRules{matcher: newMatcher().Add(initial), Source: fallbackSource(gitignore)}, nil
	}
	logger.info(fmt.Sprintf("[workspace-file-ignore] 已自动创建 %s（来源：%s）",
		IgnoreFileName, gitignoreSourceLabel(gitignore)))
	return &IgnoreRules{matcher: newMatcher().Add(initial), Source: createdSource(gitignore)}, nil
}

func gitignoreSourceLabel(gitignore *string) string {
	if gitignore != nil {
		return ".gitignore 拷贝"
	}
	return "默认模板"
}

func createdSource(gitignore *string) string {
	if gitignore != nil {
		return "created-from-gitignore"
	}
	return "created-from-template"
}

func fallbackSource(gitignore *string) string {
	if gitignore != nil {
		return "fallback-gitignore"
	}
	return "fallback-builtin"
}

// isPathIgnored ports isWorkspaceFileSearchPathIgnored: directories are
// probed with a trailing slash so gitignore dir-only rules match.
func (r *IgnoreRules) isPathIgnored(relativePath, entryType string) bool {
	if entryType == "directory" {
		return r.matcher.ignores(relativePath + "/")
	}
	return r.matcher.ignores(relativePath)
}

// readIgnoreFile ports readWorkspaceFileSearchIgnore: template preview when
// the file does not exist (no write).
func readIgnoreFile(rootPath string) (content string, source string, err error) {
	existing, err := readOptionalFile(filepath.Join(rootPath, IgnoreFileName))
	if err != nil {
		return "", "", err
	}
	if existing != nil {
		return *existing, "file", nil
	}
	gitignore, gerr := readOptionalFile(filepath.Join(rootPath, gitignoreFileName))
	if gerr != nil {
		gitignore = nil
	}
	return buildIgnoreTemplate(gitignore), "template", nil
}

// transformIgnoreFile ports transformWorkspaceFileSearchIgnore.
func transformIgnoreFile(rootPath, transform string) (string, error) {
	existing, err := readOptionalFile(filepath.Join(rootPath, IgnoreFileName))
	if err != nil {
		existing = nil
	}
	gitignore, gerr := readOptionalFile(filepath.Join(rootPath, gitignoreFileName))
	if gerr != nil {
		gitignore = nil
	}
	current := buildIgnoreTemplate(gitignore)
	if existing != nil {
		current = *existing
	}
	if transform == "sync-gitignore" {
		return syncFromGitignore(current, gitignore), nil
	}
	return resetDefaults(current, gitignore), nil
}

// writeIgnoreFile ports writeWorkspaceFileSearchIgnore.
func writeIgnoreFile(rootPath, content string) error {
	return atomicWriteIgnoreFile(filepath.Join(rootPath, IgnoreFileName), content)
}
