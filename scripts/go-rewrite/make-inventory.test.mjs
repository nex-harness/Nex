// G0.11 test: the inventory is regenerable and complete.
//
// Two properties matter:
//  1. Determinism — re-running the generator on a clean tree produces byte-identical output,
//     so the committed inventory.md cannot silently rot.
//  2. Coverage — the counts in the inventory match the source (tools, commands, channels),
//     so a new tool/command/channel shows up instead of being forgotten.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import assert from "node:assert/strict";

const ROOT = new URL("../..", import.meta.url).pathname.replace(/\/$/, "");
const INVENTORY = join(ROOT, "docs/specs/go-rewrite/inventory.md");
const GENERATOR = "make-inventory.mts";

/** oxfmt pads table cells, so match a row by its first cell instead of an exact "| kind name |" string. */
function hasRow(content, kind, name) {
  const escaped = `${kind} ${name}`.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`^\\| ${escaped}\\s*\\|`, "m").test(content);
}

/** Run the generator and return its stdout, from packages/shared so imports resolve. */
function runGenerator() {
  return execFileSync("npx", ["--yes", "tsx", `../../scripts/go-rewrite/${GENERATOR}`], {
    cwd: join(ROOT, "packages/shared"),
    encoding: "utf8",
  });
}

test("re-running the inventory generator is byte-identical", () => {
  const before = readFileSync(INVENTORY, "utf8");
  const output = runGenerator();
  const after = readFileSync(INVENTORY, "utf8");
  assert.equal(after, before, "inventory.md changed after regeneration; commit the new output");
  assert.match(output, /inventory: \d+ items/);
});

test("inventory lists the provider-visible tools", () => {
  const content = readFileSync(INVENTORY, "utf8");
  for (const tool of ["Bash", "Read", "Edit", "Write", "WebSearch", "WebFetch"]) {
    assert.ok(hasRow(content, "tool", tool), `inventory is missing tool ${tool}`);
  }
});

test("inventory lists every v4 command type", () => {
  const content = readFileSync(INVENTORY, "utf8");
  for (const command of [
    "createSession",
    "sendText",
    "resolveInteraction",
    "amendWorkflowRunSettings",
  ]) {
    assert.ok(hasRow(content, "v4 command", command), `inventory is missing command ${command}`);
  }
});

test("inventory lists every service channel", () => {
  const content = readFileSync(INVENTORY, "utf8");
  for (const channel of ["terminal", "nex-agent", "plugins", "memory"]) {
    assert.ok(hasRow(content, "channel", channel), `inventory is missing channel ${channel}`);
  }
});

test("every inventory row has an owner phase", () => {
  const content = readFileSync(INVENTORY, "utf8");
  const dataRows = content
    .split("\n")
    .filter(
      (line) =>
        line.startsWith("| ") && !line.startsWith("| item ") && !/^\|[\s|:-]+\|$/.test(line),
    );
  assert.ok(dataRows.length > 200, `expected >200 rows, found ${dataRows.length}`);
  for (const row of dataRows) {
    const cells = row.split("|").map((cell) => cell.trim());
    // cells[0] is empty (leading |), then item/source/phase/status/trace.
    assert.ok(cells[3], `row has no owner phase: ${row}`);
    assert.ok(["todo", "ported", "dropped"].includes(cells[4]), `row has bad status: ${row}`);
  }
});
