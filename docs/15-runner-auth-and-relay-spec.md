# Nex-v3 Runner 鉴权加固与内网 Server 反向中继（Relay）接入规范

- **状态**: Approved / In Implementation
- **更新日期**: 2026-11-24
- **Worktree / 分支**: `/Users/tylinux/Developer/Projects/Nex-v3-wt-runner-auth-relay` (`feat/runner-auth-relay`)
- **关联文档**:
  - [12-remote-access-spec.md](./12-remote-access-spec.md)
  - [14-runner-self-update-spec.md](./14-runner-self-update-spec.md)
- **设计目标**:
  1. **Runner 强鉴权**：消除 `nex-server` 默认无 `PairingSecret` 时任意节点可接入 `/ws/runner` 并获取 Provider API Key / 劫持 `runner_id` 的安全风险，默认自动生成并持久化 `runner_pairing_secret`，支持在 Settings -> Runners 查看、复制、轮换与开关兼容模式。
  2. **内网 Server + 外部 Runner 穿透接入**：复用 `pkg/remote` 已有的零信任 E2EE 双向 mTLS + `yamux` 隧道（`LocalProxy` + `nex-relay`），让 `nex-runner` 通过 `nex://pair?...` 配对串即可自动竞速选路（`LAN → Direct/Tailscale → nex-relay`）连入无公网 IP 的内网 `nex-server`，同时天然获得 Ed25519 设备身份认证与吊销能力。

---

## 1. Phase 1：Runner 默认持久化鉴权与令牌管理

### 1.1 持久化配置模型（`kv_store`）
在 Server SQLite `kv_store` 中存储键 `runner_auth_config`：
```json
{
  "pairing_secret": "<48-char hex (24 random bytes)>",
  "require_auth": true,
  "rotated_at": 1795460000000
}
```
- **优先级**：若启动参数/环境变量显式设置了 `NEX_PAIRING_SECRET` / `-pairing-secret`，以环境变量为准（`env_managed: true`，`require_auth` 强制为 `true`）；否则首次访问时自动生成 24 字节随机十六进制密钥并持久化到 `kv_store`，默认 `require_auth: true`。
- **握手校验（`pkg/server/ws.go`）**：当 `require_auth == true` 时，`handleRunnerWS` 按以下顺序放行（均使用 `subtle.ConstantTimeCompare`）：
  1. `hello.AuthToken == effectivePairingSecret`；
  2. 已注册 Runner 的独立 `existing.AuthToken`（且非空）；
  3. 合法的 `paired_devices` 设备令牌（通过 `nex://pair` 配对的 Runner，支持在 Remote Access 中一键吊销）。
- **SSH 部署与自更新联动**：
  - `pkg/server/ssh_deploy.go` 自动注入 `effectivePairingSecret`，通过 SSH 部署的 Runner 开箱即带鉴权令牌。
  - `pkg/server/runner_update.go` 在 `isRunnerBinaryDownloadAuthorized` 中同样校验 `effectivePairingSecret` 与已签发的短期下载令牌。

### 1.2 REST API（均受 Web Auth 保护）
- `GET /api/v1/runners/auth`：返回 `{ pairing_secret, require_auth, env_managed, rotated_at }`。
- `POST /api/v1/runners/auth/rotate`：轮换 `pairing_secret`（`env_managed` 时返回 400）。
- `PUT /api/v1/runners/auth`：更新 `{ require_auth: bool }`（`env_managed` 时返回 400）。

---

## 2. Phase 2：Runner 复用 `pkg/remote` 多通道与 `nex-relay` E2EE 隧道

### 2.1 架构设计（零侵入 `pkg/runner/node.go`）
复用 `pkg/remote.LocalProxy`（已在 `clients/app` 中验证，纯 Go 零 CGO）：
```text
nex-runner Node (WS + HTTP Updater)
        │ ws://127.0.0.1:<ephemeral>/ws/runner
        ▼
remote.LocalProxy (LAN 250ms → Direct/Tailscale 800ms → nex-relay mTLS+yamux)
        │
        ▼
Intranet nex-server (Ed25519 Identity + paired_devices)
```
- `nex-runner` 新增 CLI 能力：
  - 子命令 `nex-runner pair <nex://pair?...>`：读取/生成 `<data-dir>/remote_identity.json`，调用 `remote.PairWithHost` 完成配对并将 `PairedHostProfile` + `device_token` 持久化至 `<data-dir>/paired_server.json`（权限 `0600`）。
  - 守护进程参数 `-pair <nex://pair?...>`（或环境变量 `NEX_PAIR_URI`）：启动时先完成配对并持久化，随后自动通过 `remote.LocalProxy` 接入 Server。
  - 当未传 `-server` 但 `<data-dir>/paired_server.json` 存在时，自动拉起 `remote.LocalProxy` 并将 `ServerWSURL` 指向本地代理 `ws://127.0.0.1:<port>/ws/runner`，`AuthToken` 使用 `device_token`。
- 由此 `/ws/runner` 业务通道与 `/api/v1/runners/binary/{os}/{arch}` 自更新下载均自动走加密多通道隧道，彻底解决“内网 Server + 公网/异地 Runner”场景。

---

## 3. Phase 3：Settings -> Runners UI 与验证

- **Settings -> Runners**：
  - 展示 Runner 鉴权配置（`require_auth` 开关、可显隐/复制的 `Pairing Secret`、`轮换密钥`按钮）。
  - 手动接入命令自动拼入 `-token <pairing_secret>`。
  - 新增「通过中继 / 内网配对接入」命令生成按钮：调用现有 `POST /api/v1/remote/pair/invite` 生成短期 `nex://pair?...` 配对串，输出一键 `nex-runner -name "..." -pair "nex://pair?..."` 命令。
- **验证矩阵**：
  - `go test ./pkg/server/... ./pkg/remote/... ./cmd/nex-runner/...`
  - `make test-web`（含 `i18n.test.ts` 与新增 `SettingsRunners` 鉴权/配对命令 Vitest 用例）
  - `make verify-nocgo`
