// Package paths is the Go port of packages/services/src/paths.ts (step 3.1).
//
// 数据目录解析的唯一所有者：explicit setter > NEX_DATA_BASE_DIR > HOME。每个进程持有
// 一份快照（Default），env 读取发生在构造时而不是每次调用时 —— paths.ts 的注释说明了
// 原因：服务实例会启动后台刷新任务，若每次调用都动态读取 HOME，测试或宿主切换环境
// 变量后，旧实例可能把数据写到新实例目录。
//
// The pure Windows comparison helpers (normalizeWindowsComparablePath,
// isWindowsPathEqualOrInside, collectWindowsForbiddenAppInstallDirs,
// ValidateDataBaseDirTarget) are byte-for-byte ports and are pure functions on
// (platform, env) so a Linux CI run still exercises every Windows drive/UNC case
// (windows.go / windows_test.go).
package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// NEX_DATA_BASE_DIR is the environment override for the data base directory.
// It already exists in the TypeScript server — the Go rewrite adds none (D7).
const NEX_DATA_BASE_DIR = "NEX_DATA_BASE_DIR"

// DataBaseDirForbiddenWindowsInstallDirErrorCode is
// DATA_BASE_DIR_FORBIDDEN_WINDOWS_INSTALL_DIR_ERROR_CODE from
// packages/shared/src/settings-errors.ts; the UI branches on this exact string.
const DataBaseDirForbiddenWindowsInstallDirErrorCode = "DATA_BASE_DIR_FORBIDDEN_WINDOWS_INSTALL_DIR"

// Paths owns the resolved data base dir. The zero value is NOT usable; call New.
// All methods are safe for concurrent use.
type Paths struct {
	mu sync.Mutex
	// explicit is the setDataBaseDir() override ("" = none).
	explicit string
	// envDataBaseDir and homeDir are process-lifetime snapshots, matching the
	// module-level consts captured at import time in paths.ts.
	envDataBaseDir string
	homeDir        string
}

// New captures the process environment once, exactly like paths.ts's
// `const envDataBaseDir = …` / `const defaultDataBaseDir = …` module evaluation.
// Both inputs are trimmed here — the TS trims HOME and NEX_DATA_BASE_DIR at
// capture time. homeDir is `process.env.HOME?.trim() || homedir()` (homedir()
// itself is already untrimmed-free).
func New(envBaseDir, homeDir string) *Paths {
	return &Paths{
		envDataBaseDir: strings.TrimSpace(envBaseDir),
		homeDir:        strings.TrimSpace(homeDir),
	}
}

// NewFromProcess builds Paths from the real environment. It reads env/HOME here so
// the rest of the package stays a pure consumer (os confined to this constructor,
// matching how serverenv.HomeDir is the sanctioned accessor elsewhere in the tree).
func NewFromProcess() *Paths {
	//nolint:forbidigo // this package owns the paths.ts port; the read happens once, here.
	envBaseDir := os.Getenv(NEX_DATA_BASE_DIR)
	home := strings.TrimSpace(os.Getenv("HOME")) //nolint:forbidigo
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = userHome
		}
	}
	return New(envBaseDir, home)
}

// SetDataBaseDir ports setDataBaseDir: an empty/whitespace value clears the override.
func (p *Paths) SetDataBaseDir(dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.explicit = strings.TrimSpace(dir)
}

// DataBaseDir ports getDataBaseDir: setter > NEX_DATA_BASE_DIR > HOME snapshot.
func (p *Paths) DataBaseDir() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.explicit != "" {
		return p.explicit
	}
	if p.envDataBaseDir != "" {
		return p.envDataBaseDir
	}
	return p.homeDir
}

// NexDataRootDir ports getNexDataRootDir: {dataBaseDir}/.nex. The legacy `.zcode`
// copy-once migration (copyLegacyNexHomeOnce) is a host-side bootstrap concern and
// is intentionally NOT ported here — see decisions in the step-3.1 notes.
func (p *Paths) NexDataRootDir() string {
	return filepath.Join(p.DataBaseDir(), ".nex")
}

