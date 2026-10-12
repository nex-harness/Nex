// G0.5: generate Go contracts from the protocol surface.
//
// This emitter is a *data* generator: it serializes the zod-derived node tree
// (scripts/go-rewrite/zod-walk.mts) into Go struct literals consumed by the hand-written
// interpreter in go/contracts/validate.go. Data-driven output is deterministic, easy to
// diff in CI, and avoids generating hundreds of tiny recursive Go functions with import
// cycles. Refinements become `custom` nodes; the G0.6 corpus proves where that matters.
//
// Output:
//   go/contracts/surface.gen.go   the node trees, one var per schema id + registry maps
//   go/contracts/methods.gen.go   protocol method/notification tables + limits
//   go/contracts/manifest.gen.go  generation metadata (counts, unsupported types)
//
// Run via scripts/go-rewrite/gen-contracts.sh; scripts/go-rewrite/check-contracts-drift.sh
// re-runs it and diffs.
import { mkdirSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { methodTables, resolveSurface, type ResolvedSurfaceEntry } from "./schema-surface.mjs";
import { hasCustomCheck, newWalkContext, walk, type Node } from "./zod-walk.mjs";

const OUT = new URL("../../go/contracts/", import.meta.url);

/** JS number literal that survives a Go float64 round-trip. */
function goFloat(n: number): string {
  return `${n}`;
}

function goString(s: string): string {
  return JSON.stringify(s);
}

/** Convert an id (which may contain `$$`, `.`, `-`) into a Go identifier fragment. */
function goIdent(id: string): string {
  return id.replace(/\$\$/g, "_").replace(/[^A-Za-z0-9_]/g, "_");
}

/**
 * Emits one Go package-level `var` per distinct node and returns the identifier for a
 * node's variable, so identical sub-trees are shared and the output stays compact.
 */
class Emitter {
  private readonly lines: string[] = [];
  private readonly emitted = new Set<string>();
  private readonly queue: Array<{ node: Node; varName: string }> = [];
  private readonly byIdentity = new Map<Node, string>();
  private counter = 0;

  varFor(node: Node, hint: string): string {
    // Deduplicate by node object identity so shared sub-trees are emitted once.
    const existing = this.byIdentity.get(node);
    if (existing) return existing;
    const name = `n_${goIdent(hint)}_${this.counter++}`;
    this.byIdentity.set(node, name);
    this.queue.push({ node, varName: name });
    return name;
  }

  /** Last variable allocated (the root of a just-walked schema). */
  lastVar(): string {
    return this.queue[this.queue.length - 1].varName;
  }

  /** Drain the queue, emitting each variable exactly once. */
  drain(): string {
    while (this.queue.length > 0) {
      const { node, varName } = this.queue.shift() as { node: Node; varName: string };
      if (this.emitted.has(varName)) continue;
      this.emitted.add(varName);
      this.lines.push(`var ${varName} = ${this.render(node)}`);
      this.lines.push("");
    }
    return this.lines.join("\n");
  }

  private render(node: Node): string {
    const refine = (node as { refine?: string }).refine;
    const body = this.renderKind(node);
    if (!refine) return body;
    // Append the refine id to the struct literal. Do not use String.replace here: its
    // replacement string treats `$$` as an escape, which would corrupt ids like
    // `commandPayloadSchemas$$sendText`.
    return `${body.slice(0, -1)}, Refine: ${goString(refine)}}`;
  }

  private renderKind(node: Node): string {
    switch (node.kind) {
      case "any":
      case "never":
      case "null":
      case "undefined":
      case "boolean":
      case "bigint":
      case "nan":
        return `&Node{Kind: Kind${cap(node.kind)}}`;
      case "custom":
        return `&Node{Kind: KindCustom}`;
      case "unsupported":
        return `&Node{Kind: KindUnsupported, Unsupported: ${goString(node.type)}}`;
      case "string":
        return `&Node{Kind: KindString${this.stringFields(node)}}`;
      case "number":
        return `&Node{Kind: KindNumber${this.numberFields(node)}}`;
      case "literal":
        return `&Node{Kind: KindLiteral, Literals: ${this.literals(node.values)}}`;
      case "enum":
        return `&Node{Kind: KindEnum, Literals: ${this.literals(node.values)}}`;
      case "array":
        return `&Node{Kind: KindArray, Element: ${this.varFor(node.element, "el")}${this.minMax(node)}}`;
      case "tuple":
        return `&Node{Kind: KindTuple, Items: []*Node{${node.items
          .map((item) => this.varFor(item, "t"))
          .join(", ")}}${node.rest ? `, Rest: ${this.varFor(node.rest, "rest")}` : ""}}`;
      case "record":
        return `&Node{Kind: KindRecord, Value: ${this.varFor(node.value, "val")}}`;
      case "object":
        return this.renderObject(node);
      case "union":
        return `&Node{Kind: KindUnion, Exclusive: ${node.exclusive}${
          node.discriminator ? `, Discriminator: ${goString(node.discriminator)}` : ""
        }, Options: []*Node{${node.options.map((o) => this.varFor(o, "opt")).join(", ")}}}`;
      case "intersection":
        return `&Node{Kind: KindIntersection, Left: ${this.varFor(node.left, "left")}, Right: ${this.varFor(node.right, "right")}}`;
      case "optional":
      case "nullable":
      case "default":
      case "readonly":
      case "nonoptional":
        return `&Node{Kind: Kind${cap(node.kind)}, Inner: ${this.varFor(node.inner, "in")}}`;
      case "name":
        return `&Node{Kind: KindName, Name: ${goString(node.name)}, Inner: ${this.varFor(node.inner, "in")}}`;
      default:
        return `&Node{Kind: KindAny}`;
    }
  }

  private stringFields(node: Extract<Node, { kind: "string" }>): string {
    let out = "";
    if (node.minLength !== undefined) out += `, MinLength: iptr(${node.minLength})`;
    if (node.maxLength !== undefined) out += `, MaxLength: iptr(${node.maxLength})`;
    if (node.pattern)
      out += `, Pattern: ${goString(node.pattern.source)}, Flags: ${goString(node.pattern.flags)}`;
    if (node.formats.length > 0)
      out += `, Formats: []string{${node.formats.map(goString).join(", ")}}`;
    return out;
  }

  private numberFields(node: Extract<Node, { kind: "number" }>): string {
    let out = "";
    if (node.int) out += `, Int: true`;
    if (node.safeInt) out += `, SafeInt: true`;
    if (node.gte !== undefined) out += `, GTE: fptr(${goFloat(node.gte)})`;
    if (node.gt !== undefined) out += `, GT: fptr(${goFloat(node.gt)})`;
    if (node.lte !== undefined) out += `, LTE: fptr(${goFloat(node.lte)})`;
    if (node.lt !== undefined) out += `, LT: fptr(${goFloat(node.lt)})`;
    if (node.multipleOf !== undefined) out += `, MultipleOf: fptr(${goFloat(node.multipleOf)})`;
    return out;
  }

  private minMax(node: Extract<Node, { kind: "array" }>): string {
    let out = "";
    if (node.minLength !== undefined) out += `, MinItems: iptr(${node.minLength})`;
    if (node.maxLength !== undefined) out += `, MaxItems: iptr(${node.maxLength})`;
    return out;
  }

  private literals(values: Array<string | number | boolean | null>): string {
    return `[]Literal{${values.map((v) => literalGo(v)).join(", ")}}`;
  }

  private renderObject(node: Extract<Node, { kind: "object" }>): string {
    const props = node.properties
      .map(
        (p) =>
          `{Key: ${goString(p.key)}, Node: ${this.varFor(p.node, "p")}, Optional: ${p.optional}}`,
      )
      .join(", ");
    let out = `&Node{Kind: KindObject, Strict: ${node.strict}, Properties: []Prop{${props}}`;
    if (node.catchall) out += `, Catchall: ${this.varFor(node.catchall, "catch")}`;
    out += "}";
    return out;
  }
}

function literalGo(v: string | number | boolean | null): string {
  if (typeof v === "string") return `{Kind: "string", Str: ${goString(v)}}`;
  if (typeof v === "number") return `{Kind: "number", Num: ${goFloat(v)}}`;
  if (typeof v === "boolean") return `{Kind: "boolean", Bool: ${v}}`;
  return `{Kind: "null"}`;
}

const CAP: Record<string, string> = {
  any: "Any",
  never: "Never",
  null: "Null",
  undefined: "Undefined",
  boolean: "Boolean",
  bigint: "Bigint",
  nan: "NaN",
  string: "String",
  number: "Number",
  optional: "Optional",
  nullable: "Nullable",
  default: "Default",
  readonly: "Readonly",
  nonoptional: "Nonoptional",
};

function cap(kind: string): string {
  return CAP[kind] ?? kind;
}

const HEADER = `// Code generated by scripts/go-rewrite/gen-contracts.mts. DO NOT EDIT.
// Regenerate with scripts/go-rewrite/gen-contracts.sh; CI checks drift.

package contracts
`;

/**
 * Return a copy of the root node with its own `refine` id replaced by `id`. Nested nodes
 * keep their own refine ids (they are distinct schemas with their own rules).
 */
function canonicalizeRefine(node: Node, id: string): Node {
  if (!(node as { refine?: string }).refine) return node;
  return { ...node, refine: id } as Node;
}

function main(): void {
  const surface = resolveSurface() as ResolvedSurfaceEntry[];
  const names = new Map<object, string>();
  for (const entry of surface) names.set(entry.schema as object, entry.id);

  const emitter = new Emitter();
  const unsupportedTypes = new Set<string>();
  const roots: Array<{ id: string; varName: string }> = [];
  const refined: string[] = [];

  for (const entry of surface) {
    const ctx = newWalkContext(names);
    const node = walk(entry.schema, ctx);
    for (const u of ctx.unsupported) unsupportedTypes.add(u);
    // Canonicalize every refine id on this root to the surface id, so record members
    // (whose id contains `$$`) always match refinements.go.
    const canonical = canonicalizeRefine(node, entry.id);
    emitter.varFor(canonical, `root_${goIdent(entry.id)}`);
    roots.push({ id: entry.id, varName: emitter.lastVar() });
    if (hasCustomCheck(entry.schema)) refined.push(entry.id);
  }

  const surfaceGo = [
    HEADER,
    emitter.drain(),
    "// Registry maps schema ids and exported names to their node trees.",
    "var Registry = &Surface{",
    "\tByID: map[string]*Node{",
    ...roots.map((r) => `\t\t${goString(r.id)}: ${r.varName},`),
    "\t},",
    "\tByName: map[string]*Node{",
    ...roots.map((r) => `\t\t${goString(exportedName(r.id))}: ${r.varName},`),
    "\t},",
    "}",
    "",
    "func iptr(v int64) *int64     { return &v }",
    "func fptr(v float64) *float64 { return &v }",
    "",
  ].join("\n");

  const tables = methodTables();
  const methodsGo = emitMethods(tables);

  const manifestGo = [
    HEADER,
    `// SchemaCount is the number of schemas in the generated surface.`,
    `const SchemaCount = ${surface.length}`,
    "",
    `// UnsupportedNodeTypes lists zod node kinds the emitter could not describe`,
    `// structurally. Empty is the goal; a non-empty list means a corpus gap.`,
    `var UnsupportedNodeTypes = []string{${[...unsupportedTypes].sort().map(goString).join(", ")}}`,
    "",
    `// RefinedSchemas lists schemas whose zod .refine/.superRefine rules are`,
    `// re-implemented by hand in refinements.go. Every id here must have a matching`,
    `// entry there; a test asserts the two lists agree.`,
    `var RefinedSchemas = []string{${[...refined].sort().map(goString).join(", ")}}`,
    "",
  ].join("\n");

  mkdirSync(OUT, { recursive: true });
  const files = ["surface.gen.go", "methods.gen.go", "manifest.gen.go"];
  writeFileSync(new URL("surface.gen.go", OUT), surfaceGo);
  writeFileSync(new URL("methods.gen.go", OUT), methodsGo);
  writeFileSync(new URL("manifest.gen.go", OUT), manifestGo);

  // Format the emitted files so `gofmt -l .` is clean on the committed tree. Reimplementing
  // gofmt's tabwriter alignment would be fragile; running gofmt is exact by construction.
  try {
    execFileSync("gofmt", ["-w", ...files.map((name) => fileURLToPath(new URL(name, OUT)))], {
      stdio: "inherit",
    });
  } catch (error) {
    console.warn("gofmt not available; run scripts/go-rewrite/gen-contracts.sh to format", error);
  }

  console.log(
    `generated ${surface.length} schemas; unsupported node types: ${unsupportedTypes.size} ${[...unsupportedTypes].join(",")}`,
  );
}

/** The exported constant name for an id (used for the ByName map). */
function exportedName(id: string): string {
  if (id.includes("$$")) {
    const [record, key] = id.split("$$");
    return `${record}.${key}`;
  }
  return `${id}Schema`;
}

function emitMethods(tables: ReturnType<typeof methodTables>): string {
  const map = (name: string, obj: Record<string, string>): string[] => {
    const keys = Object.keys(obj).sort();
    return [
      `var ${name} = map[string]string{`,
      ...keys.map((k) => `\t${goString(k)}: ${goString(obj[k])},`),
      "}",
      "",
    ];
  };
  const limits = tables.PROTOCOL_V4_LIMITS ?? {};
  return [
    HEADER,
    `// V4WireProtocolVersion is the v4 conversation wire version the Go agent speaks.`,
    `const V4WireProtocolVersion = ${tables.V4_WIRE_PROTOCOL_VERSION}`,
    "",
    ...map("NexProtocolMethods", tables.nexProtocolMethods),
    ...map("NexProtocolNotifications", tables.nexProtocolNotifications),
    ...map("V4Methods", tables.V4_METHODS),
    ...map("V4Notifications", tables.V4_NOTIFICATIONS),
    `// ProtocolV4Limits mirrors PROTOCOL_V4_LIMITS from nex-protocol-v4/core.ts.`,
    `var ProtocolV4Limits = map[string]int64{`,
    ...Object.keys(limits)
      .sort()
      .map((k) => `\t${goString(k)}: ${limits[k]},`),
    "}",
    "",
  ].join("\n");
}

main();
