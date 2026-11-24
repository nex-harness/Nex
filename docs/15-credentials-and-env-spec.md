# Nex-v3 CLI 凭证与环境变量管理（多 Runner 架构）技术规范

- **状态**: Approved / In Implementation
- **更新日期**: 2026-04-20
- **Worktree / 分支**: `/Users/tylinux/Developer/Projects/Nex-v3-wt-credentials` (`feat/cli-credentials-vault`)
- **关联文档**:
  - [01-architecture-spec.md](./01-architecture-spec.md)
  - [02-client-and-protocol-spec.md](./02-client-and-protocol-spec.md)
  - [04-mcp-and-codemode-spec.md](./04-mcp-and-codemode-spec.md)
  - [06-storage-and-observability-spec.md](./06-storage-and-observability-spec.md)
- **设计目标**: 解决日常开发与无人值守定时任务（Cron）场景下使用本地 `.env` 文件不够安全、编辑繁琐、在多 Runner 异构节点间难以隔离与同步的问题，建立一套 **“分组管理 + 静态加密 + 写后不可回读 + 多 Runner 定向分发/本地直存 + 按场景最小化注入 + 流式/日志全链路自动脱敏”** 的纯 Go（`CGO_ENABLED=0`）凭证管理体系。

---

## 1. 方案 Review 与核心架构决策

### 1.1 针对多 Runner 架构的边界约束
1. **严格纯 Go 零 CGO**：所有加解密（AES-256-GCM + HKDF/随机主密钥文件 `0600`）均使用 Go 标准库 `crypto/aes`、`crypto/cipher`、`crypto/rand` 实现，保证 `CGO_ENABLED=0` 跨平台编译（macOS / Linux / Windows）。
2. **数据面与控制面分离**：
   - **`server_synced`（默认）**：Server 使用本机独立主密钥文件（`<dataDir>/vault.key`，权限 `0600`）对凭证值做 AES-256-GCM 加密落库；通过 `ConfigSnapshot` 仅定向下发给命中 `target_runners` 的节点。Runner 收到后以自身 `<runnerDataDir>/vault.key` 加密落盘，确保离线 Cron 依然可用；当 Runner 被移出授权列表或凭证被删除时，同步快照自动清理该节点上的过期副本。
   - **`runner_local`（零 Server 落盘）**：Server 仅保存元数据骨架（分组名、变量名、目标 Runner、配置状态），绝不存储真实值。用户在 Settings UI 编辑某个 Runner 的本地值时，Server 通过 WebSocket RPC（`credential.local_set` / `credential.local_delete`）实时透传给目标 Runner 写入其本地加密库。
3. **写后不可回读（Write-Only API）**：
   - REST API `GET /api/v1/credentials` 永不返回明文值，仅返回 `has_value: true`、安全掩码提示（如 `ghp_••••••a1b2`，长度 < 10 时仅返回 `••••••••`）与更新时间。
   - 更新分组时，若某变量条目 `value` 为空且 `clear_value` 为 `false`，服务端自动保留既有密文，避免前端误覆盖。
4. **防泄露优先级高于一切**：
   - 任何通过本体系注入的环境变量值（长度 >= 6 的非空值，及其 URL 编码、Base64 编码变体），都会注册进本次执行的 **`SecretRedactor`**。
   - 脱敏覆盖：`BashTool` 最终输出、`onUpdate` 实时流式 delta（带重叠尾部缓冲，防止密钥跨 chunk 边界漏脱敏）、`spillFile` 落盘日志、`BackgroundTaskManager` 缓冲。
   - 替换格式为 `***[REDACTED:<ENV_KEY>]***`，既不泄露值，又让模型明确知道对应变量已注入但被隐藏。
5. **Prompt 最小暴露（只告名、不给值）**：
   - 在 `meta_context` 中自动注入一行只读摘要，例如：`Configured runtime CLI env keys: GH_TOKEN, SENTRY_AUTH_TOKEN (values are injected automatically and redacted from output; do not run env/printenv to inspect them).`
   - 模型无需翻找 `.env`，也无需询问用户复制粘贴密钥。

