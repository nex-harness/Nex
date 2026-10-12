// G0.9 test: prove the recorded RPC frames are faithful.
//
// For every frame in go/testdata/traces/rpc/*/frames.jsonl, decode the raw bytes with the
// real deserialize() and re-serialize the decoded {header, body}; the re-encoded bytes must
// equal the recorded hex. This proves the capture went through the real codec and that the
// recorded hex is exactly what the TypeScript codec would emit.
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import assert from "node:assert/strict";

const ROOT = new URL("../../..", import.meta.url).pathname.replace(/\/$/, "");
const { deserialize, serialize, BufferReader, BufferWriter, VSBuffer } = await import(
  join(ROOT, "packages/rpc/dist/index.js")
);

const TRACE_ROOT = join(ROOT, "go/testdata/traces/rpc");

function expectedHex(value) {
  const writer = new BufferWriter();
  serialize(writer, value);
  return Buffer.from(writer.buffer.buffer).toString("hex");
}

const scenarios = existsSync(TRACE_ROOT)
  ? readdirSync(TRACE_ROOT, { withFileTypes: true })
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .sort()
  : [];

test("rpc trace directory contains the recorded scenarios", () => {
  assert.ok(
    scenarios.length >= 9,
    `expected >=9 scenarios, found ${scenarios.length}: ${scenarios}`,
  );
});

for (const id of scenarios) {
  test(`frames in ${id} re-serialize byte-identically`, () => {
    const lines = readFileSync(join(TRACE_ROOT, id, "frames.jsonl"), "utf8")
      .trim()
      .split("\n");
    assert.ok(lines.length > 0, `${id} has no frames`);
    for (const [index, line] of lines.entries()) {
      const frame = JSON.parse(line);
      const raw = Buffer.from(frame.hex, "hex");
      const reader = new BufferReader(VSBuffer.wrap(raw));
      const header = deserialize(reader);
      const body = deserialize(reader);
      const headerHex = expectedHex(header);
      const bodyHex = expectedHex(body);
      const combined = headerHex + bodyHex;
      assert.equal(
        combined,
        frame.hex,
        `${id} frame ${index} (${frame.direction}) did not round-trip: ${combined} != ${frame.hex}`,
      );
      assert.equal(reader.pos ?? raw.length, raw.length, `${id} frame ${index}: trailing bytes`);
    }
  });
}

test("a frame with a structured error carries code and name", () => {
  const lines = readFileSync(join(TRACE_ROOT, "R08", "frames.jsonl"), "utf8")
    .trim()
    .split("\n");
  const errorFrame = lines.map((l) => JSON.parse(l)).find((f) => f.decoded?.header?.[0] === 202);
  assert.ok(errorFrame, "R08 has no PromiseError frame");
  assert.equal(errorFrame.decoded.body.name, "ExplodedError");
  assert.equal(errorFrame.decoded.body.code, "E_EXPLODE");
});

test("no frame contains an absolute filesystem path", () => {
  for (const id of scenarios) {
    const content = readFileSync(join(TRACE_ROOT, id, "frames.jsonl"), "utf8");
    assert.ok(!content.includes("/mnt/"), `${id} leaks the repo path`);
    assert.ok(!content.includes("file:///"), `${id} leaks an absolute file URL`);
    assert.ok(!/\/home\/[a-z]/i.test(content), `${id} leaks a home directory`);
  }
});

test("the R09 burst contains many small frames and one large frame", () => {
  const lines = readFileSync(join(TRACE_ROOT, "R09", "frames.jsonl"), "utf8")
    .trim()
    .split("\n");
  assert.ok(lines.length > 200, `expected >200 frames in R09, got ${lines.length}`);
  const large = lines.map((l) => JSON.parse(l)).some((f) => f.hex.length > 1024 * 1024);
  assert.ok(large, "R09 has no frame larger than 1 MiB");
});
