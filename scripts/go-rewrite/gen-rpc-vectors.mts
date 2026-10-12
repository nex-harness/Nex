// G0.9: generate RPC serialization vectors from the REAL TypeScript codec.
//
// packages/rpc/src/serialization.ts defines the binary format the UI and server speak
// over the WebSocket RPC channel. The Go port (go/internal/rpc) must reproduce it byte for
// byte; this script records `serialize(value)` -> hex for a corpus of values, plus what
// `deserialize` returns, so the Go test can assert byte equality and value equality.
//
// The value list deliberately includes the documented edge cases from phase-0 step 0.8:
// undefined, multibyte strings, integer boundaries, NEGATIVE integers, non-integer
// numbers, null/booleans/arrays/nested objects, Uint8Array and the nested-Uint8Array
// marker. Whatever JS does for the negatives is recorded as-is, not "corrected".
import { writeFileSync } from "node:fs";
import {
  BufferReader,
  BufferWriter,
  deserialize,
  serialize,
} from "../../packages/rpc/src/serialization.ts";
import { VSBuffer } from "../../packages/rpc/src/buffer.ts";

interface Vector {
  name: string;
  kind: "undefined" | "value" | "binary";
  value?: unknown;
  hex: string;
  /** What deserialize returns, described in a JSON-safe way. */
  decoded: unknown;
  /** For binary values, the byte list, so the Go test can compare exactly. */
  bytes?: number[];
}

function toHex(buffer: VSBuffer): string {
  return Buffer.from(buffer.buffer).toString("hex");
}

function describeBytes(bytes: Uint8Array): number[] {
  return [...bytes];
}

/** Describe a decoded value in a JSON-safe form the Go test can compare. */
function describe(value: unknown): unknown {
  if (value === undefined) return { __undefined: true };
  if (value instanceof Uint8Array) return { __bytes: [...value] };
  if (Array.isArray(value)) return value.map(describe);
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, val] of Object.entries(value as Record<string, unknown>)) {
      out[key] = describe(val);
    }
    return out;
  }
  if (typeof value === "number" && Number.isNaN(value)) return { __nan: true };
  if (value === Infinity) return { __inf: "Infinity" };
  if (value === -Infinity) return { __inf: "-Infinity" };
  return value;
}

function vector(name: string, value: unknown): Vector {
  const writer = new BufferWriter();
  serialize(writer, value);
  const buffer = writer.buffer;
  const decoded = deserialize(new BufferReader(buffer));
  const isBinary = value instanceof Uint8Array;
  return {
    name,
    kind: value === undefined ? "undefined" : isBinary ? "binary" : "value",
    value: isBinary ? undefined : describeValueInput(value),
    hex: toHex(buffer),
    decoded: describe(decoded),
    bytes: isBinary ? describeBytes(value) : undefined,
  };
}

/** Keep the *input* value JSON-safe too (undefined cannot be stored as JSON). */
function describeValueInput(value: unknown): unknown {
  return describe(value);
}

const bytes0 = new Uint8Array(0);
const bytes300 = new Uint8Array(300);
for (let i = 0; i < 300; i += 1) bytes300[i] = i % 256;
const bytesMulti = new Uint8Array([0x00, 0x7f, 0x80, 0xff, 0xfe, 0x01]);

const values: Array<[string, unknown]> = [
  ["undefined", undefined],
  ["emptyString", ""],
  ["multibyte", "é中"],
  ["emoji", "😀"],
  ["ascii", "hello"],
  ["int0", 0],
  ["int1", 1],
  ["int127", 127],
  ["int128", 128],
  ["int16383", 16383],
  ["int16384", 16384],
  ["intMaxSafe", 2 ** 31 - 1],
  ["intNeg1", -1],
  ["intNeg2147483648", -2147483648],
  ["intNeg2", -2],
  ["intNeg1000", -1000],
  ["nonIntFloat", 1.5],
  ["nonIntLarge", 1e12],
  ["intBeyond32", 2 ** 31],
  ["nan", NaN],
  ["infinity", Infinity],
  ["null", null],
  ["true", true],
  ["false", false],
  ["emptyArray", []],
  ["arrayMixed", [1, "two", null, true, [3, 4]]],
  ["nestedObject", { a: 1, b: { c: "d", e: [true, null] }, f: "é" }],
  ["unicodeObject", { 中文: "值", emoji: "😀" }],
  ["uint8Empty", bytes0],
  ["uint8_300", bytes300],
  ["uint8Multi", bytesMulti],
  [
    "objectWithUint8",
    { payload: bytesMulti, name: "x", nested: { data: new Uint8Array([1, 2, 3]) } },
  ],
  ["arrayOfUint8", [new Uint8Array([1, 2]), new Uint8Array([3])]],
  ["objectWithMarkerLikeKey", { __nex_rpc_nested_uint8array_v1: true, base64: "AAAA" }],
  ["emptyObject", {}],
];

const vectors = values.map(([name, value]) => vector(name, value));

writeFileSync(
  new URL("../../go/testdata/rpc-vectors.json", import.meta.url),
  `${JSON.stringify({ generatedFrom: "scripts/go-rewrite/gen-rpc-vectors.mts", vectors }, null, 1)}\n`,
);

console.log(`wrote ${vectors.length} serialization vectors`);
for (const v of vectors) {
  console.log(`  ${v.name.padEnd(22)} ${v.hex}`);
}
