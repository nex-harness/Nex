# Docker：单镜像 nex-server

## 现状与问题

当前 `Dockerfile` 产出两个镜像：

- `server`：把整个 builder 阶段的 `/app` 拷进 `node:24-bookworm-slim`，镜像约 4.3 GB。其中根 `node_modules`（含 devDependencies、构建工具、已被打进 `web/dist` 的前端库）约 2.3 GB，另有源码、`tsbuildinfo`、`apps/nex-cli` 等。
- `web`：nginx 托管 `packages/web/dist`，并把 `/api`、`/ws` 反代到 server。

但 server 本身已经能托管 Web UI（`NEX_WEB_STATIC_ROOT`，SPA fallback），nginx 容器不再提供独有能力；它的配置里还保留着已被 PR #30 取代的 `nex_lite_token` cookie 说明。发行版 tarball 与 `install.sh` 早已使用「单个 SEA 二进制 + `web/` 目录、同一进程托管」的形态。

## 产品规则

- 只发布一个镜像 `ghcr.io/nex-harness/nex-server`，进程监听容器内 3030，同时提供 API、WebSocket 与 Web UI。**废弃 `nex-web` 镜像和 nginx 配置**；不再有 8080 端口。
- 镜像内容 = SEA 二进制 `nex-server` + 与之同一次构建的 `web/dist` + 运行时依赖（`git`、`openssh-client`、`ca-certificates`、`curl`）。不再包含 monorepo 源码和 `node_modules`。
- **镜像不负责编译。** `Dockerfile` 只装配已构建好的产物：构建上下文只有 `dist-docker/`（`.dockerignore` 放行的唯一目录），由 `scripts/stage-docker-context.sh <target>` 从 `packages/server/dist/sea/nex-server-<target>` 与 `packages/web/dist` 复制而来。SEA 的构建步骤沿用 `release-server.yaml`（同样的 Node 24.14.0、typecheck、agent bundle、web build、`build-sea.mjs`），`docker.yaml` 在 `docker build` 之前执行同样的步骤。这样 Docker 构建只剩两层 COPY，不重复构建、不依赖镜像内再次下载依赖。
- 架构：`docker.yaml` 只发布 `linux/amd64`（`--target linux-x64`）。`stage-docker-context.sh linux-arm64` 与 `docker build --platform linux/arm64` 可在本机使用，但 arm64 的 runtime 层需要 QEMU 才能执行 `apt-get`，CI 不在本期范围。
- 容器行为保持不变：以 root 运行（沿用现有 `nex-data` 卷的属主）；`/data` 保存全部状态（`NEX_DATA_BASE_DIR`），`/workspace` 是默认工作区；`PORT=3030`、`NEX_SERVER_HOST=0.0.0.0`；访问令牌仍由 `NEX_SERVER_AUTH_TOKEN` 提供，登录页与会话行为见 `web-token-login.md`。
- 健康检查改用 `curl` 访问公开的 `/api/auth/session`（有无令牌都返回 200），不再依赖运行时里的 Node。
- `docker-compose.yml` 只剩一个服务，端口映射 `3030:3030`。**这是对现有 compose 用户的破坏性变更**：访问地址由 `:8080` 变为 `:3030`，不再有 `web` 服务；需要 TLS/域名的部署自行在前面放反向代理，并转发 `/ws`（带 `Upgrade`）与 `/api`。

## 接口与所有者

| 项                     | 所有者                                                                  |
| ---------------------- | ----------------------------------------------------------------------- |
| 镜像内容               | `Dockerfile`（唯一）；产物准备由 `scripts/stage-docker-context.sh` 负责 |
| 镜像 tag 规则、推送    | `.github/workflows/docker.yaml`（只推 `nex-server`）                    |
| Web 静态资源托管、鉴权 | `packages/server/src/http.ts`、`webAuth.ts`（沿用，不改）               |
| 本地编排               | `docker-compose.yml`                                                    |

```text
CI / 本机：pnpm install → typecheck → agent bundle → web build → build-sea --target linux-x64
        │ packages/server/dist/sea/nex-server-linux-x64     │ packages/web/dist
        └──────────── scripts/stage-docker-context.sh ───────┘
                              ▼  dist-docker/{nex-server, web/}
Dockerfile: debian:bookworm-slim + git/ssh/curl
  /usr/local/bin/nex-server   /app/web   ← NEX_WEB_STATIC_ROOT
```

## 失败语义与限制

- 没有先准备 `dist-docker/` 时 `docker build` 直接失败（`COPY` 找不到文件）；`stage-docker-context.sh` 在缺少二进制或 `index.html` 时报错退出，不生成半成品。
- 构建机无法下载 Node 24.14.0 官方二进制时 SEA 构建失败，不回退到其它版本。
- 去掉 nginx 后没有 gzip：静态资源以未压缩形式返回。本期不改 server 的静态处理，作为已知损失记录在变更日志中。
- 已发布的 `ghcr.io/tylinux/nex-web` 旧 tag 保留在 ghcr，但不再更新；`latest` 停在 1.2.3。

## 验收

1. 在 Linux amd64 上 `docker build` 成功，镜像体积显著低于 4.3 GB，并记录实际大小。
2. 容器启动后：`GET /api/auth/session` 返回 `{"authRequired":true,"authenticated":false}`；`GET /` 返回 Web 页面；未登录访问 `/api/*` 为 401；用令牌登录后 WebSocket 可连通，首页能加载。
3. 容器重启（数据卷保留）后，已登录会话仍然有效；健康检查变为 `healthy`。
4. 容器内 `git`、`ssh` 可用；二进制在 `debian:bookworm-slim` 上能启动（共享库齐全）。
5. `docker-compose.yml` 只剩一个服务，`docker compose config` 通过；`docker.yaml` 不再引用 `nex-web`。
6. `pnpm typecheck`、`pnpm lint`、`pnpm architecture:check --changed` 通过。
7. 未验证项如实说明（例如 arm64 交叉构建若未实测）。
