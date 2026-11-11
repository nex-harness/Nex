import assert from "node:assert/strict";
import { test } from "vitest";
import { CodemodeSandbox } from "../src/codemode/runtime/host.js";

// worker 线程不继承 tsx 的 .js→.ts 解析，测试必须跑编译产物（生产同样加载 dist 里的 worker.js）。
const WORKER_URL = new URL("../dist/codemode/runtime/worker.js", import.meta.url);

function sandbox(tools: ConstructorParameters<typeof CodemodeSandbox>[0] = {}) {
  return new CodemodeSandbox({ workerUrl: WORKER_URL, timeoutMs: 5000, ...tools });
}

test("script calls injected tools in parallel and returns a value", async () => {
  const box = sandbox({
    tools: [{ name: "add", execute: async (a) => (a as { a: number; b: number }).a + (a as { a: number; b: number }).b }],
  });
  const result = await box.execute(
    "const r = await Promise.all([tools.add({a:1,b:2}), tools.add({a:3,b:4})]); return r;",
  );
  assert.equal(result.ok, true);
  assert.deepEqual(result.ok && result.value, [3, 7]);
  assert.equal(result.calls.length, 2);
  await box.close();
});

test("no process, require, fetch, or module loading inside the sandbox", async () => {
  const box = sandbox();
  const result = await box.execute(
    "return [typeof process, typeof require, typeof fetch, typeof setTimeout, typeof import_meta_probe];",
  );
  assert.equal(result.ok, true);
  assert.deepEqual(result.ok && result.value, ["undefined", "undefined", "undefined", "undefined", "undefined"]);
  const imp = await box.execute('const m = await import("node:fs"); return typeof m;');
  assert.equal(imp.ok, false);
  await box.close();
});

test("a runaway script is killed by the timeout and the next run is unaffected", async () => {
  const box = sandbox();
  const started = Date.now();
  const runaway = await box.execute("while (true) {}", { timeoutMs: 400 });
  assert.equal(runaway.ok, false);
  assert.equal(!runaway.ok && runaway.error.kind, "timeout");
  assert.ok(Date.now() - started < 4000);
  const next = await box.execute("return 1 + 1;");
  assert.equal(next.ok && next.value, 2);
  await box.close();
});

test("aborting cancels in-flight tool calls", async () => {
  let toolSignal: AbortSignal | undefined;
  const box = sandbox({
    tools: [
      {
        name: "slow",
        execute: (_a, ctx) => {
          toolSignal = ctx.signal;
          return new Promise(() => undefined);
        },
      },
    ],
  });
  const controller = new AbortController();
  const pending = box.execute("return await tools.slow({});", { signal: controller.signal });
  await new Promise((r) => setTimeout(r, 300));
  controller.abort();
  const result = await pending;
  assert.equal(!result.ok && result.error.kind, "aborted");
  assert.equal(toolSignal?.aborted, true);
  await box.close();
});

test("text() and image() output reaches the result; thrown errors carry a message", async () => {
  const box = sandbox();
  const ok = await box.execute('text("hello"); text({a:1});');
  assert.deepEqual(ok.output, [
    { type: "text", text: "hello" },
    { type: "text", text: '{"a":1}' },
  ]);
  const bad = await box.execute('throw new Error("boom");');
  assert.equal(bad.ok, false);
  assert.match(!bad.ok ? bad.error.message : "", /boom/);
  await box.close();
});