---

## 2. 数据模型设计

### 2.1 SQLite 表结构 (`credentials` + `credential_local_values`)

在 `main.db`（Server 与 Runner 均复用 `pkg/store`）中新增两张表：

```sql
CREATE TABLE IF NOT EXISTS credentials (
    id                  TEXT PRIMARY KEY,
    name                TEXT UNIQUE NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    preset              TEXT NOT NULL DEFAULT '',           -- "" | "github" | "aws" | "cloudflare" | "kubectl" | "npm" | "docker" | "sentry" | "tavily"
    storage_mode        TEXT NOT NULL DEFAULT 'server_synced', -- "server_synced" | "runner_local"
    usage_policy        TEXT NOT NULL DEFAULT 'all',        -- "all" | "chat_only" | "cron_explicit"
    scope               TEXT NOT NULL DEFAULT 'global',     -- "global" | "workspace" | "runner"
    workspace_path      TEXT NOT NULL DEFAULT '',
    target_runners_json TEXT NOT NULL DEFAULT '[]',         -- 空表示全部 runner（除非 scope == "runner"）
    entries_json        TEXT NOT NULL DEFAULT '[]',         -- 加密后的条目列表
    runner_overrides_json TEXT NOT NULL DEFAULT '{}',       -- runner_id -> 加密后的覆盖条目 map
    probe_command       TEXT NOT NULL DEFAULT '',           -- 可选只读验活命令（如 "gh auth status"）
    enabled             INTEGER NOT NULL DEFAULT 1,
    updated_at          INTEGER NOT NULL
);
```

对于 `runner_local` 模式，Runner 本地还维护一张极简加密表（仅存在 Runner 本地 `main.db`）：

```sql
CREATE TABLE IF NOT EXISTS credential_local_values (
    credential_id TEXT NOT NULL,
    env_key       TEXT NOT NULL,
    encrypted_val TEXT NOT NULL,
    value_hint    TEXT NOT NULL DEFAULT '',
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (credential_id, env_key)
);
```

### 2.2 Cron 任务扩展 (`cron_jobs`)
在现有 `cron_jobs` 表中增加幂等迁移列：
```sql
ALTER TABLE cron_jobs ADD COLUMN credential_ids_json TEXT NOT NULL DEFAULT '[]';
```
- `credential_ids` 为空时：Cron 仅自动注入 `usage_policy == "all"` 且命中当前 `(runnerID, workspacePath)` 的启用凭证。
- `credential_ids` 非空时：除上述全局基础凭证外，额外注入被该 Cron 显式授权的 `cron_explicit` 凭证（`chat_only` 凭证永远不注入 Cron）。

### 2.3 Go 结构体定义

```go
type CredentialEntry struct {
    Key          string `json:"key"`                      // 合法环境变量名 [A-Za-z_][A-Za-z0-9_]*
    Value        string `json:"value,omitempty"`          // 仅写入/下发瞬态使用，API GET 永远清空
    EncryptedVal string `json:"encrypted_val,omitempty"`  // AES-256-GCM base64 密文（落库）
    ValueHint    string `json:"value_hint,omitempty"`     // 安全掩码，如 ghp_••••a1b2
    HasValue     bool   `json:"has_value,omitempty"`      // 是否已配置非空值
    ClearValue   bool   `json:"clear_value,omitempty"`    // 显式清空已有值
    Note         string `json:"note,omitempty"`           // 备注（如权限范围、到期时间）
}

type CredentialRecord struct {
    ID              string                       `json:"id"`
    Name            string                       `json:"name"`
    Description     string                       `json:"description,omitempty"`
    Preset          string                       `json:"preset,omitempty"`
    StorageMode     string                       `json:"storage_mode"` // "server_synced" | "runner_local"
    UsagePolicy     string                       `json:"usage_policy"` // "all" | "chat_only" | "cron_explicit"
    Scope           string                       `json:"scope"`        // "global" | "workspace" | "runner"
    WorkspacePath   string                       `json:"workspace_path,omitempty"`
    TargetRunners   []string                     `json:"target_runners,omitempty"`
    Entries         []CredentialEntry            `json:"entries"`
    RunnerOverrides map[string][]CredentialEntry `json:"runner_overrides,omitempty"`
    ProbeCommand    string                       `json:"probe_command,omitempty"`
    Enabled         bool                         `json:"enabled"`
    UpdatedAt       int64                        `json:"updated_at"`
}
```

