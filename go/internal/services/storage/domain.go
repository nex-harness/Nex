// Package storage is the storage module port (step 3.8): the resource
// manager "storage" tab's disk-usage scan and clean.
//
// Source: packages/services/src/storage/ (managed module). The TS layers the
// module as domain (pure catalog/aggregate/clean-plan), app (service + scan
// job + ports) and adapters (fs walker/cleaner/roots/volume probe). The Go
// package keeps the same split by file:
//
//	domain.go      ← storage/domain/{storageCatalog,usageAggregate}.ts
//	cleanplan.go   ← storage/domain/cleanPlan.ts
//	service.go     ← storage/app/{storageService,scanJob}.ts
//	ports.go       ← storage/app/ports.ts
//	walker.go      ← storage/adapters/fsWalker.ts
//	cleaner.go     ← storage/adapters/fsCleaner.ts
//	roots.go       ← storage/adapters/rootsResolver.ts
//	volumeprobe.go ← storage/adapters/volumeProbe.ts
//
// Like the TS contract.ts, this service is NOT an RPC channel: the desktop
// main holds one instance for the resource-manager window
// (IStorageService.onScanProgress is a local event, not an rpc.Channel
// listen).
package storage

import (
	"regexp"
	"sort"
	"strings"
)

// Category IDs, in STORAGE_CATEGORY_IDS order (packages/shared/src/storage.ts).
var CategoryIDs = []string{
	"sessionStore", "subagentTranscripts", "toolOutputs", "modelTrajectory",
	"devTraces", "logs", "backups", "exports", "runtimes", "config", "other",
}

// MoreEntriesPath ports STORAGE_MORE_ENTRIES_PATH ("…"): the fold entry used
// when a category's drill-down exceeds maxEntriesPerCategory.
const MoreEntriesPath = "…"

// RootID values (StorageRootId).
const (
	RootHome        = "home"
	RootDataBaseDir = "dataBaseDir"
)

// RootSpec ports StorageRootSpec.
type RootSpec struct {
	ID                   string `json:"id"`
	Path                 string `json:"path"`
	HasCustomDataBaseDir bool   `json:"hasCustomDataBaseDir"`
}

// Volume ports StorageVolume.
type Volume struct {
	DeviceID   string `json:"deviceId"`
	MountPoint string `json:"mountPoint"`
	TotalBytes int64  `json:"totalBytes"`
	FreeBytes  int64  `json:"freeBytes"`
}

// EntryUsage ports StorageEntryUsage.
type EntryUsage struct {
	RelativePath string `json:"relativePath"`
	Bytes        int64  `json:"bytes"`
	FileCount    int    `json:"fileCount"`
}

// CategoryUsage ports StorageCategoryUsage.
type CategoryUsage struct {
	ID           string       `json:"id"`
	Bytes        int64        `json:"bytes"`
	FileCount    int          `json:"fileCount"`
	Cleanability string       `json:"cleanability"`
	Entries      []EntryUsage `json:"entries"`
}

// RootUsage ports StorageRootUsage.
type RootUsage struct {
	ID         string          `json:"id"`
	Path       string          `json:"path"`
	Volume     *Volume         `json:"volume"`
	Bytes      int64           `json:"bytes"`
	FileCount  int             `json:"fileCount"`
	Categories []CategoryUsage `json:"categories"`
}

// PathError ports StoragePathError.
type PathError struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

// Snapshot ports StorageUsageSnapshot.
type Snapshot struct {
	JobID      string      `json:"jobId"`
	Status     string      `json:"status"` // scanning | complete | cancelled | failed
	StartedAt  int64       `json:"startedAt"`
	FinishedAt int64       `json:"finishedAt,omitempty"`
	Roots      []RootUsage `json:"roots"`
	Errors     []PathError `json:"errors"`
}

// cleanability table (CLEANABILITY in storageCatalog.ts).
var cleanability = map[string]string{
	"sessionStore": "none",
	// 只有 subagent 的 transcript.jsonl 可删；其余工具输出与临时缓存暂不可删。
	"subagentTranscripts": "safe",
	"toolOutputs":         "none",
	"modelTrajectory":     "safe",
	"devTraces":           "safe",
	"logs":                "safe",
	"backups":             "confirm",
	"exports":             "safe",
	"runtimes":            "none",
	"config":              "none",
	"other":               "none",
}

// fileRule ports FileRule: ordered, first match wins (backups/caches must
// precede the generic config rules).
type fileRule struct {
	categoryID       string
	pattern          *regexp.Regexp
	entryKeySegments int // 0 = full path
}

