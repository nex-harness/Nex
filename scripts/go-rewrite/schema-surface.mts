// Schema surface shared by the Phase 0 tooling (G0.4 export, G0.5 codegen, G0.6 corpus).
//
// The Go agent speaks a *protocol surface*, not every zod object in packages/shared.
// This module is the single source of truth for which schemas are in that surface, and
// it exposes a small reflective walker over zod 4's `def` tree so the exporter, the Go
// emitter and the corpus generator all describe the same shapes.
//
// Keep this list explicit and reviewable: adding a schema here means the Go side must
// accept exactly what zod accepts for it (proved by the G0.6 corpus).

import type { z } from "zod";
import * as legacy from "../../packages/shared/src/nex-protocol/index.js";
import * as transport from "../../packages/shared/src/nex-protocol-v4/transport.js";
import * as command from "../../packages/shared/src/nex-protocol-v4/command.js";
import * as delta from "../../packages/shared/src/nex-protocol-v4/delta.js";
import * as snapshot from "../../packages/shared/src/nex-protocol-v4/snapshot.js";
import * as rows from "../../packages/shared/src/nex-protocol-v4/rows.js";
import * as sessionsIndex from "../../packages/shared/src/nex-protocol-v4/sessions-index.js";
import * as workspaceConfig from "../../packages/shared/src/nex-protocol-v4/workspace-config.js";
import * as core from "../../packages/shared/src/nex-protocol-v4/core.js";
import * as telemetry from "../../packages/shared/src/nex-protocol-v4/telemetry.js";
import * as submission from "../../packages/shared/src/nex-protocol-v4/submission.js";
import * as attachmentRef from "../../packages/shared/src/nex-protocol-v4/attachment-ref.js";
import * as modelSelection from "../../packages/shared/src/model-selection.js";
import * as modelExecution from "../../packages/shared/src/model-execution.js";

/** A schema to export, with a stable id and where it came from. */
export interface SurfaceEntry {
  /** Stable identifier used for file names and Go symbol names. */
  id: string;
  /** Repository-relative path to the defining module. */
  source: string;
  /** The live zod schema object. */
  schema: z.ZodTypeAny;
}

const LEGACY = "packages/shared/src/nex-protocol/index.ts";
const TRANSPORT = "packages/shared/src/nex-protocol-v4/transport.ts";
const COMMAND = "packages/shared/src/nex-protocol-v4/command.ts";
const DELTA = "packages/shared/src/nex-protocol-v4/delta.ts";
const SNAPSHOT = "packages/shared/src/nex-protocol-v4/snapshot.ts";
const ROWS = "packages/shared/src/nex-protocol-v4/rows.ts";
const SESSIONS_INDEX = "packages/shared/src/nex-protocol-v4/sessions-index.ts";
const WORKSPACE_CONFIG = "packages/shared/src/nex-protocol-v4/workspace-config.ts";
const TELEMETRY = "packages/shared/src/nex-protocol-v4/telemetry.ts";
const SUBMISSION_SRC = "packages/shared/src/nex-protocol-v4/submission.ts";
const ATTACHMENT_REF_SRC = "packages/shared/src/nex-protocol-v4/attachment-ref.ts";
const MODEL_SELECTION_SRC = "packages/shared/src/model-selection.ts";
const MODEL_EXECUTION_SRC = "packages/shared/src/model-execution.ts";
const CORE_SRC = "packages/shared/src/nex-protocol-v4/core.ts";

type AnySchema = z.ZodTypeAny;

/** Duck-type a zod 4 schema. `z.toJSONSchema` throws for non-schemas, so check first. */
export function isZodSchema(value: unknown): value is AnySchema {
  return (
    typeof value === "object" &&
    value !== null &&
    "def" in value &&
    typeof (value as { safeParse?: unknown }).safeParse === "function"
  );
}

/**
 * Collect every `*Schema` export of a module whose value looks like a zod schema.
 * Additionally collect `Schema`-suffixed keys inside exported records of schemas
 * (e.g. every payload of `commandPayloadSchemas`), so the command family is reachable.
 * This keeps the surface honest: when a module gains a schema, the corpus and the Go
 * emitter pick it up on the next run instead of drifting silently.
 */
const EXTRA_SCHEMA_SOURCES: Array<{ source: string; name: string; value: AnySchema }> = [];

function collectSchemas(source: string, mod: Record<string, unknown>): Record<string, AnySchema> {
  const out: Record<string, AnySchema> = {};
  for (const [name, value] of Object.entries(mod)) {
    if (name.endsWith("Schema") && isZodSchema(value)) {
      out[name] = value;
      continue;
    }
    // Records of schemas, e.g. commandPayloadSchemas.
    if (
      name.endsWith("Schemas") &&
      typeof value === "object" &&
      value !== null &&
      !isZodSchema(value)
    ) {
      for (const [key, inner] of Object.entries(value as Record<string, unknown>)) {
        if (!isZodSchema(inner)) continue;
        // Namespaced id so command payloads do not collide with top-level schemas.
        const alias = `${name}$$${key}`;
        out[alias] = inner;
        EXTRA_SCHEMA_SOURCES.push({ source, name: alias, value: inner });
      }
    }
  }
  return out;
}

