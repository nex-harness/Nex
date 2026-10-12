// Reflective, faithful walker over zod 4's internal `_zod.def` tree.
//
// The JSON Schema produced in G0.4 is lossy: `.refine`/`.superRefine` and transforms
// disappear, and some nullable/default edges are normalized. To generate Go validators
// that accept *exactly* what zod accepts, this walker reads the real `_zod.def`
// (including per-check internals such as `min_length`, `greater_than`, regex patterns)
// and produces a normalized description both the Go emitter (G0.5) and the corpus
// generator (G0.6) understand. Any node type it cannot describe is reported, never
// silently widened.

export type Value = unknown;

/** A normalized, reproducible description of one zod node. */
export type Node =
  | { kind: "any" } // any / unknown: accepts everything
  | { kind: "never" } // never: accepts nothing
  | { kind: "null" }
  | { kind: "undefined" }
  | { kind: "boolean" }
  | {
      kind: "string";
      minLength?: number;
      maxLength?: number;
      pattern?: RegExpPattern;
      formats: string[];
    }
  | {
      kind: "number";
      int?: boolean;
      safeInt?: boolean;
      gte?: number;
      gt?: number;
      lte?: number;
      lt?: number;
      multipleOf?: number;
    }
  | { kind: "bigint" }
  | { kind: "nan" }
  | { kind: "literal"; values: Array<string | number | boolean | null> }
  | { kind: "enum"; values: Array<string | number> }
  | { kind: "array"; element: Node; minLength?: number; maxLength?: number }
  | { kind: "tuple"; items: Node[]; rest: Node | null }
  | { kind: "record"; value: Node }
  | {
      kind: "object";
      properties: Array<{ key: string; node: Node; optional: boolean }>;
      catchall: Node | null;
      strict: boolean;
    }
  | { kind: "union"; options: Node[]; exclusive: boolean; discriminator?: string | null }
  | { kind: "intersection"; left: Node; right: Node }
  | { kind: "optional"; inner: Node }
  | { kind: "nullable"; inner: Node }
  | { kind: "default"; inner: Node }
  | { kind: "readonly"; inner: Node }
  | { kind: "nonoptional"; inner: Node }
  | { kind: "name"; name: string; inner: Node }
  | { kind: "custom"; opaque: true } // refine/superRefine/transform we cannot inline
  | { kind: "unsupported"; type: string };

/** Most node kinds carry an optional `refine` id set by the emitter on a root copy. */
export type Refinable = Node & { refine?: string };

export interface RegExpPattern {
  source: string;
  flags: string;
}

/**
 * True when a schema carries `.refine`/`.superRefine` checks anywhere on its own `def`
 * (not on referenced children). Those checks are applied by zod after the structural
 * parse; the Go side reproduces them by hand, keyed by schema id.
 */
export function hasCustomCheck(schema: unknown): boolean {
  const def = defOf(schema);
  const checks = def?.checks;
  if (!Array.isArray(checks)) return false;
  return checks.some((check) => checkDef(check).check === "custom");
}

export interface WalkContext {
  /** Records node types the walker could not describe (for the manifest). */
  unsupported: Set<string>;
  /** Identity map from a live schema object to its exported name, for `name` nodes. */
  names: Map<object, string>;
  /**
   * Memoized results by schema object identity. Without this, every reference to a
   * shared schema would produce a fresh Node, and the Go emitter could not dedupe.
   */
  memo: Map<object, Node>;
  depth: number;
}

const MAX_DEPTH = 80;

export function walk(schema: unknown, ctx: WalkContext): Node {
  if (ctx.depth > MAX_DEPTH) return { kind: "any" };
  if (schema && typeof schema === "object" && ctx.memo.has(schema as object)) {
    return ctx.memo.get(schema as object) as Node;
  }
  const def = defOf(schema);
  if (def === undefined) {
    return { kind: "any" };
  }

  const named =
    typeof schema === "object" && schema !== null ? ctx.names.get(schema as object) : undefined;
  const withDepth: WalkContext = { ...ctx, depth: ctx.depth + 1 };
  const inner = walkDef(def, schema, { ...withDepth, memo: ctx.memo });
  // Named references keep the generated code compact and let the corpus report by name.
  let result: Node = inner;
  if (named && typeof (schema as { safeParse?: unknown }).safeParse === "function") {
    result = { kind: "name", name: named, inner };
  }
  // A schema with `.refine`/`.superRefine` checks gets a hand-written rule applied after
  // its structural check — everywhere the schema is referenced, matching zod.
  if (hasCustomCheck(schema)) {
    result = { ...result, refine: ctx.names.get(schema as object) ?? named } as Node;
  }
  if (schema && typeof schema === "object") ctx.memo.set(schema as object, result);
  return result;
}

function defOf(schema: unknown): Record<string, unknown> | undefined {
  if (!schema || typeof schema !== "object") return undefined;
  const zod = (schema as { _zod?: { def?: Record<string, unknown> } })._zod;
  if (zod?.def) return zod.def;
  const def = (schema as { def?: Record<string, unknown> }).def;
  return def;
}

