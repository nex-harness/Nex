// G0.11: generate docs/specs/go-rewrite/inventory.md from the TypeScript source.
//
// The inventory is the checklist later phases tick off: every protocol method, notification,
// command, tool, service channel, migration and HTTP route, with its owner phase and status.
// It is GENERATED so it cannot silently rot; make-inventory.test.mjs re-runs it and requires
// byte-identical output.
//
// Status is 'ported' only for the pieces that actually exist in Go today (the S01 handshake
// surface); everything else is 'todo'. Owner phase is assigned by the rule table below and
// can be refined by editing this script, never by hand-editing the markdown.
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, readdirSync, writeFileSync, existsSync } from "node:fs";
import { join } from "node:path";

const ROOT = new URL("../..", import.meta.url).pathname.replace(/\/$/, "");
const OUT = join(ROOT, "docs/specs/go-rewrite/inventory.md");

const legacy = await import(join(ROOT, "packages/shared/src/nex-protocol/index.ts"));
const transport = await import(join(ROOT, "packages/shared/src/nex-protocol-v4/transport.ts"));
const command = await import(join(ROOT, "packages/shared/src/nex-protocol-v4/command.ts"));
const channels = await import(join(ROOT, "packages/shared/src/channels.ts"));
const migrations = await import(
  join(ROOT, "apps/nex-cli/packages/adapters/src/storage/session-store/migrations.ts")
);

const REL = (path) => path.replace(`${ROOT}/`, "");

function read(rel) {
  return readFileSync(join(ROOT, rel), "utf8");
}

function listFiles(rel) {
  const dir = join(ROOT, rel);
  if (!existsSync(dir)) return [];
  return readdirSync(dir)
    .filter((name) => name.endsWith(".ts") && !name.endsWith(".d.ts"))
    .sort()
    .map((name) => `${rel}/${name}`);
}

/** Rows are {item, source, phase, status, trace}. */
const rows = [];
const add = (item, source, phase, status = "todo", trace = "—") =>
  rows.push({ item, source, phase, status, trace });

// ── (a) legacy protocol methods + notifications ──────────────────────────────
const LEGACY_SRC = REL("packages/shared/src/nex-protocol/index.ts");
const KNOWN_PORTED = new Set(["runtime/capabilities", "startup/storageState"]);
for (const [key, method] of Object.entries(legacy.nexProtocolMethods)) {
  add(
    `method ${method}`,
    LEGACY_SRC,
    "1.1",
    KNOWN_PORTED.has(method) ? "ported" : "todo",
    KNOWN_PORTED.has(method) ? "S01" : "—",
  );
  void key;
}
for (const [key, method] of Object.entries(legacy.nexProtocolNotifications)) {
  add(
    `notification ${method}`,
    LEGACY_SRC,
    "1.1",
    KNOWN_PORTED.has(method) ? "ported" : "todo",
    KNOWN_PORTED.has(method) ? "S01" : "—",
  );
  void key;
}

// ── (b) v4 methods, notifications, commands ──────────────────────────────────
const TRANSPORT_SRC = REL("packages/shared/src/nex-protocol-v4/transport.ts");
for (const method of Object.values(transport.V4_METHODS)) {
  add(`v4 method ${method}`, TRANSPORT_SRC, "1.10", "todo");
}
for (const method of Object.values(transport.V4_NOTIFICATIONS)) {
  add(`v4 notification ${method}`, TRANSPORT_SRC, "1.10", "todo");
}

const COMMAND_SRC = REL("packages/shared/src/nex-protocol-v4/command.ts");
const commandTypes = Object.keys(command.commandPayloadSchemas).sort();
for (const type of commandTypes) {
  add(`v4 command ${type}`, COMMAND_SRC, "1.10", "todo");
}

