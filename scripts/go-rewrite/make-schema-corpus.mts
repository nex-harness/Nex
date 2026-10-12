// G0.6: build the schema agreement corpus.
//
// The corpus is the oracle for the Go validators: for every surface schema we produce a
// set of instances, judge each with the *real* zod schema (safeParse), and write the
// verdict. go/contracts/agreement_test.go then requires the Go validator to agree with
// every verdict. Any disagreement is a bug in the Go side (or a generator gap) — never
// something the generator hides.
//
// Instances come from three sources:
//   1. recorded frames (go/testdata/traces/agent/*/in.normalized.json, out.normalized.json)
//      matched to their schema;
//   2. exhaustive structural generation from the node tree (every union branch, every
//      optional present/absent, boundary numbers/strings/lengths);
//   3. mutations of generated-valid instances (drop a required key, add an unknown key,
//      wrong type, empty string, negative number).
//
// Output: go/testdata/schema-corpus/corpus.jsonl (one {schema,instance,valid} per line)
//         plus manifest.json (counts per schema).
import { readFileSync, readdirSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import { z } from "zod";
import { resolveSurface, type ResolvedSurfaceEntry } from "./schema-surface.mjs";
import { newWalkContext, walk, type Node } from "./zod-walk.mjs";

const OUT_DIR = new URL("../../go/testdata/schema-corpus/", import.meta.url);
const TRACES = new URL("../../go/testdata/traces/agent/", import.meta.url);

/** A generated instance plus a label describing how it was produced. */
interface Instance {
  value: unknown;
  why: string;
}

/** Generate candidate instances for a node. Must be finite and small. */
function instancesFor(node: Node, depth: number): Instance[] {
  if (depth > 6) return [{ value: null, why: "depth-cap" }];
  switch (node.kind) {
    case "any":
      return [
        { value: {}, why: "any-object" },
        { value: [], why: "any-array" },
        { value: "s", why: "any-string" },
        { value: 1, why: "any-number" },
        { value: true, why: "any-bool" },
        { value: null, why: "any-null" },
      ];
    case "never":
      return [
        { value: null, why: "never-null" },
        { value: 1, why: "never-number" },
      ];
    case "null":
      return [
        { value: null, why: "null" },
        { value: 0, why: "null-wrong" },
      ];
    case "undefined":
      return [{ value: null, why: "undefined-json" }];
    case "boolean":
      return [
        { value: true, why: "bool-true" },
        { value: false, why: "bool-false" },
        { value: "true", why: "bool-wrong-type" },
      ];
    case "bigint":
    case "nan":
      return [{ value: 0, why: "unrepresentable" }];
    case "string":
      return stringInstances(node);
    case "number":
      return numberInstances(node);
    case "literal":
      return [
        ...node.values.map((v) => ({ value: v, why: "literal" })),
        { value: "__no_literal__", why: "literal-wrong" },
      ];
    case "enum":
      return [
        ...node.values.map((v) => ({ value: v, why: "enum" })),
        { value: "__no_enum__", why: "enum-wrong" },
      ];
    case "array":
      return arrayInstances(node, depth);
    case "tuple":
      return tupleInstances(node, depth);
    case "record":
      return [
        { value: {}, why: "record-empty" },
        { value: { k: first(instancesFor(node.value, depth + 1)) }, why: "record-one" },
        { value: [], why: "record-wrong" },
      ];
    case "object":
      return objectInstances(node, depth);
    case "union":
      return unionInstances(node, depth);
    case "intersection":
      return intersectionInstances(node, depth);
    case "optional":
      return [{ value: null, why: "optional-absent" }, ...instancesFor(node.inner, depth + 1)];
    case "nullable":
      return [{ value: null, why: "nullable-null" }, ...instancesFor(node.inner, depth + 1)];
    case "default":
      return [...instancesFor(node.inner, depth + 1)];
    case "readonly":
    case "nonoptional":
    case "name":
      return instancesFor(
        node.kind === "name" ? node.inner : (node as { inner: Node }).inner,
        depth + 1,
      );
    case "custom":
      return [
        { value: {}, why: "custom-object" },
        { value: "x", why: "custom-string" },
      ];
    case "unsupported":
      return [{ value: {}, why: "unsupported" }];
    default:
      return [{ value: null, why: "unknown" }];
  }
}

function stringInstances(node: Extract<Node, { kind: "string" }>): Instance[] {
  const base = ["", "a", "abc", "é", "😀", "a b", "sk-test", "user@example.com"];
  const out: Instance[] = base.map((value) => ({ value, why: "string" }));
  // Boundary lengths around min/max.
  const around = new Set<number>();
  if (node.minLength !== undefined)
    around
      .add(node.minLength - 1)
      .add(node.minLength)
      .add(node.minLength + 1);
  if (node.maxLength !== undefined)
    around
      .add(node.maxLength - 1)
      .add(node.maxLength)
      .add(node.maxLength + 1);
  for (const n of around) {
    if (n >= 0 && n <= 20) out.push({ value: "x".repeat(n), why: `len-${n}` });
  }
  out.push({ value: 1, why: "string-wrong-type" });
  out.push({ value: null, why: "string-null" });
  return out;
}

function numberInstances(node: Extract<Node, { kind: "number" }>): Instance[] {
  const out: Instance[] = [
    0,
    1,
    -1,
    1.5,
    127,
    128,
    2 ** 31 - 1,
    2 ** 31,
    2 ** 53 - 1,
    2 ** 53,
    -(2 ** 53),
  ].map((value) => ({ value, why: "number" }));
  for (const bound of [node.gte, node.gt, node.lte, node.lt, node.multipleOf]) {
    if (bound !== undefined) {
      out.push({ value: bound - 1, why: "bound-1" });
      out.push({ value: bound, why: "bound" });
      out.push({ value: bound + 1, why: "bound+1" });
    }
  }
  out.push({ value: "1", why: "number-wrong-type" });
  out.push({ value: true, why: "number-bool" });
  return out;
}

function arrayInstances(node: Extract<Node, { kind: "array" }>, depth: number): Instance[] {
  const el = instancesFor(node.element, depth + 1);
  const out: Instance[] = [
    { value: [], why: "array-empty" },
    { value: [first(el).value], why: "array-one" },
    { value: "not-array", why: "array-wrong" },
  ];
  const lens = new Set<number>();
  if (node.minLength !== undefined)
    lens
      .add(node.minLength - 1)
      .add(node.minLength)
      .add(node.minLength + 1);
  if (node.maxLength !== undefined)
    lens
      .add(node.maxLength - 1)
      .add(node.maxLength)
      .add(node.maxLength + 1);
  for (const n of lens) {
    if (n >= 0 && n <= 8)
      out.push({ value: Array.from({ length: n }, () => first(el).value), why: `array-len-${n}` });
  }
  return out;
}

function tupleInstances(node: Extract<Node, { kind: "tuple" }>, depth: number): Instance[] {
  const full = node.items.map((item) => first(instancesFor(item, depth + 1)).value);
  const out: Instance[] = [
    { value: full, why: "tuple-full" },
    { value: full.slice(0, Math.max(0, full.length - 1)), why: "tuple-short" },
  ];
  if (node.rest)
    out.push({
      value: [...full, first(instancesFor(node.rest, depth + 1)).value],
      why: "tuple-rest",
    });
  else out.push({ value: [...full, "__extra__"], why: "tuple-extra" });
  return out;
}

function objectInstances(node: Extract<Node, { kind: "object" }>, depth: number): Instance[] {
  const out: Instance[] = [];
  // All-present object built from each property's first (usually minimal) candidate.
  const all: Record<string, unknown> = {};
  const perProp = new Map<string, Instance[]>();
  for (const prop of node.properties) {
    const list = instancesFor(prop.node, depth + 1);
    perProp.set(prop.key, list);
    all[prop.key] = first(list).value;
  }
  out.push({ value: all, why: "object-all" });
  out.push({ value: {}, why: "object-empty" });

  // One-property-at-a-time variation: swap exactly one property to each alternate
  // candidate value while keeping the rest at their valid base. This is what makes the
  // corpus actually exercise nested bounds, fractional numbers, wrong types and enums.
  for (const prop of node.properties) {
    const alternates = perProp.get(prop.key) ?? [];
    for (let i = 1; i < alternates.length; i++) {
      const variant = { ...all, [prop.key]: alternates[i].value };
      out.push({ value: variant, why: `object-vary-${prop.key}:${alternates[i].why}` });
    }
  }

  // Drop each required property, one at a time.
  for (const prop of node.properties) {
    if (prop.optional) continue;
    const copy = { ...all };
    delete copy[prop.key];
    out.push({ value: copy, why: `object-missing-${prop.key}` });
  }
  // Unknown property (strict rejects, loose accepts).
  out.push({ value: { ...all, __unknown_key__: 1 }, why: "object-unknown" });
  // Only-optional object.
  if (node.properties.some((p) => p.optional)) {
    const onlyRequired: Record<string, unknown> = {};
    for (const prop of node.properties) if (!prop.optional) onlyRequired[prop.key] = all[prop.key];
    out.push({ value: onlyRequired, why: "object-required-only" });
  }
  out.push({ value: "not-object", why: "object-wrong" });
  return out;
}

function unionInstances(node: Extract<Node, { kind: "union" }>, depth: number): Instance[] {
  const out: Instance[] = [];
  node.options.forEach((option, index) => {
    out.push({ value: first(instancesFor(option, depth + 1)).value, why: `union-${index}` });
  });
  out.push({ value: "__union_none__", why: "union-none" });
  return out;
}

function intersectionInstances(
  node: Extract<Node, { kind: "intersection" }>,
  depth: number,
): Instance[] {
  const left = first(instancesFor(node.left, depth + 1)).value;
  const right = first(instancesFor(node.right, depth + 1)).value;
  const merged = isObject(left) && isObject(right) ? { ...left, ...right } : left;
  return [
    { value: merged, why: "intersection" },
    { value: left, why: "intersection-left-only" },
  ];
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function first(list: Instance[]): Instance {
  return list[0] ?? { value: null, why: "empty" };
}

/** Structural mutations of a valid instance: drop keys, add keys, change types. */
function mutate(value: unknown, why: string, depth = 0): Instance[] {
  if (depth > 4) return [];
  const out: Instance[] = [];
  if (isObject(value)) {
    for (const key of Object.keys(value)) {
      out.push({ value: withoutKey(value, key), why: `${why}:drop:${key}` });
    }
    out.push({ value: { ...value, __junk__: "x", __junk2__: 2 }, why: `${why}:add-unknown` });
    const firstKey = Object.keys(value)[0];
    if (firstKey)
      out.push({ value: { ...value, [firstKey]: "__wrong__" }, why: `${why}:retype:${firstKey}` });
  } else if (Array.isArray(value)) {
    out.push({ value: [], why: `${why}:empty-array` });
    out.push({ value: [...value, "__extra__"], why: `${why}:extra-item` });
  } else if (typeof value === "string") {
    out.push({ value: "", why: `${why}:empty-string` });
    out.push({ value: "__zzz__", why: `${why}:other-string` });
  } else if (typeof value === "number") {
    out.push({ value: 0, why: `${why}:zero` });
    out.push({ value: -1, why: `${why}:negative` });
  }
  return out;
}

function withoutKey(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const copy = { ...obj };
  delete copy[key];
  return copy;
}

/** Judge an instance with the real zod schema. */
function judge(schema: z.ZodTypeAny, value: unknown): boolean {
  return schema.safeParse(value).success;
}

/** Recorded frames matched to a surface schema, by method. */
function framesBySchema(): Map<string, unknown[]> {
  const map = new Map<string, unknown[]>();
  const add = (schema: string, value: unknown): void => {
    const list = map.get(schema) ?? [];
    list.push(value);
    map.set(schema, list);
  };
  if (!existsSync(TRACES)) return map;
  for (const dir of readdirSync(TRACES)) {
    for (const file of ["in.normalized.json", "out.normalized.json"]) {
      const path = new URL(`${dir}/${file}`, TRACES);
      if (!existsSync(path)) continue;
      const frames = JSON.parse(readFileSync(path, "utf8")) as Array<Record<string, unknown>>;
      for (const frame of frames) {
        const method = frame.method as string | undefined;
        if (!method) continue;
        if (file.startsWith("in") && frame.id !== undefined && method === "v4/command") {
          const params = frame.params as Record<string, unknown> | undefined;
          const type = params?.type as string | undefined;
          if (type) add(`commandPayloadSchemas$$${type}`, params?.payload);
          add("commandEnvelope", { ...frame, params });
        } else if (method === "v4/conversation/subscribe") {
          add("v4ConversationSubscribeParams", frame.params);
        } else if (method === "v4/conversation/frame") {
          add("conversationTopicWireFrame", frame.params);
        } else if (method === "startup/storageState") {
          add("nexStorageStartupState", frame.params);
        }
      }
    }
  }
  return map;
}

function main(): void {
  const surface = resolveSurface() as ResolvedSurfaceEntry[];
  const frameCorpus = framesBySchema();
  mkdirSync(OUT_DIR, { recursive: true });

  const lines: Array<string> = [];
  const manifest: Array<{ id: string; valid: number; invalid: number; frames: number }> = [];
  let totalValid = 0;
  let totalInvalid = 0;

  for (const entry of surface) {
    const ctx = newWalkContext(new Map());
    const node = walk(entry.schema, ctx);
    const candidates: Instance[] = [];
    candidates.push(...instancesFor(node, 0));
    // Mutations of each already-generated valid instance.
    for (const base of candidates.slice(0, 6)) {
      if (judge(entry.schema, base.value)) candidates.push(...mutate(base.value, base.why));
    }

    const seen = new Set<string>();
    let valid = 0;
    let invalid = 0;
    for (const candidate of candidates) {
      const key = JSON.stringify(candidate.value);
      if (seen.has(key)) continue;
      seen.add(key);
      const ok = judge(entry.schema, candidate.value);
      if (ok) valid++;
      else invalid++;
      lines.push(
        JSON.stringify({
          schema: entry.id,
          instance: candidate.value,
          valid: ok,
          why: candidate.why,
        }),
      );
    }

    // Recorded frames: judge and add.
    let frames = 0;
    for (const frame of frameCorpus.get(entry.id) ?? []) {
      const key = JSON.stringify(frame);
      if (seen.has(key)) continue;
      seen.add(key);
      const ok = judge(entry.schema, frame);
      if (ok) valid++;
      else invalid++;
      frames++;
      lines.push(
        JSON.stringify({ schema: entry.id, instance: frame, valid: ok, why: "recorded-frame" }),
      );
    }

    totalValid += valid;
    totalInvalid += invalid;
    manifest.push({ id: entry.id, valid, invalid, frames });
  }

  lines.sort();
  writeFileSync(new URL("corpus.jsonl", OUT_DIR), `${lines.join("\n")}\n`);
  writeFileSync(
    new URL("manifest.json", OUT_DIR),
    `${JSON.stringify(
      {
        generatedFrom: "scripts/go-rewrite/make-schema-corpus.mts",
        schemas: surface.length,
        instances: lines.length,
        valid: totalValid,
        invalid: totalInvalid,
        perSchema: manifest,
      },
      null,
      2,
    )}\n`,
  );

  console.log(
    `corpus: ${lines.length} instances across ${surface.length} schemas (valid=${totalValid}, invalid=${totalInvalid})`,
  );
}

main();