/** Module path -> its exported `*Schema` constants. */
const MODULES: Array<{ source: string; schemas: Record<string, AnySchema> }> = [
  { source: LEGACY, schemas: collectSchemas(LEGACY, legacy as Record<string, unknown>) },
  { source: TRANSPORT, schemas: collectSchemas(TRANSPORT, transport as Record<string, unknown>) },
  { source: COMMAND, schemas: collectSchemas(COMMAND, command as Record<string, unknown>) },
  { source: DELTA, schemas: collectSchemas(DELTA, delta as Record<string, unknown>) },
  { source: SNAPSHOT, schemas: collectSchemas(SNAPSHOT, snapshot as Record<string, unknown>) },
  { source: ROWS, schemas: collectSchemas(ROWS, rows as Record<string, unknown>) },
  {
    source: SESSIONS_INDEX,
    schemas: collectSchemas(SESSIONS_INDEX, sessionsIndex as Record<string, unknown>),
  },
  {
    source: WORKSPACE_CONFIG,
    schemas: collectSchemas(WORKSPACE_CONFIG, workspaceConfig as Record<string, unknown>),
  },
  { source: TELEMETRY, schemas: collectSchemas(TELEMETRY, telemetry as Record<string, unknown>) },
  {
    source: SUBMISSION_SRC,
    schemas: collectSchemas(SUBMISSION_SRC, submission as Record<string, unknown>),
  },
  {
    source: ATTACHMENT_REF_SRC,
    schemas: collectSchemas(ATTACHMENT_REF_SRC, attachmentRef as Record<string, unknown>),
  },
  {
    source: MODEL_SELECTION_SRC,
    schemas: collectSchemas(MODEL_SELECTION_SRC, modelSelection as Record<string, unknown>),
  },
  {
    source: MODEL_EXECUTION_SRC,
    schemas: collectSchemas(MODEL_EXECUTION_SRC, modelExecution as Record<string, unknown>),
  },
  { source: CORE_SRC, schemas: collectSchemas(CORE_SRC, core as Record<string, unknown>) },
];

/**
 * The protocol surface the Go agent must validate: the schema families named in
 * phase-0 step 0.4 and everything reachable from them through composition.
 *
 * Roots are listed explicitly (each seeded schema id below); `resolveSurface()` then
 * expands references so a root that embeds `conversationDeltaSchema` pulls the delta
 * family in automatically.
 */
const ROOTS: Array<{ id: string; source: string; name: string }> = [
  // legacy protocol envelope + method families
  { id: "nexProtocolMessage", source: LEGACY, name: "nexProtocolMessageSchema" },
  { id: "nexProtocolRequestId", source: LEGACY, name: "nexProtocolRequestIdSchema" },
  { id: "nexProtocolTrace", source: LEGACY, name: "nexProtocolTraceSchema" },
  { id: "nexStorageStartupState", source: LEGACY, name: "nexStorageStartupStateSchema" },
  // v4 handshake / transport
  { id: "helloMessage", source: TRANSPORT, name: "helloMessageSchema" },
  { id: "clientHello", source: TRANSPORT, name: "clientHelloSchema" },
  { id: "hostCapabilities", source: TRANSPORT, name: "hostCapabilitiesSchema" },
  { id: "subscribeParams", source: TRANSPORT, name: "subscribeParamsSchema" },
  { id: "subscribeAck", source: TRANSPORT, name: "subscribeAckSchema" },
  {
    id: "conversationSubscribeParams",
    source: TRANSPORT,
    name: "v4ConversationSubscribeParamsSchema",
  },
  {
    id: "conversationSubscribeResult",
    source: TRANSPORT,
    name: "v4ConversationSubscribeResultSchema",
  },
  { id: "connectionFlowParams", source: TRANSPORT, name: "v4ConnectionFlowParamsSchema" },
  { id: "connectionFlowResult", source: TRANSPORT, name: "v4ConnectionFlowResultSchema" },
  { id: "conversationTopicFrame", source: TRANSPORT, name: "conversationTopicFrameSchema" },
  { id: "conversationTopicWireFrame", source: TRANSPORT, name: "conversationTopicWireFrameSchema" },
  // v4 command envelope + payloads + results
  { id: "commandEnvelope", source: COMMAND, name: "commandEnvelopeSchema" },
  { id: "commandResult", source: COMMAND, name: "commandResultSchema" },
  { id: "commandAck", source: COMMAND, name: "commandAckSchema" },
  { id: "commandsQueryParams", source: COMMAND, name: "commandsQueryParamsSchema" },
  { id: "commandsQueryResult", source: COMMAND, name: "commandsQueryResultSchema" },
  // The 34 v4 command payloads (one root per command type). `commandPayloadSchemas`
  // is a record, so `resolveSurface` expands it into `commandPayloadSchemas$$<type>`.
  { id: "__commandPayloadSchemas", source: COMMAND, name: "commandPayloadSchemas$$" },
  // v4 conversation family
  { id: "conversationSnapshot", source: SNAPSHOT, name: "conversationSnapshotSchema" },
  { id: "conversationDelta", source: DELTA, name: "conversationDeltaSchema" },
  { id: "conversationRow", source: ROWS, name: "conversationRowSchema" },
  { id: "toolCallRow", source: ROWS, name: "toolCallRowSchema" },
];