---

## 3. 加密机制 (`pkg/store/vault.go`)

- 每个 `Store` 实例在 `Open(dataDir)` 时初始化 `vaultKey`（32 字节 AES-256 密钥）：
  - 优先读取 `<dataDir>/vault.key`（64 位 hex，权限强制 `0600`）。
  - 若不存在则使用 `crypto/rand` 生成 32 字节并原子写入 `<dataDir>/vault.key`（`0600`）。
- 加密格式：`v1:<base64(nonce[12] || ciphertext || tag[16])>`。
- 掩码规则 `MaskSecretHint(v string)`:
  - 空串 -> `""`
  - 长度 < 10 -> `"••••••••"`
  - 长度 >= 10 -> `v[:4] + "••••" + v[len(v)-4:]`

---

## 4. 协议扩展 (`pkg/protocol/frames.go`)

### 4.1 `ConfigSnapshot` 扩展
```go
Credentials       []store.CredentialRecord `json:"credentials,omitempty"`
CredentialsSynced bool                     `json:"credentials_synced,omitempty"`
```
- `Server.buildConfigSnapshot(ctx, runnerID)` 构造下发列表时：
  1. 过滤出 `Enabled == true` 且匹配 `runnerID` 的记录。
  2. 对 `server_synced` 记录：在 Server 侧解密 `Entries` 及仅属于该 `runnerID` 的 `RunnerOverrides`（剔除其他 Runner 的覆盖项，防止横向泄露！），通过已认证的 WS 信道下发给该 Runner。
  3. 对 `runner_local` 记录：仅下发元数据（不含任何值）。
  4. Runner 收到 `CredentialsSynced == true` 时，用自己的 `vaultKey` 重新加密落盘，并**全量对账清理**已不再属于本节点的旧 `server_synced` 记录。

### 4.2 新增 WS RPC 帧类型
- `credential.local_set`：Server -> 目标 Runner，直接写入/清空该 Runner 本地的 `credential_local_values`（零 Server 落盘）。
- `credential.probe`：Server -> 目标 Runner，在目标 Runner 上用该凭证分组的实际解析环境执行只读 `probe_command`（带 10s 超时与 `SecretRedactor` 脱敏），返回 `{ok, exit_code, output, latency_ms}`。

---

## 5. 运行时解析、注入与脱敏 (`pkg/harness/tools/redactor.go` & `pkg/runner/`)

### 5.1 环境变量解析优先级
Runner 提供统一方法 `ResolveSessionEnv(ctx, workDir, triggerSource, cronCredentialIDs) (envMap map[string]string, keyNames []string)`：
1. 过滤 `Enabled == true` 且命中当前 `runnerID` 的凭证分组。
2. 按 `triggerSource` 过滤：
   - `"chat"` / `"subagent"`：允许 `usage_policy == "all"` 和 `"chat_only"`；跳过 `"cron_explicit"`。
   - `"cron"`：允许 `usage_policy == "all"`，以及 `id` 在 `cronCredentialIDs` 中的 `"cron_explicit"`；严格跳过 `"chat_only"`。
3. 按作用域优先级叠加（后者覆盖前者）：
   1. `scope == "global"`
   2. `scope == "runner"`
   3. `scope == "workspace"`（要求 `NormalizeWorkspaceKey(rec.WorkspacePath) == NormalizeWorkspaceKey(workDir)`）
4. 每个分组内部：`Entries`（基础值） < `RunnerOverrides[runnerID]`（单节点覆盖值） < `credential_local_values`（当 `storage_mode == "runner_local"` 时）。