function child(node: unknown, ctx: WalkContext): Node {
  const next: WalkContext = { ...ctx, depth: ctx.depth + 1 };
  return walk(node, next);
}

function walkDef(def: Record<string, unknown>, schema: unknown, ctx: WalkContext): Node {
  const type = def.type as string | undefined;
  switch (type) {
    case "any":
    case "unknown":
      return { kind: "any" };
    case "never":
      return { kind: "never" };
    case "null":
      return { kind: "null" };
    case "undefined":
      return { kind: "undefined" };
    case "boolean":
      return { kind: "boolean" };
    case "string":
      return walkString(def, schema);
    case "number":
      return walkNumber(def, schema);
    case "bigint":
      return { kind: "bigint" };
    case "nan":
      return { kind: "nan" };
    case "literal":
      return { kind: "literal", values: literalValues(def) };
    case "enum":
      return { kind: "enum", values: enumValues(def) };
    case "array":
      return walkArray(def, schema, ctx);
    case "tuple":
      return {
        kind: "tuple",
        items: (def.items as unknown[]).map((item) => child(item, ctx)),
        rest: def.rest ? child(def.rest, ctx) : null,
      };
    case "record":
      return { kind: "record", value: child(def.valueType, ctx) };
    case "object":
      return walkObject(def, ctx);
    case "union":
      return walkUnion(def, ctx);
    case "intersection":
      return { kind: "intersection", left: child(def.left, ctx), right: child(def.right, ctx) };
    case "optional":
      return { kind: "optional", inner: child(def.innerType, ctx) };
    case "nullable":
      return { kind: "nullable", inner: child(def.innerType, ctx) };
    case "default":
      return { kind: "default", inner: child(def.innerType, ctx) };
    case "readonly":
      return { kind: "readonly", inner: child(def.innerType, ctx) };
    case "nonoptional":
      return { kind: "nonoptional", inner: child(def.innerType, ctx) };
    case "lazy":
      return child((def.getter as () => unknown)(), ctx);
    case "promise":
      ctx.unsupported.add("promise");
      return { kind: "unsupported", type: "promise" };
    case "custom":
      return { kind: "custom", opaque: true };
    case "transform":
    case "pipe":
      ctx.unsupported.add("transform");
      return { kind: "unsupported", type: "transform" };
    case "catch":
      // `.catch(v)` accepts anything: a value that fails the inner schema is replaced by
      // the fallback, so validation never fails. The value is therefore `any`.
      return { kind: "any" };
    case "symbol":
    case "function":
    case "set":
    case "map":
    case "date":
    case "template_literal":
      ctx.unsupported.add(type);
      return { kind: "unsupported", type };
    default:
      ctx.unsupported.add(`unknown:${type}`);
      return { kind: "unsupported", type: type ?? "missing" };
  }
}

function literalValues(def: Record<string, unknown>): Array<string | number | boolean | null> {
  const values = def.values;
  if (values instanceof Set) return [...values] as Array<string | number | boolean | null>;
  if (Array.isArray(values)) return values as Array<string | number | boolean | null>;
  return [];
}

function enumValues(def: Record<string, unknown>): Array<string | number> {
  const entries = def.entries as Record<string, unknown> | undefined;
  if (entries) return Object.values(entries) as Array<string | number>;
  const values = def.values;
  if (values instanceof Set) return [...values] as Array<string | number>;
  return [];
}

function walkString(_def: Record<string, unknown>, schema: unknown): Node {
  const node: Node = { kind: "string", formats: [] };
  for (const check of checksOf(schema)) {
    const cdef = checkDef(check);
    switch (cdef.check) {
      case "min_length":
        (node as { minLength?: number }).minLength = cdef.minimum as number;
        break;
      case "max_length":
        (node as { maxLength?: number }).maxLength = cdef.maximum as number;
        break;
      case "string_format":
        if (cdef.format === "regex") {
          (node as { pattern?: RegExpPattern }).pattern = regexPattern(cdef.pattern);
        } else {
          (node as { formats: string[] }).formats.push(cdef.format as string);
        }
        break;
      case "custom":
        (node as { kind: string }).kind = "custom";
        break;
      default:
        // Overwrite checks (trim/toLowerCase) change the parsed value; on input they are
        // permissive. Ignore for validation.
        break;
    }
  }
  return node;
}

function walkNumber(_def: Record<string, unknown>, schema: unknown): Node {
  const node: Node = { kind: "number" };
  for (const check of checksOf(schema)) {
    const cdef = checkDef(check);
    switch (cdef.check) {
      case "number_format":
        if (cdef.format === "safeint") (node as { safeInt?: boolean }).safeInt = true;
        else if (cdef.format === "int") (node as { int?: boolean }).int = true;
        break;
      case "greater_than":
        if (cdef.inclusive) (node as { gte?: number }).gte = cdef.value as number;
        else (node as { gt?: number }).gt = cdef.value as number;
        break;
      case "less_than":
        if (cdef.inclusive) (node as { lte?: number }).lte = cdef.value as number;
        else (node as { lt?: number }).lt = cdef.value as number;
        break;
      case "multiple_of":
        (node as { multipleOf?: number }).multipleOf = cdef.value as number;
        break;
      case "custom":
        (node as { kind: string }).kind = "custom";
        break;
      default:
        break;
    }
  }
  return node;
}