var fileRules = []fileRule{
	{"subagentTranscripts", regexp.MustCompile(`^cli/agents/[^/]+/[^/]+/transcript\.jsonl$`), 3},
	{"sessionStore", regexp.MustCompile(`^v2/[^/]+\.sqlite(?:-wal|-shm)?$`), 0},
	{"sessionStore", regexp.MustCompile(`^cli/db/db\.sqlite(?:-wal|-shm)?$`), 0},
	{"toolOutputs", regexp.MustCompile(`^v2/checkpoints/(?:.+\/)?(?:pending|tmp)/`), 0},
	{"backups", regexp.MustCompile(`^cli/db/db\.sqlite\.[^/]+$`), 0},
	{"backups", regexp.MustCompile(`^cli/config\.json\.bak[^/]*$`), 0},
	{"backups", regexp.MustCompile(`^v2/[^/]+\.bak$`), 0},
	{"backups", regexp.MustCompile(`^v2/[^/]+\.backup\.json$`), 0},
	{"backups", regexp.MustCompile(`^v2/setting\.json\.(?:corrupt-|[^/]*backup)[^/]*$`), 0},
	{"backups", regexp.MustCompile(`^v2/config\.json\.pre-[^/]+$`), 0},
	{"toolOutputs", regexp.MustCompile(`^v2/coding-plan-cache\.json$`), 0},
	// Bot 历史缓存仅供资源管理器识别展示，不加载配置或启动渠道。
	{"toolOutputs", regexp.MustCompile(`^v2/bots-model-cache[^/]*\.json$`), 0},
	{"logs", regexp.MustCompile(`^computer-use/run/[^/]+\.log$`), 0},
	{"config", regexp.MustCompile(`^v2/[^/]+\.json$`), 0},
	{"config", regexp.MustCompile(`^cli/config\.json$`), 0},
	{"config", regexp.MustCompile(`^agents/[^/]+\.md$`), 0},
	{"config", regexp.MustCompile(`^AGENTS\.md$`), 0},
}

// prefixRules ports PREFIX_RULES; the flattened index is sorted by descending
// prefix length so the LONGEST prefix wins.
var prefixRules = map[string][]string{
	"sessionStore": {"v2/sessions", "v2/session-bindings", "v2/checkpoints"},
	// transcript.jsonl 由上面的文件规则先命中，其余 cli/agents 内容留在这里
	"subagentTranscripts": nil,
	"toolOutputs": {
		"cli/artifacts", "cli/agents", "cli/sessions", "cli/exec",
		"cli/image-cache", "cli/pdf-cache", "clipboard",
		"git-checkpoint-index", "editor-icon", "tmp", "cache",
	},
	"modelTrajectory": {"cli/debug", "cli/rollout"},
	"devTraces":       {"v2/dev", "v2/acp-traffic-proxy", "v2/acp-stream-diagnostics"},
	"logs":            {"v2/logs", "cli/log", "logs", "v2/crash", "v2/perf", "feedback/logs"},
	"backups":         {"backup", "v2/backup", "v2/migrations", "cli/db/backup", "cli/db/backups"},
	"exports":         {"export-log", "export-log-stage", "feedback"},
	"runtimes":        {"agents", "bundled-agents", "lite", "computer-use", "cli/plugins"},
	// cli/plugins 整体（含 cache）不可清理，插件缓存归运行时。
	"config": {
		"v2/agent-config", "v2/bots-runtime-locks", "v2/bot-attachments", "v2/certs",
		"v2/acp-auth", "v2/acp-config", "v2/provider", "cli/models", "cli/memories",
		"cli/workflows", "security", "commands", "skills", "workspaces", "workspace",
		"mailbox", "server", "controller", "launcher", "dev-signing",
		"cua-helper-dev-identity", "perf-task-manifests", "plugin-workspace", "projects",
		// 桌面宠物目录（docs/specs/desktop-pets.md）：用户资产，不可清理。
		"pets",
	},
}

type prefixIndexEntry struct {
	prefix     string
	categoryID string
}

var prefixIndex = buildPrefixIndex()

func buildPrefixIndex() []prefixIndexEntry {
	var entries []prefixIndexEntry
	for category, prefixes := range prefixRules {
		for _, prefix := range prefixes {
			entries = append(entries, prefixIndexEntry{prefix, category})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if len(entries[i].prefix) != len(entries[j].prefix) {
			return len(entries[i].prefix) > len(entries[j].prefix)
		}
		return entries[i].prefix < entries[j].prefix
	})
	return entries
}

