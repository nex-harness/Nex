# Docker：单镜像 nex-server

## 产品规则

- 只发布一个镜像 `ghcr.io/nex-harness/nex-server`，容器对外 3030 同时提供 API、WebSocket 与 Web UI。
- 镜像内容 = `packages/nex-server-cli` 的 staging 发行目录（`bin/nex` + 随包 Node +
  `server-core.js` + `nex.cjs` + 生产依赖闭包 + 官方插件）+ 同一次构建的 `web/dist`。
  不含 monorepo 源码与 devDependencies。
- **镜像不负责编译。** `Dockerfile` 只装配产物；构建上下文只有 `dist-docker/`
  （`Dockerfile.dockerignore` 放行 `dist-docker` 与 `docker` 两个目录），由
  `scripts/stage-docker-context.sh <target>` 从
  `packages/nex-server-cli/dist-release/nex-server-<target>` 与 `packages/web/dist` 复制。

## 为什么镜像里有一个 nginx

`server-core` 只接受回环监听：`packages/nex-server-cli/src/server-core/http.ts` 对
非回环 host 直接 fail-closed，因为 R2 的 Core 面向"本机 / SSH 隧道"入口，鉴权靠
`run/ingress-token` 而不是网络位置。它也不托管 Web UI——上游 R2 架构里 UI 由调用方
（桌面窗口、远控宿主）提供。

因此容器分工是：

```
浏览器 ──:3030──▶ nginx ──静态资源──▶ /app/web
                    └──/api,/ws──▶ 127.0.0.1:<random>（server-core，注入 Bearer）
```

- 端口与 token 由 core 启动后写入 `<NEX_DATA_BASE_DIR>/run/{status.json,ingress-token}`；
  `docker/entrypoint.sh` 等就绪后用 `envsubst` 渲染 `docker/nginx.conf` 并启动 nginx。
- 浏览器从不接触 ingress token；对外访问控制应放在本容器之前（反代 + TLS + 登录层）。

## 容器行为

- 以 root 运行；`/data` 保存全部状态（`NEX_DATA_BASE_DIR`），`/workspace` 是默认工作区。
- 对外端口 `NEX_SERVER_PORT`（默认 3030）；core 端口由 supervisor 动态分配。
- 健康检查走 `/`（nginx 直接返回 SPA `index.html`，不经过需要 token 的 core）。
- `docker-compose.yml` 单服务，映射 `3030:3030`；需要 TLS/域名时在前面放反向代理并转发
  `/ws`（带 `Upgrade`）与 `/api`。

## 构建与发布

- staging 只能构建**当前平台**（`currentServerTarget()`），不能交叉编译。因此
  `docker.yaml` 与 `release-server.yaml` 的每个 target 必须有同架构 runner。
- `docker.yaml`：`pnpm --filter @nex/server-cli stage` → `scripts/stage-docker-context.sh linux-x64`
  → `docker build`。
- `release-server.yaml`：同样 staging，把 `<release>/` 加 `web/` 打成 tar.gz 上传。

## 接口与所有者

| 项                     | 所有者                                                                  |
| ---------------------- | ----------------------------------------------------------------------- |
| 镜像内容               | `Dockerfile`（唯一）；产物准备由 `scripts/stage-docker-context.sh` 负责 |
| 镜像 tag 规则、推送    | `.github/workflows/docker.yaml`                                         |
| Web 静态资源托管       | `docker/nginx.conf`（容器内 nginx）                                     |
| API / WebSocket 与鉴权 | `packages/nex-server-cli/src/server-core/{http,ingressToken}.ts`        |
| 本地编排               | `docker-compose.yml`、`docker/entrypoint.sh`                            |

```text
CI / 本机：pnpm install → typecheck → agent bundle → web build → server-cli stage
        │ packages/nex-server-cli/dist-release/nex-server-linux-x64 │ packages/web/dist
        └──────────────── scripts/stage-docker-context.sh ──────────┘
                     ▼  dist-docker/{nex-server/, web/}
Dockerfile: node:22-bookworm-slim + git/ssh/nginx/gettext
  /opt/nex/bin/nex   /app/web   nginx:3030 → 127.0.0.1:<core port>
```

## 失败语义与限制

- 没有先准备 `dist-docker/` 时 `docker build` 直接失败（`COPY` 找不到文件）；
  `stage-docker-context.sh` 在缺少 release 目录或 `index.html` 时报错退出，不生成半成品。
- core 未在 90s 内写出 `status.json` 端口时，entrypoint 杀掉 core 并以非零码退出。
- 镜像内 nginx 是新引入的一层；静态资源压缩、缓存头由 nginx 默认行为决定。

## 验收

1. 在 Linux amd64 上 `docker build` 成功，记录镜像与 tar.gz 实际大小。
2. 容器启动后：`GET /` 返回 Web 页面；`/api/*` 与 `/ws` 经 nginx 注入 token 后可访问。
3. 容器重启（数据卷保留）后会话仍可恢复，健康检查变为 `healthy`。
4. 容器内 `git`、`ssh` 可用。
5. `docker compose config` 通过。
6. `pnpm typecheck`、`pnpm lint` 通过。
7. 未验证项如实说明（本机为 macOS，未实测 linux 镜像构建与 arm64 交叉）。
