import test from "node:test";
import assert from "node:assert/strict";
import { scanText } from "./scrub-check.mjs";

test("flags secret-shaped strings", () => {
  const bad = [
    "sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
    "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123",
    "AKIAABCDEFGHIJKLMNOP",
    "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
    "-----BEGIN RSA PRIVATE KEY-----",
    "alice@corp.io",
    "/home/alice/project/",
    "C:\\Users\\alice\\x\\",
  ];
  for (const s of bad) assert.ok(scanText(`x ${s} y`).length > 0, `should flag: ${s}`);
});
test("allows the deliberate fake key and example.com", () => {
  assert.deepEqual(scanText('{"apiKey":"sk-test-fake"} a@example.com'), []);
});
test("does not flag ordinary trace content", () => {
  assert.deepEqual(
    scanText('{"method":"v4/command","params":{"text":"hello","path":"<WS>/hello.txt"}}'),
    [],
  );
});