// 任何类别下都不能删除的文件：启动引导文件、凭据、Helper broker 凭据、
// 诊断开关、活动崩溃现场。
var protectedBasenames = map[string]bool{
	"setting.json":       true,
	"setting.json.lock":  true,
	"credentials.json":   true,
	".credentials.json":  true,
	".tokens":            true,
	"nex-stdio-tap.json": true,
}

var protectedPrefixes = []string{"v2/crash/live"}

// fileRuleScopes ports FILE_RULE_SCOPES: the directories file rules live in;
// clean enumerates these non-recursively.
var fileRuleScopes = map[string][]string{
	"backups": {"cli/db", "cli", "v2"},
	"logs":    {"computer-use/run"},
}

// recursiveFileRuleScopes ports RECURSIVE_FILE_RULE_SCOPES: categories that
// rely only on file rules but need recursive enumeration.
var recursiveFileRuleScopes = map[string][]string{
	"subagentTranscripts": {"cli/agents"},
}

// CleanScope ports StorageCleanScope.
type CleanScope struct {
	Prefix    string
	Recursive bool
}

// CatalogContext ports StorageCatalogContext.
type CatalogContext struct {
	RootID               string
	HasCustomDataBaseDir bool
}

// Classification ports StorageClassification.
type Classification struct {
	CategoryID string
	EntryKey   string
}

// NormalizeRelativePath ports normalizeStorageRelativePath: backslashes to
// slashes, surrounding slashes trimmed.
func NormalizeRelativePath(relativePath string) string {
	path := strings.ReplaceAll(relativePath, `\`, "/")
	return strings.Trim(path, "/")
}

func isUnderPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func entryKeyBelow(path, prefix string) string {
	if path == prefix {
		return prefix
	}
	rest := path[len(prefix)+1:]
	next := strings.SplitN(rest, "/", 2)[0]
	if next == "" {
		return prefix
	}
	return prefix + "/" + next
}

// ClassifyStoragePath ports classifyStoragePath: root-level special cases →
// file rules (exact) → prefix rules (longest prefix) → other.
func ClassifyStoragePath(rawPath string, context CatalogContext) Classification {
	path := NormalizeRelativePath(rawPath)
	// 启用自定义数据路径后，home 根下的 v2 是迁移遗留的旧副本，整体归「其他」。
	if context.RootID == RootHome && context.HasCustomDataBaseDir && isUnderPrefix(path, "v2") {
		return Classification{"other", "v2"}
	}
	// agent/ 是 ACP 时代残留，当前代码无写入方，同样归「其他」。
	if isUnderPrefix(path, "agent") {
		return Classification{"other", "agent"}
	}
	for _, rule := range fileRules {
		if rule.pattern.MatchString(path) {
			if rule.entryKeySegments > 0 {
				return Classification{rule.categoryID, strings.Join(strings.SplitN(path, "/", rule.entryKeySegments+1)[:rule.entryKeySegments], "/")}
			}
			return Classification{rule.categoryID, path}
		}
	}
	for _, entry := range prefixIndex {
		if isUnderPrefix(path, entry.prefix) {
			return Classification{entry.categoryID, entryKeyBelow(path, entry.prefix)}
		}
	}
	first := strings.SplitN(path, "/", 2)[0]
	if first == "" {
		first = path
	}
	return Classification{"other", first}
}

// CleanabilityOf ports getStorageCategoryCleanability.
func CleanabilityOf(categoryID string) string {
	if c, ok := cleanability[categoryID]; ok {
		return c
	}
	return "none"
}

// IsProtectedPath ports isProtectedStoragePath.
func IsProtectedPath(rawPath string) bool {
	path := NormalizeRelativePath(rawPath)
	basename := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		basename = path[idx+1:]
	}
	if protectedBasenames[basename] {
		return true
	}
	for _, prefix := range protectedPrefixes {
		if isUnderPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// CleanScopes ports getStorageCleanScopes.
func CleanScopes(categoryID string) []CleanScope {
	if categoryID == "other" || cleanability[categoryID] == "none" {
		return nil
	}
	var scopes []CleanScope
	for _, prefix := range append(append([]string{}, prefixRules[categoryID]...), recursiveFileRuleScopes[categoryID]...) {
		scopes = append(scopes, CleanScope{Prefix: prefix, Recursive: true})
	}
	for _, prefix := range fileRuleScopes[categoryID] {
		scopes = append(scopes, CleanScope{Prefix: prefix, Recursive: false})
	}
	return scopes
}
