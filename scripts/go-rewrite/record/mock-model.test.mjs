import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { startMockModel } from "./mock-model.mjs";

async function post(port, body) {
  return fetch(`http://127.0.0.1:${port}/v1/chat/completions`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}
const logFile = () => join(mkdtempSync(join(tmpdir(), "mock-")), "req.jsonl");

test("replays steps in order and repeats the last one", async () => {
  const mock = await startMockModel({ steps: [{ text: "one" }, { text: "two" }] });
  try {
    const texts = [];
    for (let i = 0; i < 3; i++) {
      const r = await (await post(mock.port, { stream: false, messages: [] })).json();
      texts.push(r.choices[0].message.content);
    }
    assert.deepEqual(texts, ["one", "two", "two"]);
  } finally {
    await mock.close();
  }
});

test("streams text in chunks, ends with usage and [DONE]", async () => {
  const mock = await startMockModel({ steps: [{ text: "hello world!", chunkSize: 5 }] });
  try {
    const body = await (await post(mock.port, { stream: true, messages: [] })).text();
    const events = body.split("\n\n").filter(Boolean);
    assert.equal(events.at(-1), "data: [DONE]");
    const deltas = events.slice(0, -1).map((e) => JSON.parse(e.slice(6)));
    assert.equal(
      deltas
        .filter((d) => d.choices[0].delta.content)
        .map((d) => d.choices[0].delta.content)
        .join(""),
      "hello world!",
    );
    assert.equal(deltas.at(-1).choices[0].finish_reason, "stop");
    assert.equal(deltas.at(-1).usage.total_tokens, 120);
  } finally {
    await mock.close();
  }
});

test("tool-call arguments arrive split across two chunks and re-join to valid JSON", async () => {
  const mock = await startMockModel({
    steps: [{ toolCalls: [{ id: "call_1", name: "Read", args: { file_path: "/tmp/x" } }] }],
  });
  try {
    const body = await (await post(mock.port, { stream: true, messages: [] })).text();
    const deltas = body
      .split("\n\n")
      .filter((e) => e.startsWith("data: {"))
      .map((e) => JSON.parse(e.slice(6)));
    const args = deltas
      .flatMap((d) => d.choices[0].delta.tool_calls ?? [])
      .map((c) => c.function.arguments)
      .join("");
    assert.deepEqual(JSON.parse(args), { file_path: "/tmp/x" });
    assert.equal(deltas.at(-1).choices[0].finish_reason, "tool_calls");
  } finally {
    await mock.close();
  }
});

test("error steps return the HTTP status", async () => {
  const mock = await startMockModel({
    steps: [{ error: { status: 429, headers: { "retry-after": "1" } } }, { text: "ok" }],
  });
  try {
    const r = await post(mock.port, { stream: true, messages: [] });
    assert.equal(r.status, 429);
    assert.equal(r.headers.get("retry-after"), "1");
    assert.equal((await post(mock.port, { stream: false, messages: [] })).status, 200);
  } finally {
    await mock.close();
  }
});

test("dropAfterChunks cuts the stream before [DONE]", async () => {
  const mock = await startMockModel({
    steps: [{ text: "abcdefghijkl", chunkSize: 2, dropAfterChunks: 2 }],
  });
  try {
    const r = await post(mock.port, { stream: true, messages: [] });
    let text = "";
    let failed = false;
    try {
      text = await r.text();
    } catch {
      failed = true;
    }
    assert.ok(failed || !text.includes("[DONE]"), "stream must not complete normally");
  } finally {
    await mock.close();
  }
});

test("logs each request with tool names and tool-result counts", async () => {
  const file = logFile();
  const mock = await startMockModel({ steps: [{ text: "x" }], logFile: file });
  try {
    await post(mock.port, {
      stream: false,
      tools: [{ function: { name: "Read" } }],
      messages: [
        { role: "system", content: "s" },
        { role: "user", content: "hi" },
        { role: "tool", content: "r" },
      ],
    });
    const row = JSON.parse(readFileSync(file, "utf8").trim());
    assert.deepEqual(row.tools, ["Read"]);
    assert.equal(row.toolResultCount, 1);
    assert.equal(row.lastUserText, "hi");
    assert.deepEqual(row.messageRoles, ["system", "user", "tool"]);
  } finally {
    await mock.close();
  }
});