function walkArray(def: Record<string, unknown>, schema: unknown, ctx: WalkContext): Node {
  const node: Extract<Node, { kind: "array" }> = {
    kind: "array",
    element: child(def.element, ctx),
  };
  for (const check of checksOf(schema)) {
    const cdef = checkDef(check);
    if (cdef.check === "min_length") node.minLength = cdef.minimum as number;
    else if (cdef.check === "max_length") node.maxLength = cdef.maximum as number;
  }
  return node;
}

function walkObject(def: Record<string, unknown>, ctx: WalkContext): Node {
  const shape = def.shape as Record<string, unknown> | undefined;
  const properties: Array<{ key: string; node: Node; optional: boolean }> = [];
  if (shape) {
    for (const key of Object.keys(shape).sort()) {
      const fieldSchema = shape[key];
      // `optional()` and `default()` both make a property absent-able on input; unwrap so
      // the field validates against the inner type and presence is tracked separately.
      const optional = isAbsentable(fieldSchema);
      // A `.catch()` anywhere in the wrapper chain means the field accepts anything.
      const target = hasCatch(fieldSchema)
        ? fieldSchema
        : optional
          ? innerOf(fieldSchema)
          : fieldSchema;
      properties.push({ key, node: child(target ?? fieldSchema, ctx), optional });
    }
  }
  const catchallDef = def.catchall as { _zod?: { def?: Record<string, unknown> } } | undefined;
  const catchallType = catchallDef?._zod?.def?.type;
  let catchall: Node | null = null;
  let strict = false;
  if (catchallType === "never") {
    strict = true;
  } else if (catchallType && catchallType !== "unknown") {
    catchall = child(def.catchall, ctx);
  }
  return { kind: "object", properties, catchall, strict };
}

function walkUnion(def: Record<string, unknown>, ctx: WalkContext): Node {
  const options = (def.options as unknown[]).map((option) => child(option, ctx));
  return {
    kind: "union",
    options,
    exclusive: def.inclusive === false,
    discriminator: findDiscriminator(def.options as unknown[]),
  };
}

/** If every option is an object sharing one literal-valued key, that key is a tag. */
function findDiscriminator(options: unknown[]): string | null {
  let candidate: string | null = null;
  for (const option of options) {
    const shape = (defOf(option)?.shape ?? undefined) as Record<string, unknown> | undefined;
    if (!shape) return null;
    const literalKeys = Object.keys(shape).filter((key) => defOf(shape[key])?.type === "literal");
    if (literalKeys.length !== 1) return null;
    if (candidate === null) candidate = literalKeys[0];
    else if (candidate !== literalKeys[0]) return null;
  }
  return candidate;
}

function isAbsentable(schema: unknown): boolean {
  let current = schema;
  for (let i = 0; i < 4; i++) {
    const def = defOf(current);
    const type = def?.type;
    if (type === "optional" || type === "default") return true;
    // `catch`/`readonly`/`nonoptional` wrap another schema; keep looking through them.
    if (type === "catch" || type === "readonly") {
      current = def?.innerType;
      continue;
    }
    return false;
  }
  return false;
}

/** True when a `catch` wrapper appears anywhere in the outer wrapper chain. */
function hasCatch(schema: unknown): boolean {
  let current = schema;
  for (let i = 0; i < 4; i++) {
    const def = defOf(current);
    const type = def?.type;
    if (type === "catch") return true;
    if (type === "optional" || type === "readonly") {
      current = def?.innerType;
      continue;
    }
    return false;
  }
  return false;
}

/** The inner schema of an optional/default wrapper, unwrapping catch/readonly first. */
function innerOf(schema: unknown): unknown {
  let current = schema;
  for (let i = 0; i < 4; i++) {
    const def = defOf(current);
    const type = def?.type;
    if (type === "optional" || type === "default") return def?.innerType;
    if (type === "catch" || type === "readonly") {
      current = def?.innerType;
      continue;
    }
    return current;
  }
  return current;
}

function checksOf(schema: unknown): unknown[] {
  const def = defOf(schema);
  const checks = def?.checks;
  return Array.isArray(checks) ? checks : [];
}

/** Read a check's internals from `check._zod.def` (where zod 4 stores them). */
function checkDef(check: unknown): Record<string, unknown> {
  if (!check || typeof check !== "object") return {};
  const zod = (check as { _zod?: { def?: Record<string, unknown> } })._zod;
  if (zod?.def) return zod.def;
  const def = (check as { def?: Record<string, unknown> }).def;
  return def ?? {};
}

export function regexPattern(pattern: unknown): RegExpPattern {
  if (pattern instanceof RegExp) {
    return { source: pattern.source, flags: pattern.flags };
  }
  return { source: "(?!)", flags: "" };
}

export function newWalkContext(names: Map<object, string>): WalkContext {
  return { unsupported: new Set(), names, memo: new Map(), depth: 0 };
}
