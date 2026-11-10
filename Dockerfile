# syntax=docker/dockerfile:1
# Nex server 镜像（无 Electron、无 nginx）：预先构建好的 SEA 二进制 + 同一次构建的 Web 静态资源。
#   server 进程自己托管 Web UI（NEX_WEB_STATIC_ROOT）并提供 /api、/ws，登录页与会话由 server 处理。
#   构建上下文只有 dist-docker/，由 scripts/stage-docker-context.sh 从 SEA 与 web 产物准备；
#   SEA 的构建步骤与 .github/workflows/release-server.yaml 一致，不在镜像里重复构建。
#   规格：docs/specs/docker-single-image.md
#
# 注意：本镜像只装配产物，不负责编译。2.0 基线尚未提供 packages/server/scripts/build-sea.mjs
# 与 packages/server/packaging/（它们随旧 fork 的 SEA 发行改造引入），在补齐之前
# scripts/stage-docker-context.sh 会因为找不到 dist/sea/nex-server-<target> 直接报错退出。
FROM debian:bookworm-slim

# git：工作区/检查点功能；openssh-client：SSH 远程工作区（可选链路）；curl：健康检查。
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates openssh-client curl \
    && rm -rf /var/lib/apt/lists/*

COPY dist-docker/nex-server /usr/local/bin/nex-server
COPY dist-docker/web /app/web

ENV NODE_ENV=production \
    NEX_ENV=production \
    PORT=3030 \
    NEX_SERVER_HOST=0.0.0.0 \
    NEX_DATA_BASE_DIR=/data \
    NEX_SERVER_WORKSPACE=/workspace \
    NEX_WEB_STATIC_ROOT=/app/web

VOLUME ["/data", "/workspace"]
WORKDIR /workspace
EXPOSE 3030

# 健康检查走 `/`（SPA index.html）。2.0 基线的 `/api/*` 在配置了 NEX_SERVER_AUTH_TOKEN 时
# 一律要求鉴权（含 /api/server-info），没有旧 fork 的公开 /api/auth/session 端点；
# 而静态资源路径不受令牌保护，`/` 在镜像里恒定返回 200，只用来判断进程是否在服务。
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s \
  CMD curl -fsS "http://127.0.0.1:${PORT}/" >/dev/null || exit 1

CMD ["/usr/local/bin/nex-server"]
