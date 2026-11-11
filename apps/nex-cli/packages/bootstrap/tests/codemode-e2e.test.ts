import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { test } from "vitest";
import { createMcpAdapter } from "@nex/adapters";
import { CODEMODE_TOOL_NAME } from "@nex/contracts";
import {
  PermissionService,
  buildCodemodeDescription,
  builtInTools,
  createCodemodeToolEntry,
  createToolExecutor,
  createToolRegistry,
  defaultPermissionConfig,
  registerMcpTools,
  setCodemodeWorkerUrl,
} from "@nex/core";

const SERVER_PATH = fileURLToPath(new URL("./fixtures/echo-mcp-server.mjs", import.meta.url));
const WORKER_PATH = fileURLToPath(
  new URL("../../core/dist/codemode/runtime/worker.js", import.meta.url),
);

async function setup() {
  const mcpPort = createMcpAdapter();
  const snapshot = await mcpPort.connectConfiguredServers({
    fixture: { type: "stdio", command: process.execPath, args: [SERVER_PATH] },
  });
  const registry = createToolRegistry();
  registry.register(createCodemodeToolEntry(buildCodemodeDescription({ listedCandidates: [] })));
  // 与 toolSearch 同一条路径：MCP 工具默认 deferred，脚本仍可调用。
  registerMcpTools(registry, mcpPort, snapshot.tools, { deferNonOfficialTools: true });
  setCodemodeWorkerUrl(WORKER_PATH);
  const events: { type: string }[] = [];
  const executor = createToolExecutor({
    registry,
    // MCP 工具 needsApproval:true；这里用 bypass 配置放行以测路径本身，权限被拒的情形另测。
    permissionService: new PermissionService(defaultPermissionConfig),
    emitEvent: async (event) => {
      events.push(event as { type: string });
    },
    sessionId: "sess_codemode_e2e" as never,
    getMode: () => "yolo" as never,
  });
  return { mcpPort, registry, executor, events };
}

test("Codemode runs a script that calls a real MCP tool; the model sees only the summary", async () => {
  const { mcpPort, executor } = await setup();
  try {
    const result = await executor.execute({
      id: "call-1",
      name: CODEMODE_TOOL_NAME,
      input: {
        code: `
          const names = Object.keys(tools).filter((n) => n.includes("read_issue"));
          const issues = await Promise.all([1, 2, 3].map((n) => tools[names[0]]({ number: n })));
          // 中间结果每条 2KB+，只把标题汇总 return 给模型。
          return issues.map((i) => JSON.parse(i.content[0].text).title);
        `,
      },
    });
    assert.equal(result.success, true, JSON.stringify(result.error));
    const text =
      typeof result.modelContent === "string"
        ? result.modelContent
        : JSON.stringify(result.modelContent);
    assert.match(text, /Issue 1/);
    assert.match(text, /Issue 3/);
    assert.ok(!text.includes("xxxxxxxxxx"), "intermediate 2KB bodies must not reach the model");
    const output = result.output as { calls: { status: string }[] };
    assert.equal(output.calls.length, 3);
    assert.ok(output.calls.every((call) => call.status === "ok"));
  } finally {
    await mcpPort.close();
  }
});

test("Codemode cannot call itself and a failing child surfaces as a script error", async () => {
  const { mcpPort, executor } = await setup();
  try {
    const result = await executor.execute({
      id: "call-2",
      name: CODEMODE_TOOL_NAME,
      input: {
        code: `try { await tools.${CODEMODE_TOOL_NAME}({ code: "return 1" }); return "recursed"; } catch (e) { return "blocked: " + e.message; }`,
      },
    });
    assert.equal(result.success, true);
    assert.match(JSON.stringify(result.modelContent), /blocked/);
    assert.ok(!JSON.stringify(result.modelContent).includes("recursed"));
  } finally {
    await mcpPort.close();
  }
});

test("searchTools finds a deferred MCP tool inside the script without changing the declared set", async () => {
  const { mcpPort, executor, registry } = await setup();
  try {
    const before = registry
      .toContracts()
      .map((tool) => tool.name)
      .sort();
    const result = await executor.execute({
      id: "call-3",
      name: CODEMODE_TOOL_NAME,
      input: { code: `return (await searchTools("post chat message")).map((t) => t.name);` },
    });
    assert.equal(
      result.success,
      true,
      JSON.stringify(result.error) + JSON.stringify(result.modelContent),
    );
    assert.match(JSON.stringify(result.modelContent), /post_message/);
    assert.deepEqual(
      registry
        .toContracts()
        .map((tool) => tool.name)
        .sort(),
      before,
    );
  } finally {
    await mcpPort.close();
  }
});

test("a runaway script fails with an error result and the next call still works", async () => {
  const { mcpPort, executor } = await setup();
  try {
    const bad = await executor.execute({
      id: "call-4",
      name: CODEMODE_TOOL_NAME,
      input: { code: `// @options: {"timeout_ms": 500}\nwhile (true) {}` },
    });
    assert.equal(bad.success, false);
    assert.match(bad.error?.message ?? "", /timeout/);
    const ok = await executor.execute({
      id: "call-5",
      name: CODEMODE_TOOL_NAME,
      input: { code: "return 40 + 2;" },
    });
    assert.equal(ok.success, true);
    assert.match(JSON.stringify(ok.modelContent), /42/);
  } finally {
    await mcpPort.close();
  }
});

void builtInTools;
