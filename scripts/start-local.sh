#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

ADDR="${NEX_ADDR:-127.0.0.1:8080}"
DATA_ROOT="${NEX_DATA_ROOT:-${HOME}/.nex/v3-local}"
WORKSPACE="${NEX_WORKSPACE:-${ROOT_DIR}}"

if [[ ! -x "./bin/nex-server" || ! -x "./bin/nex-runner" ]]; then
  echo "==> Building nex-server and nex-runner..."
  make build
fi

mkdir -p "${DATA_ROOT}/server" "${DATA_ROOT}/runner"

cleanup() {
  echo ""
  echo "==> Stopping local nex-runner and nex-server..."
  [[ -n "${RUNNER_PID:-}" ]] && kill "${RUNNER_PID}" 2>/dev/null || true
  [[ -n "${SERVER_PID:-}" ]] && kill "${SERVER_PID}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

./bin/nex-server -addr "${ADDR}" -data-dir "${DATA_ROOT}/server" &
SERVER_PID=$!

for _ in {1..30}; do
  if curl -fsS "http://${ADDR}/api/v1/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.15
done

# Optionally bootstrap local providers/models from ~/.nex/v2/provider_config.json via REST API if present
V2_CFG="${HOME}/.nex/v2/provider_config.json"
NODE_BIN="$(command -v node || echo /Users/tylinux/.nvm/versions/node/v23.9.0/bin/node)"
if [[ -f "${V2_CFG}" && -x "${NODE_BIN}" ]]; then
  "${NODE_BIN}" - "${V2_CFG}" "http://${ADDR}" <<'EOF'
const fs = require('node:fs');
const [cfgPath, base] = process.argv.slice(2);
(async () => {
  try {
    const raw = JSON.parse(fs.readFileSync(cfgPath, 'utf8'));
    const rules = raw?.config?.providerConfigRules?.providerRules || [];
    for (const r of rules) {
      const apiType = r?.config?.api?.type;
      const driver = apiType === 'anthropic' ? 'anthropic' : 'openai_responses';
      await fetch(`${base}/api/v1/providers`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          id: r.providerId,
          name: r.providerName || r.providerId,
          driver,
          base_url: r?.config?.api?.baseUrl || '',
          api_key: r?.config?.access?.apiKey || '',
          proxy_mode: 'direct',
          enabled: true,
        }),
      });
    }
    const mRules = raw?.config?.modelConfigRules?.providerModelRules || [];
    for (const mr of mRules) {
      if (!mr?.config?.enabled) continue;
      const props = mr?.config?.properties || {};
      const inp = props?.inputFormat || {};
      await fetch(`${base}/api/v1/models`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          id: mr.modelId,
          provider_id: mr.providerId,
          display_name: mr.modelId,
          source: 'custom',
          context_window: props.contextWindow || 200000,
          max_output_tokens: mr?.config?.optionSpecs?.maxOutputTokens?.max || 65536,
          supports_vision: Boolean(inp.supportsImage),
          supports_pdf: Boolean(inp.supportsPdf),
          supports_tools: Boolean(props.supportsToolCall ?? true),
          supports_reasoning: Boolean(mr?.config?.optionSpecs?.reasoningLevel),
          supports_caching: true,
        }),
      });
    }
  } catch {}
})();
EOF
fi

./bin/nex-runner \
  -id "run_local_$(uname -s | tr '[:upper:]' '[:lower:]')" \
  -name "Local $(scutil --get ComputerName 2>/dev/null || hostname)" \
  -server "ws://${ADDR}/ws/runner" \
  -data-dir "${DATA_ROOT}/runner" \
  -workspace "${WORKSPACE}" &
RUNNER_PID=$!

echo "==> Nex-v3 Local Workbench is running at: http://${ADDR}"
wait "${SERVER_PID}"
