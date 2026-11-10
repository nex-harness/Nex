#!/bin/sh
# 启动 Nex Server Core（回环地址，随机端口）与 nginx（对外固定端口），两者同生命周期。
#
# 端口与 ingress token 都是 core 启动后写入 <serverRoot>/run/ 的事实：
#   run/status.json     { state, host, port, ... } —— 由 supervisor 持久化
#   run/ingress-token   每次启动轮换的 Bearer token（0600）
# nginx 必须等这两个文件就绪后才知道往哪里转发。
set -e

SERVER_ROOT="${NEX_DATA_BASE_DIR:-/data}"
PUBLIC_PORT="${NEX_SERVER_PORT:-3030}"
RUN_DIR="$SERVER_ROOT/run"
STATUS_FILE="$RUN_DIR/status.json"
TOKEN_FILE="$RUN_DIR/ingress-token"

mkdir -p "$SERVER_ROOT"

# serve 不带 --daemon 时前台运行 supervisor，容器日志直接承载服务输出。
/opt/nex/bin/nex serve --server-root "$SERVER_ROOT" &
core_pid=$!

# 等 status.json 报告带端口的 ready 状态。
i=0
core_host=""
core_port=""
while :; do
  i=$((i + 1))
  if [ "$i" -gt 180 ]; then
    echo "[entrypoint] server core did not report a port within 90s" >&2
    kill "$core_pid" 2>/dev/null || true
    exit 1
  fi
  if ! kill -0 "$core_pid" 2>/dev/null; then
    echo "[entrypoint] server core exited before becoming ready" >&2
    exit 1
  fi
  if [ -f "$STATUS_FILE" ]; then
    core_port="$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$STATUS_FILE" | head -n 1)"
    core_host="$(sed -n 's/.*"host"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$STATUS_FILE" | head -n 1)"
    if [ -n "$core_port" ] && [ -f "$TOKEN_FILE" ]; then
      break
    fi
  fi
  sleep 0.5
done

# supervisor 报告的 host 可能是通配地址；容器内一律走回环。
case "$core_host" in
  0.0.0.0 | :: | "") core_host="127.0.0.1" ;;
esac

echo "[entrypoint] core listening on $core_host:$core_port; nginx on :$PUBLIC_PORT"

export NEX_CORE_HOST="$core_host"
export NEX_CORE_PORT="$core_port"
export NEX_INGRESS_TOKEN
NEX_INGRESS_TOKEN="$(cat "$TOKEN_FILE")"

envsubst '${NEX_CORE_HOST} ${NEX_CORE_PORT} ${NEX_INGRESS_TOKEN}' \
  < /etc/nginx/nex-nginx.conf.template > /etc/nginx/conf.d/default.conf

nginx -g 'daemon off;' &
nginx_pid=$!

# 任一进程退出就收掉另一个，避免容器半活。
wait -n 2>/dev/null || wait
status=$?
kill "$core_pid" "$nginx_pid" 2>/dev/null || true
exit "$status"
