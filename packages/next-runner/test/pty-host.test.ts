// pty-host 消险测试：真实启动 pty-host 子进程，跑默认 shell，验证
// create/write/输出/serialize/resize/kill 这条完整链路。
//
// 默认入口是 src/pty-host/ptyHost.ts（tsx 源码）；设置 NEX_PTY_HOST_ENTRY 可指向
// dist/pty-host.mjs，用同一份测试验证 bundle 产物（Linux 冒烟脚本即如此使用）。

import assert from "node:assert/strict";
import { test } from "node:test";
import { randomUUID } from "node:crypto";
import { PtyHostClient } from "../src/pty-host/client.js";
import { COALESCE_MAX_BYTES } from "../src/pty-host/session.js";

const IS_WINDOWS = process.platform === "win32";

async function collectUntil(
  client: PtyHostClient,
  id: string,
  predicate: (text: string) => boolean,
  timeoutMs = 15_000,
): Promise<string> {
  let text = "";
  const deadline = Date.now() + timeoutMs;
  while (!predicate(text)) {
    const remaining = deadline - Date.now();
    assert.ok(
      remaining > 0,
      `timed out waiting for output; got: ${JSON.stringify(text.slice(-400))}`,
    );
    text += await client.nextData(id, remaining);
  }
  return text;
}

test("pty-host: create, echo, serialize, resize, kill", { timeout: 45_000 }, async () => {
  const client = new PtyHostClient();
  const exits: Array<{ id: string; code: number }> = [];
  client.onSessionExit((event) => exits.push({ id: event.id, code: event.code }));
  await client.start();

  const id = `spike-${randomUUID()}`;
  try {
    const created = await client.create({ id, cols: 100, rows: 30, cwd: process.cwd() });
    assert.equal(created.id, id);
    assert.ok(created.pid > 0);
    console.log(
      `[pty-host test] created id=${created.id} pid=${created.pid} shell=${created.shell}`,
    );

    const marker = `pty-ok-${Math.floor(Math.random() * 1_000_000)}`;
    await client.write(id, `echo ${marker}\n`);
    const output = await collectUntil(client, id, (text) => text.includes(marker));
    assert.match(output, new RegExp(marker));

    const screen = await client.serialize(id);
    assert.ok(
      screen.includes(marker),
      `serialize() should contain ${marker}: ${screen.slice(-400)}`,
    );

    // 重连回放：每次 serialize 都是当前屏幕的独立快照。
    await client.resize(id, 132, 40);
    await client.write(id, `echo resized-$(( 1 + 1 ))\n`);
    const afterResize = await collectUntil(client, id, (text) => text.includes("resized-2"));
    assert.ok(afterResize.includes("resized-2"));
    const resizedScreen = await client.serialize(id);
    assert.ok(resizedScreen.includes("resized-2"), "serialized screen reflects post-resize output");

    const listed = await client.list();
    assert.ok(listed.some((terminal) => terminal.id === id));
    assert.equal(listed.find((terminal) => terminal.id === id)?.cols, 132);

    await client.kill(id);
    const deadline = Date.now() + 10_000;
    while (!exits.some((event) => event.id === id)) {
      assert.ok(Date.now() < deadline, "kill should emit an exit event");
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    console.log(`[pty-host test] exit code=${exits.find((event) => event.id === id)?.code}`);
    assert.deepEqual(await client.list(), []);
  } finally {
    await client.dispose();
  }
});

test("pty-host: client restarts the host after a crash", { timeout: 45_000 }, async () => {
  const client = new PtyHostClient();
  await client.start();
  const internal = client as unknown as { child: { kill(signal: NodeJS.Signals): boolean } | null };
  assert.ok(internal.child, "child process should be running");
  internal.child.kill("SIGKILL");

  // 崩溃后自动重启：下一次 create 能拿到新宿主的响应。
  const deadline = Date.now() + 15_000;
  let created: Awaited<ReturnType<PtyHostClient["create"]>> | null = null;
  while (!created && Date.now() < deadline) {
    created = await client.create({ cols: 80, rows: 24, cwd: process.cwd() }).catch(() => null);
    if (!created) await new Promise((resolve) => setTimeout(resolve, 100));
  }
  assert.ok(created, "host should restart after a crash");
  assert.ok(client.restarts >= 1);
  console.log(`[pty-host test] restarted host, restarts=${client.restarts}`);
  await client.kill(created.id);
  await client.dispose();
});

test("pty-host: pause/resume applies pty backpressure", { timeout: 45_000 }, async () => {
  if (IS_WINDOWS) return; // 该用例依赖 `yes`，Windows 上没有。
  const client = new PtyHostClient();
  await client.start();
  const id = `flood-${randomUUID()}`;
  try {
    await client.create({ id, cols: 120, rows: 30, cwd: process.cwd() });

    let received = 0;
    const onData = (event: { id: string; data: string }): void => {
      if (event.id === id) received += event.data.length;
    };
    client.onSessionData(onData);
    await client.write(id, "yes pty-flood | head -c 4000000\n");
    await new Promise((resolve) => setTimeout(resolve, 500));
    client.pause(id);
    await new Promise((resolve) => setTimeout(resolve, 200));
    const pausedAt = received;
    await new Promise((resolve) => setTimeout(resolve, 1000));
    // pty 暂停后内核缓冲很快就会写满，期间到达的字节应远小于持续输出总量。
    const duringPause = received - pausedAt;
    client.resume(id);
    client.off("data", onData);
    console.log(`[pty-host test] paused window received ${duringPause} bytes`);
    assert.ok(duringPause < 400_000, `paused pty should not flood (got ${duringPause} bytes)`);

    // 合并：单个 data 事件不超过 64 KiB 上限，且事件数明显少于原始读块的量级。
    const chunkSizes: number[] = [];
    const measure = (event: { id: string; data: string }): void => {
      if (event.id === id) chunkSizes.push(Buffer.byteLength(event.data, "utf8"));
    };
    client.onSessionData(measure);
    const marker = `flood-done-${Math.floor(Math.random() * 1_000_000)}`;
    await client.write(id, `yes pty-chunks | head -c 2000000; echo ${marker}\n`);
    await collectUntil(client, id, (text) => text.includes(marker), 20_000);
    client.off("data", measure);
    const total = chunkSizes.reduce((sum, size) => sum + size, 0);
    console.log(
      `[pty-host test] flood: ${chunkSizes.length} events, ${total} bytes, max ${Math.max(...chunkSizes, 0)}`,
    );
    assert.ok(chunkSizes.length > 0, "flood should produce data events");
    assert.ok(
      Math.max(...chunkSizes) <= COALESCE_MAX_BYTES,
      "data events stay within the 64 KiB cap",
    );
    assert.ok(total >= 2_000_000, "no output was dropped by coalescing");
  } finally {
    await client.dispose();
  }
});
