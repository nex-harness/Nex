import assert from "node:assert/strict";
import { test } from "vitest";
import {
  buildCodemodeDescription,
  CODEMODE_INLINE_BUDGET_TOKENS,
} from "../src/codemode/description.js";
import { renderCodemodeOutput } from "../src/codemode/output.js";
import { parseCodemodeSource } from "../src/codemode/source.js";
import type { CodemodeResult } from "../src/codemode/types.js";

const entry = (name: string, description: string) => ({ name, description, inputSchema: {} });

test("description lists tools up to the inline budget and says how many were left out", () => {
  const many = Array.from({ length: 400 }, (_, i) =>
    entry(`tool_${String(i).padStart(3, "0")}`, "d".repeat(100)),
  );
  const text = buildCodemodeDescription({ listedCandidates: many });
  const budgetChars = CODEMODE_INLINE_BUDGET_TOKENS * 4;
  const listedSection = text.slice(text.indexOf("Listed tools:"));
  assert.ok(listedSection.length < budgetChars + 400);
  assert.match(text, /\d+ more tools are not listed here/);
});

test("description is independent of the order tools were registered (cache-stable)", () => {
  const a = [entry("b_tool", "B"), entry("a_tool", "A")];
  const b = [entry("a_tool", "A"), entry("b_tool", "B")];
  assert.equal(
    buildCodemodeDescription({ listedCandidates: a }),
    buildCodemodeDescription({ listedCandidates: b }),
  );
});

test("output over the token budget is truncated and flagged; the full text is kept for spill", () => {
  const result: CodemodeResult = {
    ok: true,
    value: "x".repeat(10_000),
    output: [{ type: "text", text: "head" }],
    calls: [],
    storeWrites: { set: {}, delete: [] },
  };
  const rendered = renderCodemodeOutput(result, 100);
  assert.equal(rendered.truncated, true);
  assert.equal(rendered.text.length, 400);
  assert.ok(rendered.fullText.length > 10_000);
  const small = renderCodemodeOutput(result, 10_000);
  assert.equal(small.truncated, false);
});

test("@options line is parsed and rejects unknown fields", () => {
  const parsed = parseCodemodeSource(
    '// @options: {"max_output_tokens": 50, "timeout_ms": 1000}\nreturn 1;',
  );
  assert.deepEqual(parsed.options, { maxOutputTokens: 50, timeoutMs: 1000 });
  assert.throws(() => parseCodemodeSource('// @options: {"nope": 1}\nreturn 1;'));
});

test("oneLine collapses a multi-paragraph description to a short single line", async () => {
  const { oneLine } = await import("../src/codemode/description.js");
  const long = `First paragraph.\n\n## When to Use\n${"x".repeat(5000)}`;
  const line = oneLine(long);
  assert.ok(!line.includes("\n"));
  assert.ok(line.length <= 120);
  assert.ok(line.startsWith("First paragraph."));
});