### 5.2 工具注入与全链路脱敏
- `BashTool` 与 `BackgroundTaskManager` 新增可选 `EnvProvider` / `Redactor`：
  - 启动子进程时，在 `os.Environ()` 基础上叠加 `envMap`（同名键覆盖，不污染 Runner 主进程全局环境）。
  - 所有输出路径（`boundedCollector` 的 `Write`、`onUpdate`、`spillFile`、`bgTask.Write`）均经过 `SecretRedactor`。
  - 流式 `onUpdate` 采用尾部保留窗口（`maxSecretLen - 1` 字节，上限 256B），确保密钥被管道切分在相邻两次 `Read` 之间时仍能完整脱敏，进程退出/Finalize 时 flush 剩余缓冲。
- `codemode/mcp_pool.go` 的 `expandEnvPlaceholders` 支持优先查找注入的 `envMap`，再回落 `os.LookupEnv`。
- `prompts.SessionOptions` 新增 `ConfiguredEnvKeys []string`，在 `meta_context` 中仅输出变量名清单与安全使用说明。

---

## 6. REST API 设计 (`pkg/server/api.go`)

- `GET /api/v1/credentials`：返回所有凭证分组（脱敏视图，绝不含明文；附带已配置的 `runner_overrides` key 列表）。
- `POST /api/v1/credentials` / `PUT /api/v1/credentials`：创建或更新凭证分组，自动加密保存并 `BroadcastConfigSync`。
- `DELETE /api/v1/credentials/{id}`：删除凭证分组并 `BroadcastConfigSync`。
- `POST /api/v1/credentials/import-env`：解析粘贴的 `.env` 文本（支持注释、`export ` 前缀、单双引号），按指定分组与作用域批量写入。
- `POST /api/v1/credentials/{id}/local-values`：通过 WS RPC 直接将值写入指定在线 Runner 的本地加密库（不落 Server DB）。
- `POST /api/v1/credentials/{id}/probe`：通过 WS RPC 在指定 Runner 上运行只读验活命令并返回脱敏结果。

---

## 7. Settings UI 设计 (`clients/web/src/features/SettingsCredentials.tsx`)

1. **导航入口**：在 `SettingsPage.tsx` 的 `settings.group.basics` 分组（`Runners` 下方）新增 **Credentials & Env**（`KeyRound` 图标）。
2. **核心界面**：
   - 顶部作用域筛选：`全部` / `全局` / `按 Runner 筛选` / `按工作区筛选`。
   - 快捷操作：`+ 新建凭证分组`、`CLI 预设库`（GitHub `gh`、AWS、Cloudflare、Kubernetes、npm、Docker、Sentry、Tavily）、`粘贴 .env 批量导入`。
   - 分组卡片：展示组名、存储模式徽章（`Server 同步` / `Runner 本地`）、策略徽章（`对话+定时` / `仅对话` / `仅定时授权`）、启用开关、变量行（Key + 掩码状态 + 更新值）、目标 Runner 状态与一键 **`验证（Probe）`** 按钮。
3. **联动**：
   - **Automations（定时任务）**：创建表单新增“授权凭证”多选（展示可用 `all` / `cron_explicit` 分组，隐藏 `chat_only`）。
   - **MCP Settings**：环境变量与 Header 中的 `${VAR}` 自动匹配并提示是否已在凭证库配置。

---

## 8. 测试与验收标准

1. **Go 单元测试**：
   - `pkg/store/credentials_test.go`：AES-256-GCM 加解密、掩码提示、部分更新保留密文、`ClearValue` 清空、Runner 本地值表 CRUD。
   - `pkg/harness/tools/redactor_test.go`：精确替换、URL/Base64 变体替换、跨 chunk 边界流式脱敏、`BashTool` 注入与防泄露测试（运行 `env` / `echo $SECRET` 绝不输出明文）。
   - `pkg/server` & `pkg/runner` 集成测试：`ConfigSnapshot` 按 `runnerID` 定向过滤（不泄露其他 Runner 的覆盖值）、`usage_policy` 在 `chat` vs `cron` 场景下的隔离、`.env` 导入 API 校验。
2. **前端构建与校验**：
   - `clients/web` TypeScript 类型检查与 `npm run build` 零报错。
   - `go test ./...` 全量通过。
