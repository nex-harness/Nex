import assert from "node:assert/strict";
import { test } from "vitest";
import { NestedToolRunner } from "../src/tool/nested/runner.js";
import { createToolRegistry } from "../src/tool/registry.js";
import type { ToolEntry, ToolExecutionResult } from "../src/tool/types.js";
import type { ToolExecuteOptions } from "../src/tool/executor/types.js";

function entry(name: string, exposure?: "hidden"): ToolEntry {
  return {
    capability: name,
    metadata: {
      name,
      readOnly: true,
      destructive: false,
      concurrentSafe: true,
      sideEffectScope: "none",
      riskLevel: "low",
      needsApproval: false,
      ...(exposure ? { exposure } : {}),
    },
    inputSchema: { type: "object" },
    outputSchema: { type: "object" },
    handler: async () => ({}),
  } as unknown as ToolEntry;
}

function result(partial: Partial<ToolExecutionResult>): ToolExecutionResult {
  return {
    toolCallId: "x",
    toolName: "x",
    success: true,
    output: {},
    durationMs: 1,
    startedAt: new Date(),
    completedAt: new Date(),
    ...partial,
  };
}

function setup(
  execute: (
    call: { id: string; name: string; input: unknown },
    o?: ToolExecuteOptions,
  ) => Promise<ToolExecutionResult>,
  extra: Partial<ConstructorParameters<typeof NestedToolRunner>[0]> = {},
) {
  const registry = createToolRegistry();
  registry.register(entry("Read"));
  registry.register(entry("Secret", "hidden"));
  registry.register(entry("Codemode"));
  return new NestedToolRunner({
    executor: { execute },
    registry,
    parentToolCallId: "parent-1",
    forbiddenToolNames: new Set(["Codemode"]),
    ...extra,
  });
}

test("script receives the raw output while the model channel is the truncated text", async () => {
  const raw = { rows: Array.from({ length: 1000 }, (_, i) => i) };
  const runner = setup(async () =>
    result({ output: raw, modelContent: "rows: [0,1,2,… truncated]" }),
  );
  const res = await runner.call("Read", {});
  assert.deepEqual(res.data, raw);
  assert.equal(res.modelText, "rows: [0,1,2,… truncated]");
  assert.deepEqual(runner.auditRecords()[0]?.output, raw);
  assert.equal(runner.auditRecords()[0]?.parentToolCallId, "parent-1");
});

test("children go through the executor with the parent id and abort signal", async () => {
  let seen: ToolExecuteOptions | undefined;
  const controller = new AbortController();
  const runner = setup(
    async (_call, options) => {
      seen = options;
      return result({});
    },
    { signal: controller.signal },
  );
  await runner.call("Read", {});
  assert.equal(seen?.parentToolCallId, "parent-1");
  assert.equal(seen?.signal, controller.signal);
});

test("codemode cannot call itself, hidden and unknown tools are rejected without executing", async () => {
  let executed = 0;
  const runner = setup(async () => {
    executed += 1;
    return result({});
  });
  for (const name of ["Codemode", "Secret", "Nope"]) {
    const res = await runner.call(name, {});
    assert.equal(res.success, false, name);
  }
  assert.equal(executed, 0);
  assert.equal(runner.auditRecords().length, 3);
});

test("call-count limit rejects the excess and is audited", async () => {
  const runner = setup(async () => result({}), { limits: { maxCalls: 2 } });
  assert.equal((await runner.call("Read", {})).success, true);
  assert.equal((await runner.call("Read", {})).success, true);
  const third = await runner.call("Read", {});
  assert.equal(third.success, false);
  assert.match(third.errorMessage ?? "", /limit of 2/);
});

test("concurrency is capped but all parallel calls complete", async () => {
  let active = 0;
  let peak = 0;
  const runner = setup(
    async () => {
      active += 1;
      peak = Math.max(peak, active);
      await new Promise((resolve) => setTimeout(resolve, 15));
      active -= 1;
      return result({});
    },
    { limits: { maxConcurrency: 2 } },
  );
  const all = await Promise.all(Array.from({ length: 6 }, () => runner.call("Read", {})));
  assert.ok(all.every((r) => r.success));
  assert.equal(peak, 2);
});

test("an aborted script does not start new child calls", async () => {
  const controller = new AbortController();
  controller.abort();
  let executed = 0;
  const runner = setup(
    async () => {
      executed += 1;
      return result({});
    },
    { signal: controller.signal },
  );
  const res = await runner.call("Read", {});
  assert.equal(res.success, false);
  assert.equal(executed, 0);
});

test("oversized raw results fail the call instead of flooding the sandbox", async () => {
  const runner = setup(async () => result({ output: { big: "x".repeat(200) } }), {
    limits: { maxResultBytes: 50 },
  });
  const res = await runner.call("Read", {});
  assert.equal(res.success, false);
  assert.match(res.errorMessage ?? "", /script limit/);
});

test("executor failures surface as failed results the script can inspect", async () => {
  const runner = setup(async () =>
    result({ success: false, output: undefined, error: { type: "x", message: "denied by user" } }),
  );
  const res = await runner.call("Read", {});
  assert.equal(res.success, false);
  assert.equal(res.errorMessage, "denied by user");
});
