# ToolSearch + Codemode end-to-end

Proves the whole path in a real server (dev build or SEA binary) without a real model:

1. `node mock-model.mjs` — scripted OpenAI-compatible endpoint on `127.0.0.1:18999`.
2. A throwaway `HOME` containing:
   - provider config registering the mock model (`http://127.0.0.1:18999/v1`; 2.0 uses the
     provider registry, see `~/.nex` provider files written by the app);
   - `.nex/cli/config.json` with an stdio MCP server running
     `../fixtures/echo-mcp-server.mjs` and
     `permission.autoApproveHighRisk` / `allowMediumRiskInAuto` set;
   - 2.0 设置项是 `toolSearchMode`（`auto` / `local` / `off`，默认 `off`）和
     `codemodeEnabled`（默认 `false`），都写在 `~/.nex/v2/setting.json`，例如
     `{"toolSearchMode":"auto","codemodeEnabled":true}`。
3. Start the server with `HOME`/`NEX_DATA_BASE_DIR` pointing at that directory and a clean
   environment (`env -i`, no `NEX_AGENT_SERVER_COMMAND` / `NEX_SEA_AGENT_ROLE`).
   For the SEA binary, copy it to a path outside the repo first: the agent child is spawned
   from `process.execPath` and a binary inside the monorepo is resolved as a dev runtime.
4. In the web UI pick the mock model and a workspace, then send any prompt. Approve the
   `mcp__fixture__read_issue` permission prompt; it can reappear, so keep approving until the
   turn finishes (the dev run needed one approval, the SEA run two).

Expected request log (`$MOCK_MODEL_LOG`):

| request | tool results in history | `mcp__fixture__read_issue` declared |
| --- | --- | --- |
| 1 | 0 | no (only `ToolSearch` and `Codemode` are visible) |
| 2 | 1 (the ToolSearch hit) | yes |
| 3 | 2 (ToolSearch + the Codemode summary `Issue 1, Issue 2, Issue 3`) | yes |

The 2 KB issue bodies fetched inside the script never appear in the log.
