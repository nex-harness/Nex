// G0.4: export the protocol surface (phase-0 step 0.4) to JSON Schema + manifest + method tables.
//
// Output (deterministic, sorted):
//   go/contracts/schema/<id>.json          one JSON Schema per surface schema
//   go/contracts/schema/manifest.json      ids -> source, unsupported, refinements
//   go/contracts/schema/methods.json       protocol method/notification tables + limits
//
// Not every schema can be converted with `z.toJSONSchema`; refinements (superRefine) and
// transforms are lossy there. The manifest records each one with a reason, so the Go side
// (step 0.5) knows exactly which rules it must implement by hand. This exporter never
// silently drops a rule: run it and read `unsupported`/`refinements`.
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { z } from "zod";
import { methodTables, resolveSurface, type ResolvedSurfaceEntry } from "./schema-surface.mjs";

const OUT = new URL("../../go/contracts/schema/", import.meta.url);
const LEGACY_SOURCE = "packages/shared/src/nex-protocol/index.ts";

interface Unsupported {
  id: string;
  source: string;
  reason: string;
}

interface Refinement {
  id: string;
  source: string;
  /** Number of `.superRefine`/`.refine`/`.check` calls found on the root or its members. */
  count: number;
  note: string;
}

/** Stable JSON: 2-space indent, keys sorted at every level. Deterministic across runs. */
function stableStringify(value: unknown): string {
  return `${JSON.stringify(sortKeys(value), null, 2)}\n`;
}

function sortKeys(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sortKeys);
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value as Record<string, unknown>).sort()) {
      out[key] = sortKeys((value as Record<string, unknown>)[key]);
    }
    return out;
  }
  return value;
}

/** Count refinement checks reachable from a schema, so the manifest can flag them. */
function countChecks(schema: z.ZodTypeAny): number {
  let count = 0;
  const seen = new Set<unknown>();
  const visit = (node: unknown): void => {
    if (!node || typeof node !== "object" || seen.has(node)) return;
    seen.add(node);
    const def = (node as { def?: Record<string, unknown> }).def;
    if (def) {
      if (Array.isArray(def.checks) && def.checks.length > 0) count += def.checks.length;
      for (const value of Object.values(def)) visit(value);
    }
  };
  visit(schema);
  return count;
}

function jsonSchemaFor(entry: ResolvedSurfaceEntry): {
  schema?: unknown;
  unsupported?: Unsupported;
} {
  try {
    const schema = z.toJSONSchema(entry.schema, { unrepresentable: "any", io: "input" });
    return { schema };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return {
      // Fall back to a permissive schema so consumers always get a file; the manifest
      // records why it is permissive rather than pretending the conversion succeeded.
      schema: { $schema: "https://json-schema.org/draft/2020-12/schema" },
      unsupported: { id: entry.id, source: entry.source, reason: message },
    };
  }
}

function main(): void {
  const surface = resolveSurface();
  rmSync(OUT, { recursive: true, force: true });
  mkdirSync(OUT, { recursive: true });

  const unsupported: Unsupported[] = [];
  const refinements: Refinement[] = [];
  const exported: Array<{ id: string; source: string; root: boolean }> = [];

  for (const entry of surface) {
    const { schema, unsupported: problem } = jsonSchemaFor(entry);
    writeFileSync(new URL(`${entry.id}.json`, OUT), stableStringify(schema));
    exported.push({ id: entry.id, source: entry.source, root: entry.root });
    if (problem) unsupported.push(problem);

    const checks = countChecks(entry.schema);
    if (checks > 0) {
      refinements.push({
        id: entry.id,
        source: entry.source,
        count: checks,
        note: "refinements are not expressible in JSON Schema; the Go validator must re-implement them",
      });
    }
  }

  const manifest = {
    generatedFrom: "scripts/go-rewrite/export-schemas.mts",
    converter: "z.toJSONSchema (zod 4)",
    legacySource: LEGACY_SOURCE,
    count: exported.length,
    schemas: exported,
    unsupported,
    refinements,
  };
  writeFileSync(new URL("manifest.json", OUT), stableStringify(manifest));

  const tables = methodTables();
  const methods = {
    generatedFrom: "scripts/go-rewrite/export-schemas.mts",
    V4_WIRE_PROTOCOL_VERSION: tables.V4_WIRE_PROTOCOL_VERSION,
    PROTOCOL_V4_LIMITS: tables.PROTOCOL_V4_LIMITS,
    nexProtocolMethods: tables.nexProtocolMethods,
    nexProtocolNotifications: tables.nexProtocolNotifications,
    V4_METHODS: tables.V4_METHODS,
    V4_NOTIFICATIONS: tables.V4_NOTIFICATIONS,
  };
  writeFileSync(new URL("methods.json", OUT), stableStringify(methods));

  console.log(
    `exported ${exported.length} schemas (${exported.filter((e) => e.root).length} roots); ` +
      `unsupported=${unsupported.length}, refinements=${refinements.length}; ` +
      `methods=${Object.keys(tables.nexProtocolMethods).length}+${Object.keys(tables.nexProtocolNotifications).length}, ` +
      `V4=${Object.keys(tables.V4_METHODS).length}+${Object.keys(tables.V4_NOTIFICATIONS).length}`,
  );
  if (unsupported.length > 0) {
    console.log("unsupported:");
    for (const u of unsupported) console.log(`  ${u.id}: ${u.reason}`);
  }
}

main();
