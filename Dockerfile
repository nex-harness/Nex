# syntax=docker/dockerfile:1
# Nex server 镜像（无 Electron）。
#
# 2.0 的发行形态是 packages/nex-server-cli 的 staging 产物，不是单文件 SEA：
#   bin/nex + runtime/{node,server-cli.js,server-core.js,nex.cjs,node_modules,packages}
# 构建上下文 dist-docker/ 由 scripts/stage-docker-context.sh 准备，
# 因此本镜像只装配产物、不在镜像内编译；规格见 docs/specs/docker-single-image.md。
#
# 重要架构边界：server-core 只监听回环地址（packages/nex-server-cli/src/server-core/http.ts
# 的 fail-closed 检查），它面向本机/SSH 隧道入口，不直接对外提供 Web UI。
# 因此镜像里由 nginx 承担对外 :3030 —— 托管 Web 静态资源并把 /api、/ws 反代到回环上的 core。
FROM node:22-bookworm-slim AS base

# git：工作区/检查点；openssh-client：SSH 远程工作区；nginx：对外入口；curl：健康检查。
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates openssh-client nginx curl gettext-base \
    && rm -rf /var/lib/apt/lists/*

COPY dist-docker/nex-server /opt/nex
COPY dist-docker/web /app/web

# launcher 在 staging 时已有执行位，这里再显式确认一次，避免跨文件系统的 mode 丢失。
RUN chmod +x /opt/nex/bin/nex

ENV NODE_ENV=production \
    NEX_ENV=production \
    NEX_DATA_BASE_DIR=/data \
    NEX_SERVER_PORT=3030

VOLUME ["/data", "/workspace"]
WORKDIR /workspace
EXPOSE 3030

# 健康检查打对外入口：`/` 由 nginx 直接返回 SPA index.html，不经过需要 ingress token 的 core。
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s \
  CMD curl -fsS "http://127.0.0.1:3030/" >/dev/null || exit 1

COPY docker/nginx.conf /etc/nginx/nex-nginx.conf.template
COPY docker/entrypoint.sh /usr/local/bin/nex-entrypoint
RUN chmod +x /usr/local/bin/nex-entrypoint

ENTRYPOINT ["/usr/local/bin/nex-entrypoint"]