/** Method tables Go needs as constants (step 0.4.3). */
export function methodTables(): {
  nexProtocolMethods: Record<string, string>;
  nexProtocolNotifications: Record<string, string>;
  V4_METHODS: Record<string, string>;
  V4_NOTIFICATIONS: Record<string, string>;
  V4_WIRE_PROTOCOL_VERSION: number;
  PROTOCOL_V4_LIMITS: Record<string, number>;
} {
  return {
    nexProtocolMethods: { ...(legacy.nexProtocolMethods as Record<string, string>) },
    nexProtocolNotifications: {
      ...(legacy.nexProtocolNotifications as Record<string, string>),
    },
    V4_METHODS: { ...(transport.V4_METHODS as Record<string, string>) },
    V4_NOTIFICATIONS: { ...(transport.V4_NOTIFICATIONS as Record<string, string>) },
    V4_WIRE_PROTOCOL_VERSION: core.V4_WIRE_PROTOCOL_VERSION as number,
    PROTOCOL_V4_LIMITS: { ...(core.PROTOCOL_V4_LIMITS as Record<string, number>) },
  };
}

export interface ResolvedSurfaceEntry extends SurfaceEntry {
  /** True when this schema is one of the explicit roots (not pulled in by reference). */
  root: boolean;
}

/**
 * Resolve the surface: start from the roots, then transitively pull in every named
 * `*Schema` constant reachable through composition, so validators never reference an
 * undefined Go symbol.
 */
export function resolveSurface(): ResolvedSurfaceEntry[] {
  const byName = new Map<string, { source: string; schema: AnySchema }>();
  for (const mod of MODULES) {
    for (const [name, schema] of Object.entries(mod.schemas)) {
      byName.set(name, { source: mod.source, schema });
    }
  }

  const seen = new Map<string, ResolvedSurfaceEntry>();
  const queue: Array<{ name: string; root: boolean }> = [];
  for (const r of ROOTS) {
    if (r.name.includes("$$")) {
      // Record root: expand into one entry per key. `$$` with an empty key means
      // "every key of the record".
      const sep = r.name.indexOf("$$");
      const recordName = r.name.slice(0, sep);
      const key = r.name.slice(sep + 2);
      if (key === "") {
        const candidates = [...byName.keys()].filter((c) => c.startsWith(`${recordName}$$`));
        if (candidates.length === 0) throw new Error(`surface root ${recordName} has no members`);
        for (const candidate of candidates) queue.push({ name: candidate, root: true });
        continue;
      }
      const name = `${recordName}$$${key}`;
      if (!byName.has(name)) throw new Error(`surface root ${r.name} not found`);
      queue.push({ name, root: true });
      continue;
    }
    if (!byName.has(r.name)) {
      throw new Error(`surface root ${r.name} is not exported from ${r.source}`);
    }
    queue.push({ name: r.name, root: true });
  }

  while (queue.length > 0) {
    const { name, root } = queue.shift() as { name: string; root: boolean };
    if (seen.has(name)) {
      if (root) seen.get(name)!.root = true;
      continue;
    }
    const entry = byName.get(name);
    if (!entry) {
      throw new Error(`referenced schema ${name} is not an exported module constant`);
    }
    seen.set(name, {
      id: name.replace(/Schema$/, ""),
      source: entry.source,
      schema: entry.schema,
      root,
    });
    for (const ref of referencedNames(entry.schema)) {
      if (!seen.has(ref)) queue.push({ name: ref, root: false });
    }
  }

  return [...seen.values()].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

/**
 * Names of named `*Schema` constants referenced from a schema. Three ways a name can
 * appear: a direct object identity match against a known module export, a `def` node
 * that *is* one of those exports, or a string tag we attached via a helper.
 */
function referencedNames(root: AnySchema): string[] {
  const names = new Set<string>();
  const schemaToStrings = new Map<object, string>();
  for (const mod of MODULES) {
    for (const [name, schema] of Object.entries(mod.schemas)) {
      schemaToStrings.set(schema as object, name);
    }
  }

  const visit = (node: unknown, depth: number): void => {
    if (depth > 200 || typeof node !== "object" || node === null) return;
    if (schemaToStrings.has(node as object)) {
      // A referenced named constant: record it, but keep walking its def below.
      names.add(schemaToStrings.get(node as object) as string);
    }
    if (Array.isArray(node)) {
      for (const item of node) visit(item, depth + 1);
      return;
    }
    const record = node as Record<string, unknown>;
    if ("def" in record && record.def && typeof record.def === "object") {
      // Step into the referenced schema's own definition exactly once.
      visit(record.def, depth + 1);
      return;
    }
    for (const value of Object.values(record)) visit(value, depth + 1);
  };

  // Do not re-add the root's own name through self-visits.
  visit(root.def, 0);
  return [...names];
}