// AppConfigDir ports getAppConfigDir: {dataBaseDir}/.nex/v2.
func (p *Paths) AppConfigDir() string {
	return filepath.Join(p.NexDataRootDir(), "v2")
}

// ConversationWorkspaceDir ports getConversationWorkspaceDir.
func (p *Paths) ConversationWorkspaceDir() string {
	return filepath.Join(p.NexDataRootDir(), "workspace", "default")
}

// ExportLogStageDir / ExportLogDir / FeedbackRootDir / FeedbackAttachmentDir /
// FeedbackLogArchiveDir / GitCheckpointIndexRootDir port the same-named getters.
func (p *Paths) ExportLogStageDir() string {
	return filepath.Join(p.NexDataRootDir(), "export-log-stage")
}
func (p *Paths) ExportLogDir() string    { return filepath.Join(p.NexDataRootDir(), "export-log") }
func (p *Paths) FeedbackRootDir() string { return filepath.Join(p.NexDataRootDir(), "feedback") }
func (p *Paths) FeedbackAttachmentDir() string {
	return filepath.Join(p.FeedbackRootDir(), "attachments")
}
func (p *Paths) FeedbackLogArchiveDir() string { return filepath.Join(p.FeedbackRootDir(), "logs") }
func (p *Paths) GitCheckpointIndexRootDir() string {
	return filepath.Join(p.NexDataRootDir(), "git-checkpoint-index")
}

// TasksIndexDatabasePath ports getTasksIndexDatabasePath:
// {dataBaseDir}/.nex/v2/tasks-index.sqlite.
func (p *Paths) TasksIndexDatabasePath() string {
	return filepath.Join(p.AppConfigDir(), "tasks-index.sqlite")
}

// taskSessionDir ports getTaskSessionDir: ~/.nex/v2/sessions/{workspaceHash}.
func (p *Paths) taskSessionDir(workspacePath, workspaceIdentity string) string {
	return filepath.Join(p.AppConfigDir(), "sessions", WorkspaceHash(workspacePath, workspaceIdentity))
}

// LegacyTaskSessionSnapshotPath ports getLegacyTaskSessionSnapshotPath:
// ~/.nex/v2/sessions/{workspaceHash}/{taskId}.json.
func (p *Paths) LegacyTaskSessionSnapshotPath(workspacePath, taskID, workspaceIdentity string) string {
	return filepath.Join(p.taskSessionDir(workspacePath, workspaceIdentity), taskID+".json")
}

// LegacyDeletedTaskSessionSnapshotPath ports getLegacyDeletedTaskSessionSnapshotPath:
// ~/.nex/v2/sessions/{workspaceHash}/{taskId}.deleted.json.
func (p *Paths) LegacyDeletedTaskSessionSnapshotPath(workspacePath, taskID, workspaceIdentity string) string {
	return filepath.Join(p.taskSessionDir(workspacePath, workspaceIdentity), taskID+".deleted.json")
}

// WorkspaceKey is the AGENTS.md identity rule: 远程优先使用 workspaceIdentity，
// 本地回退 workspacePath。身份 key 统一为 `workspaceIdentity?.trim() || workspacePath`，
// 适用于去重、绑定、缓存、队列、持久化和请求关联。
func WorkspaceKey(workspacePath, workspaceIdentity string) string {
	if key := strings.TrimSpace(workspaceIdentity); key != "" {
		return key
	}
	return workspacePath
}

// WorkspaceHash ports getWorkspaceHash: SHA-256 of the workspace key, first 12
// hex chars — the same hash Nex session persistence uses.
func WorkspaceHash(workspacePath, workspaceIdentity string) string {
	sum := sha256.Sum256([]byte(WorkspaceKey(workspacePath, workspaceIdentity)))
	return hex.EncodeToString(sum[:])[:12]
}