// ── (c) tools ────────────────────────────────────────────────────────────────
// The authoritative provider-visible tool names are the members of
// SORTED_PROVIDER_TOOL_NAMES in core/src/tool/provider-visible-order.ts. Local (non
// provider-visible) tools are the `name:` literals in the tool handlers. Both sets are
// read from source so the inventory tracks the repository, not a hand-kept list.
const TOOL_ORDER_SRC = "apps/nex-cli/packages/core/src/tool/provider-visible-order.ts";
const providerToolNames = new Set(
  [
    ...read(TOOL_ORDER_SRC).matchAll(/SORTED_PROVIDER_TOOL_NAMES = new Set\(\[([\s\S]*?)\]\)/g),
  ].flatMap((match) => [...match[1].matchAll(/"([^"]+)"/g)].map((m) => m[1])),
);
for (const name of [...providerToolNames].sort()) {
  add(`tool ${name}`, TOOL_ORDER_SRC, "1.8", "todo");
}
const localToolNames = new Set();
for (const file of listFiles("apps/nex-cli/packages/core/src/tool/handlers")) {
  for (const match of read(file).matchAll(/name:\s*["'`]([A-Za-z][A-Za-z0-9_]*)["'`]/g)) {
    if (!providerToolNames.has(match[1])) localToolNames.add(match[1]);
  }
}
for (const name of [...localToolNames].sort()) {
  add(`tool ${name} (local)`, "apps/nex-cli/packages/core/src/tool/handlers", "1.8", "todo");
}

// ── (d) service channels ─────────────────────────────────────────────────────
const CHANNELS_SRC = REL("packages/shared/src/channels.ts");
// Tier-1 stateless IO adapters are ported (step 3.2); their trace is the
// G3 gate check that runs the package's race suite. The tier-4 knowledge
// extensions are ported (step 3.7; commands shares G3.memory with the
// memory service because one gate runs both packages' race suites).
// Everything else in phase 3 stays todo until its step lands.
const PORTED_CHANNELS: Record<string, string> = {
  commands: "G3.memory",
  file: "G3.file",
  "file-watcher": "G3.watcher",
  hooks: "G3.hooks",
  "mcp-sync": "G3.mcpsync",
  "media-preview": "G3.media",
  memory: "G3.memory",
  "plugin-management": "G3.plugins",
  "plugin-sync": "G3.plugins",
  plugins: "G3.plugins",
  "skill-sync": "G3.skills",
  skills: "G3.skills",
  system: "G3.system",
  // Tier 6a (step 3.9): IBotsService + the two POST /api/bots/* routes.
  bots: "G3.bots",
};
for (const channel of Object.values(channels.ServiceChannels).sort()) {
  const trace = PORTED_CHANNELS[channel];
  add(`channel ${channel}`, CHANNELS_SRC, "3", trace ? "ported" : "todo", trace ?? "—");
}

// ── (e) SQLite migrations ────────────────────────────────────────────────────
const MIGRATIONS_SRC = REL(
  "apps/nex-cli/packages/adapters/src/storage/session-store/migrations.ts",
);
for (const migration of migrations.SQLITE_MIGRATIONS) {
  add(`migration ${migration.id}`, MIGRATIONS_SRC, "1.3", "ported", "G1.3");
}

// ── (f) HTTP routes ──────────────────────────────────────────────────────────
const httpSources = ["packages/server/src/http.ts", "packages/server/src/webAuth.ts"];
const routeRegex = /app\.(get|post|put|delete|patch|use|all)\(\s*["'`]([^"'`]+)["'`]/g;
// The routes the Go shell now owns (step 3.8 spritesheet, step 3.9 bot
// callbacks). Both are answered by internal/server/{petsroute,botsroute}.go
// BEFORE the gateway is consulted, so they are ported even though the routing
// table still lists them for Node — dropping the Options.{Pets,Bots} wiring
// falls back to the upstream with no table edit, which is the same rollback
// shape both routes were shipped with.
const PORTED_ROUTES: Record<string, string> = {
  "POST /api/bots/:provider": "G3.bots",
  "POST /api/bots/:provider/:botId": "G3.bots",
};
for (const source of httpSources) {
  if (!existsSync(join(ROOT, source))) continue;
  const content = read(source);
  const seen = new Set();
  for (const match of content.matchAll(routeRegex)) {
    const key = `${match[1].toUpperCase()} ${match[2]}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const trace = PORTED_ROUTES[key];
    add(`route ${key}`, source, "2", trace ? "ported" : "todo", trace ?? "—");
  }
}

// ── render ───────────────────────────────────────────────────────────────────
const header = [
  "# Behavior inventory",
  "",
  "Generated by `scripts/go-rewrite/make-inventory.mts` (Phase 0 step 0.11). **Do not edit by hand.**",
  "Re-running the generator on a clean tree must produce identical output; `make-inventory.test.mjs` checks that.",
  "",
  "`status` is `ported` only for pieces that exist in Go today; everything else is `todo`.",
  "`trace` names the scenario or gate check that proves a ported item.",
  "",
];

const groups = [
  ["Legacy protocol methods and notifications", /^(method|notification) /],
  ["v4 methods, notifications and commands", /^v4 /],
  ["Agent tools", /^tool /],
  ["Service channels", /^channel /],
  ["SQLite migrations", /^migration /],
  ["HTTP routes", /^route /],
];

const lines = [...header];
for (const [title, pattern] of groups) {
  const group = rows.filter((row) => pattern.test(row.item));
  lines.push(`## ${title}`, "");
  lines.push(`Count: ${group.length}`, "");
  lines.push("| item | source | owner phase | status | trace |");
  lines.push("| ---- | ------ | ----------- | ------ | ----- |");
  for (const row of group) {
    lines.push(`| ${row.item} | ${row.source} | ${row.phase} | ${row.status} | ${row.trace} |`);
  }
  lines.push("");
}

lines.push("## Totals", "");
const statuses = { ported: 0, todo: 0, dropped: 0 };
for (const row of rows) statuses[row.status] = (statuses[row.status] ?? 0) + 1;
lines.push(`- items: ${rows.length}`);
lines.push(`- ported: ${statuses.ported}`);
lines.push(`- todo: ${statuses.todo}`);
lines.push("");

mkdirSync(join(ROOT, "docs/specs/go-rewrite"), { recursive: true });
writeFileSync(OUT, `${lines.join("\n")}`);
// 提交的文件必须已是 oxfmt 格式，否则 G0.0.fmt-own 会在每次重新生成后失败。
execFileSync(join(ROOT, "node_modules/.bin/oxfmt"), [OUT], { cwd: ROOT, stdio: "ignore" });
console.log(
  `inventory: ${rows.length} items (ported ${statuses.ported}, todo ${statuses.todo}); ` +
    `methods=${Object.keys(legacy.nexProtocolMethods).length}, commands=${commandTypes.length}, ` +
    `tools=${providerToolNames.size}+${localToolNames.size}, channels=${Object.keys(channels.ServiceChannels).length}`,
);
