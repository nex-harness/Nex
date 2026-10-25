# Nex

<div align="center">
  <img src="public/logo/icons/1024x1024.png" alt="Nex" width="128" height="128" />
</div>

<p align="center">
  简体中文 | <a href="README.md">English</a>
</p>

Nex 是 AI 编程工作台，提供桌面应用、浏览器界面和终端 Agent。本仓库包含客户端、后端服务、共享 UI，以及 Agent CLI 与运行时源码。

## 项目来源与版本

- Nex 当前版本 **v1.2.3**。
- Nex 基于 [ZCode](https://github.com/zai-org/ZCode) **v3.14.3**（Apache-2.0 开源版本）二次开发而来，
  在其基础上做了品牌重命名、遥测与账号体系裁剪、UI 精简等深度定制，
  详见 [CHANGELOG.md](CHANGELOG.md)。
- 感谢 ZCode 原团队的优秀工作，原项目的 Apache-2.0 许可与归属声明见 [LICENSE](LICENSE) 与 [NOTICE.md](NOTICE.md)。

## Docker（单镜像 server）

镜像只有一个：`nex-server`（SEA 单文件二进制 + 同一次构建的 Web 静态资源，不含 Electron 和 monorepo 源码）。同一个进程提供 API、WebSocket 与 Web UI，不再需要 nginx / `nex-web`。

拉取发布的镜像 `ghcr.io/nex-harness/nex-server`，或在本机先构建 SEA 与 Web，再装配镜像（Dockerfile 不负责编译，产物准备见 `scripts/stage-docker-context.sh`）：

```bash
pnpm --filter @nex/web build
(cd packages/server && node scripts/build-sea.mjs --target linux-x64)   # arm64 主机用 linux-arm64
scripts/stage-docker-context.sh linux-x64
NEX_SERVER_AUTH_TOKEN=$(openssl rand -hex 32) docker compose up -d --build
```

SEA 构建前还需要完成 typecheck、agent bundle 等步骤，完整顺序与 `.github/workflows/release-server.yaml` 相同。

- 浏览器打开 `http://<host>:3030/`，在登录页输入上面的令牌（会话行为见「Web 登录」一节）
- `nex-data` 卷持久化 `~/.nex` 状态，`nex-workspace` 卷是默认工作区
- 需要 TLS 或域名时在前面放反向代理，并转发 `/ws`（带 `Upgrade`）与 `/api`；服务端识别 `X-Forwarded-Proto: https` 来设置会话 cookie 的 `Secure`
- 架构：CI 只发布 `linux/amd64` 镜像；arm64 主机用 `stage-docker-context.sh linux-arm64` 在本机构建
- 从 1.2.2 及更早的 compose 升级：访问端口由 `8080` 改为 `3030`，不再有 `web` 服务；`ghcr.io/tylinux/nex-web` 不再更新。详见 `docs/specs/docker-single-image.md`

## 初始化

准备 Git、Node.js **24.14.0** 和 pnpm **10.33.2**，版本以 [mise.toml](mise.toml) 为准。以下开发和打包命令均在仓库根目录执行。

```bash
pnpm bootstrap
```

`pnpm bootstrap` 安装 workspace 依赖、准备桌面本地运行资源，再执行 `build:bootstrap`。

Agent CLI 与运行时源码位于 [apps/nex-cli/](apps/nex-cli/)，作为普通目录随本仓库一起克隆，无需单独拉取或初始化 Git submodule。

根据需要选择其他初始化或构建入口：

| 命令                           | 用途                                                              |
| ------------------------------ | ----------------------------------------------------------------- |
| `pnpm install`                 | 安装依赖                                                          |
| `pnpm prepare:desktop-runtime` | 准备桌面运行资源，默认包含远程资源准备                            |
| `pnpm prepare:remote-assets`   | 单独准备远程运行资源                                              |
| `pnpm bootstrap:with-remote`   | 初始化依赖、本地与远程资源，并串行构建相关包；跳过桌面应用 bundle |
| `pnpm build`                   | 递归执行各 workspace 包的构建脚本，包括包内的资源准备步骤         |

默认 `bootstrap` 跳过远程资源准备，适合本地桌面开发。使用远程工作区或验证远程发行资源时，再运行对应准备命令。

## 开发与运行

### 桌面版

```bash
pnpm dev:desktop

# 使用测试环境
pnpm dev:desktop:test
```

`pnpm dev:desktop` 默认等同于 `pnpm dev:desktop:prod`，使用生产服务配置。启动脚本会准备本地运行资源、构建桌面 Agent，再启动 Electron 和源码监听。

需要独立开发数据目录时，可设置 `NEX_DATA_BASE_DIR`。例如在 macOS / Linux 中：

```bash
NEX_DATA_BASE_DIR="$HOME/.nex-dev-home" pnpm dev:desktop:test
```

### 远程功能（SSH/WSL）

先执行 `pnpm bootstrap:with-remote` 准备远程资源（mock-cdn），再 `pnpm dev:desktop`；连接远程项目时资源选择「本地下载后上传」。开发态资源取自本地 `packages/desktop/mock-cdn` 和本地构建产物，经 SFTP 上传到远程，不访问 CDN。

### Web 开发

修改 Web 或后端源码时，使用开发模式：

```bash
pnpm dev:web

# 指定后端工作区（macOS / Linux）
NEX_SERVER_WORKSPACE=/path/to/project pnpm dev:web
```

该命令同时启动 Web 开发服务器（默认 `http://localhost:5173`）和后端（默认 `http://localhost:3030`）；浏览器访问前者。`/ws` 和一般 `/api` 请求代理到本地后端，`/api/v1/oauth/token` 单独代理到当前配置的产品服务。

Agent 源码修改后，执行 `pnpm --filter @nex/cli... build` 并重启服务。需要验证完整发行包时，按下方“Nex 命令行版”打包章节解压运行。

### Nex 命令行版

命令行发行包包含 TUI、Web 和 Agent，统一使用 `nex` 启动：无参数进入 TUI；第一个参数为 `--web` 时启动 Web；其他参数交给现有 Agent CLI 处理。两种模式都在本机运行，无需 Electron。

```bash
# 默认进入终端交互界面
nex

# 启动 Web 界面
nex --web

# 指定项目和端口，不自动打开浏览器
nex --web --workspace /path/to/project --port 3030 --no-open

# 查看 CLI 或 Web 参数
nex --help
nex --web --help
```

Web 模式默认工作目录为当前目录，监听 `127.0.0.1`，默认不启用访问令牌，自动选择空闲端口并打开浏览器。访问终端输出的地址，按 `Ctrl+C` 停止服务。局域网访问可使用 `--host 0.0.0.0`；监听非本机地址时默认生成访问令牌，使用终端输出的带令牌链接。可通过 `--token` 指定令牌或 `--no-token` 关闭令牌认证。

直接启动通用 Web 服务的 HTTP 入口时，通过 `NEX_SERVER_AUTH_TOKEN` 配置 API／WebSocket 认证；通过程序接口创建服务时，使用 `authToken` 选项。

配置令牌后，浏览器在输入该令牌前会被带到登录页（`/login`），登录成功后回到原来请求的页面。浏览器只保存一个随机会话 cookie（30 天、`HttpOnly`）；会话以哈希形式保存在 Nex 配置目录的 `web-sessions.json` 中，令牌变更后全部失效。直接打开 `http://host:3030/?token=<令牌>` 仍可直接登录。同一地址连续 5 次失败会锁定登录 1 分钟。详见 `docs/specs/web-token-login.md`。

首次打开 Web（设置了令牌时为登录后）会提示是否允许浏览器通知，用于在页面处于后台时收到任务完成、失败或需要确认的提醒。该提示每个浏览器只出现一次；之后可在「设置 → 浏览器通知」查看当前状态并授权。若浏览器已拦截通知，需要在浏览器的站点设置中重新允许（页面无法再次弹出已被拒绝的授权框）。通知需要 HTTPS 或 `localhost`。详见 `docs/specs/web-task-notifications.md`。

构建方式见下方打包章节。`pnpm build:nex` 只生成发行包，不会替换 `PATH` 中已有的 `nex`。如果命令仍指向旧安装或其他源码目录，macOS / Linux 可用 `command -v nex` 检查，Windows 可用 `where.exe nex` 检查。

### CLI 源码开发

直接开发 TUI 或 Agent 时，运行源码入口：

```bash
pnpm --filter @nex/cli dev --help
pnpm --filter @nex/cli dev

# 构建 CLI 及其 workspace 依赖
pnpm --filter @nex/cli... build
node apps/nex-cli/packages/cli/dist/nex.cjs --help
```

这个入口直接运行 Agent CLI，不经过发行包的 `--web` 分流。开发 Web 用 `pnpm dev:web`；验证统一的 `nex` 命令，用下方解压后的 `bin/nex.mjs`。

## 配置

根目录 [.env.example](.env.example) 提供服务地址与构建配置示例，可按需复制到 `.env`，本地覆盖放入 `.env.local`。Desktop 的开发环境通过 `dev:desktop:test` / `dev:desktop:prod` 选择。

| 配置                               | 用途                                             |
| ---------------------------------- | ------------------------------------------------ |
| `NEX_DATA_BASE_DIR`                | 应用数据基目录，数据写入其下的 `.nex/`           |
| `NEX_SERVER_WORKSPACE`             | Web 后端的工作区路径                             |
| `NEX_BUILTIN_PROVIDER_CONFIG_FILE` | 本地 Provider 配置文件路径；未设置时使用内置配置 |
| `NEX_DIST_BASE_URL`                | 命令行安装脚本使用的下载根地址                   |

运行时变量可在启动命令的环境中显式设置。随客户端发布的默认配置见 [config/README.md](config/README.md)。

## 打包

第三方声明生成、发行校验流程及声明在发行物中的位置见 [third-party/README.md](third-party/README.md)。

### 桌面版

```bash
pnpm bundle:desktop

# 指定目标平台与 CPU 架构
pnpm bundle:desktop -- --os win --arch x64

pnpm bundle:desktop -- --help
```

默认目标为 macOS arm64，默认输出目录为 `packages/desktop/dist/`。`--os` 支持 `mac`、`win`、`linux`，`--arch` 支持 `x64`、`arm64`；实际打包与签名需要目标平台对应的工具和配置。

安装：双击打开产物 DMG，将 Nex 拖入"应用程序"。本地构建未签名，首次打开若被 macOS 拦截，执行：

```bash
sudo xattr -rd com.apple.quarantine /Applications/Nex.app
```

### Nex 命令行版

构建入口为 `pnpm build:nex`。脚本会依次构建 CLI/TUI、后端和 Web，收集 TUI 的原生库、worker 与运行时依赖，再组装发行包；运行发行包仍需要 Node.js，版本以 `mise.toml` 为准。

打包前必须设置下载根地址 `NEX_DIST_BASE_URL`（可放在 `.env`、`.env.local` 或环境变量中），也可以通过 `--base-url` 传入。以下地址是占位示例，发布时替换为实际托管地址：

```bash
pnpm build:nex --base-url https://downloads.example.com/nex/

# 已配置 NEX_DIST_BASE_URL 时
pnpm build:nex

# 仅重新组包，复用已有的 Agent、后端和 Web 构建产物
pnpm build:nex --skip-build

# 查看版本、输出目录等可选参数
pnpm build:nex --help
```

默认版本取根目录 `package.json`，输出目录为 `dist/nex/`：

- `releases/<version>/nex-<version>.tar.gz`：运行包。
- `releases/<version>/sha256.txt`：校验摘要。
- `latest.json`、`install.sh`：版本索引和安装脚本。

完整目录可上传到配置的下载根地址。安装脚本从该地址下载运行包，默认安装到 `~/.nex/runtime`，并在 `~/.local/bin` 创建 `nex` 命令。安装目录可通过 `NEX_DIST_HOME` 修改，命令目录可通过 `NEX_DIST_BIN_DIR` 修改。

旧 Lite 用户需要改用上述构建命令、环境变量和新的安装脚本。新安装不会删除旧 Lite 目录，也不会迁移或删除已有会话数据。

本地调试打包产物时，可直接解压运行，无需上传或安装：

```bash
nex_version=$(node -p "require('./dist/nex/latest.json').version")
mkdir -p dist/nex/debug
tar -xzf "dist/nex/releases/$nex_version/nex-$nex_version.tar.gz" \
  -C dist/nex/debug
# 默认启动 TUI
node dist/nex/debug/nex/bin/nex.mjs

# 启动 Web
node dist/nex/debug/nex/bin/nex.mjs --web \
  --workspace "$PWD" --port 3030 --no-open
```

浏览器打开 `http://127.0.0.1:3030`，即可验证同一后端服务托管 Web 页面和 Agent 的完整链路。该端口需要空闲；如正在运行 `pnpm dev:web`，可改用其他 `--port`。

## 仓库结构

| 目录                                                 | 职责                                       |
| ---------------------------------------------------- | ------------------------------------------ |
| `packages/desktop`                                   | Electron Main、Host、Renderer 与桌面打包   |
| `packages/web`                                       | Web 客户端                                 |
| `packages/server`                                    | HTTP / WebSocket 服务与远程连接            |
| `packages/nex-server-cli`                            | 独立 Server 启动与进程管理                 |
| `packages/ui`                                        | 共享 React 组件、hooks 与 Zustand 状态     |
| `packages/services`                                  | 业务服务与持久化                           |
| `packages/shared`、`packages/rpc`、`packages/client` | 共享协议和类型、RPC 框架、Agent 客户端 SDK |
| `packages/provider`、`packages/provider-node`        | Provider 公共能力与 Node 实现              |
| `apps/nex-cli`                                       | Agent CLI、TUI、运行时与工具               |
| `scripts`、`config`、`third-party`                   | 构建维护脚本、内置配置与第三方声明材料     |

## 项目声明

功能与优惠范围、维护规则、执行与数据风险，以及许可和第三方版权说明，详见 [NOTICE.md](NOTICE.md)。
