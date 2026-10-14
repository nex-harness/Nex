package file

// mentionfilter.go ports workspaceFileMentionFilter.ts — the final
// include/traverse decision every indexed entry passes through. The built-in
// directory blacklist only applies when the .nexignore rules are NOT active
// (fail-open); the file-level skips always apply.

import (
	"path"
	"strings"
)

var skippedDirectoryNames = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true,
	"bower_components": true, "jspm_packages": true, "__pycache__": true,
	"site-packages": true, "venv": true, "coverage": true, "htmlcov": true,
	"lcov-report": true, "cmakefiles": true, "pods": true, "deriveddata": true,
	"storybook-static": true, "playwright-report": true, "test-results": true,
	"allure-results": true, "allure-report": true, "cdk.out": true,
	"eggs": true, "pip-wheel-metadata": true, "wheels": true,
}

var skippedDirectoryPrefixes = []string{"cmake-build-", "bazel-"}

var skippedDirectorySuffixes = []string{".egg-info", ".dist-info"}

var skippedFileNames = map[string]bool{"coverage.out": true, "lcov.info": true}

var skippedFileExtensions = map[string]bool{
	".a": true, ".aar": true, ".beam": true, ".class": true, ".dll": true,
	".dylib": true, ".ear": true, ".exe": true, ".gcda": true, ".gcno": true,
	".gem": true, ".hi": true, ".idb": true, ".ilk": true, ".jar": true,
	".lib": true, ".node": true, ".nupkg": true, ".o": true, ".obj": true,
	".pdb": true, ".profdata": true, ".profraw": true, ".pyc": true,
	".pyo": true, ".rlib": true, ".so": true, ".tsbuildinfo": true, ".war": true,
}

// searchDecision is WorkspaceFileSearchDecision.
type searchDecision struct {
	include  bool
	traverse bool
}

// searchFilter is WorkspaceFileSearchFilter — injectable per the TS note
// (后续自定义规则应注入另一份完整实现来替换默认实现).
type searchFilter interface {
	evaluate(entry workspaceEntry, ignoreRulesActive bool) searchDecision
}

// defaultSearchFilter is defaultWorkspaceFileSearchFilter.
type defaultSearchFilter struct{}

func shouldSkipDirectory(name string) bool {
	n := strings.ToLower(name)
	if skippedDirectoryNames[n] {
		return true
	}
	for _, p := range skippedDirectoryPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, s := range skippedDirectorySuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

func shouldSkipFile(name string) bool {
	n := strings.ToLower(name)
	if n == ".env" || strings.HasPrefix(n, ".env.") {
		return true
	}
	if skippedFileNames[n] {
		return true
	}
	return skippedFileExtensions[strings.ToLower(path.Ext(n))]
}

func isInsideHiddenDirectory(relativePath string) bool {
	segs := strings.Split(relativePath, "/")
	for _, seg := range segs[:len(segs)-1] {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

func (defaultSearchFilter) evaluate(entry workspaceEntry, ignoreRulesActive bool) searchDecision {
	if entry.Type == "directory" {
		// ignoreRulesActive 时目录黑名单退役：目录排除的唯一来源是 .nexignore。
		if !ignoreRulesActive && shouldSkipDirectory(entry.Name) {
			return searchDecision{include: false, traverse: false}
		}
		// 隐藏目录不占候选，但保留遍历，让 .github 等目录中的普通文件进入索引。
		hidden := strings.HasPrefix(entry.Name, ".") || isInsideHiddenDirectory(entry.RelativePath)
		return searchDecision{include: !hidden, traverse: true}
	}
	return searchDecision{include: !shouldSkipFile(entry.Name), traverse: false}
}
