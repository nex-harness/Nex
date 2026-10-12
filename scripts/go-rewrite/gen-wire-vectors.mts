// 用真实的 TypeScript wire 实现生成 Go 对照向量：
//  - crc32 / base64（wire-binary.ts）
//  - 分片编码（wire-codec.ts encodeTopicWireFrames，使用生产同款 measureTopicNotificationEnvelopeBytes）
//  - 组装器对一组有序/乱序/损坏输入的事件序列（wire-assembler.ts）
// Go 的 wire 测试读取 go/testdata/vectors/wire.json 并要求逐字节/逐事件一致。
import { writeFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { z } from "zod";
import {
  crc32WireBytes,
  encodeWireBytesBase64,
  decodeWireBase64,
} from "../../packages/shared/src/nex-protocol-v4/wire-binary.ts";
import {
  encodeTopicWireFrames,
  measureTopicNotificationEnvelopeBytes,
} from "../../packages/shared/src/nex-protocol-v4/wire-codec.ts";
import { TopicWireFrameAssembler } from "../../packages/shared/src/nex-protocol-v4/wire-assembler.ts";
import { PROTOCOL_V4_LIMITS } from "../../packages/shared/src/nex-protocol-v4/core.ts";

const enc = new TextEncoder();
const hex = (b: Uint8Array) => Buffer.from(b).toString("hex");

// ── crc32 / base64 ──
const bytesCases: Uint8Array[] = [
  new Uint8Array(0),
  enc.encode("a"),
  enc.encode("123456789"),
  enc.encode("hello world"),
  enc.encode("中文😀\u2028"),
  new Uint8Array([0, 255, 128, 1, 2]),
  new Uint8Array(1000).map((_, i) => i % 251),
];
const crcBase64 = bytesCases.map((b) => ({
  hex: hex(b),
  crc32: crc32WireBytes(b),
  base64: b.length ? encodeWireBytesBase64(b) : "",
}));
const badBase64 = [
  "",
  "AAA",
  "AAAA",
  "AA==",
  "A===",
  "AAA=A",
  "!!!!",
  "QUJD",
  "QUJDRA==",
  "QUJDRA=",
  "QUJDRA",
  "QU JD",
  "====",
  "AA=A",
].map((s) => ({ input: s, decoded: ((d) => (d ? hex(d) : null))(decodeWireBase64(s)) }));

// ── 分片编码 ──
// 帧用最小 schema：assembler 的 schema 只要求 topic/subscriptionId 为字符串并保留其余字段。
// seq 必须是数字：让"信封匹配但 schema 不通过"（WIRE_FAULT_INVALID_PAYLOAD）这条路径可被触发。
const frameSchema = z
  .object({ topic: z.string(), subscriptionId: z.string(), seq: z.number() })
  .passthrough();
type Frame = z.infer<typeof frameSchema>;
const measure = (wire: any) => measureTopicNotificationEnvelopeBytes(wire).maxBytes;

const sha256 = (s: string) => createHash("sha256").update(s).digest("hex");
const BIG_BYTES = 64 * 1024; // 超过该大小的帧/分片不内联，只记录 sha256
function mkFrame(payloadBytes: number, text = "x"): Frame {
  return {
    topic: "conversation/s1",
    subscriptionId: "sub-1",
    seq: 1,
    data: text.repeat(payloadBytes),
  };
}
const encodeCases = [
  { name: "tiny", frame: mkFrame(5), maxPhysicalFrameBytes: undefined },
  {
    name: "just-under-limit",
    frame: mkFrame(PROTOCOL_V4_LIMITS.maxFrameBytes - 1500),
    maxPhysicalFrameBytes: undefined,
  },
  {
    name: "over-limit-ascii",
    frame: mkFrame(PROTOCOL_V4_LIMITS.maxFrameBytes * 2 + 123),
    maxPhysicalFrameBytes: undefined,
  },
  { name: "limit-below-envelope-overhead", frame: mkFrame(5000), maxPhysicalFrameBytes: 1024 },
  { name: "small-limit", frame: mkFrame(5000), maxPhysicalFrameBytes: 4096 },
  { name: "small-limit-multibyte", frame: mkFrame(3000, "中"), maxPhysicalFrameBytes: 4096 },
  { name: "small-limit-astral", frame: mkFrame(2000, "😀"), maxPhysicalFrameBytes: 4096 },
  {
    name: "escapes",
    frame: {
      topic: "conversation/s1",
      subscriptionId: "sub-1",
      seq: 1,
      data: '"\\\n\t\u2028\u2029\u0000'.repeat(400),
    },
    maxPhysicalFrameBytes: 4096,
  },
  {
    name: "lone-surrogate",
    frame: {
      topic: "conversation/s1",
      subscriptionId: "sub-1",
      seq: 1,
      data: "a\ud800b".repeat(2500),
    },
    maxPhysicalFrameBytes: 4096,
  },
].map((c, index) => {
  const base = {
    name: c.name,
    logicalFrameId: `lf-${index}`,
    logicalFrameOrdinal: index + 1,
    maxPhysicalFrameBytes: c.maxPhysicalFrameBytes ?? null,
    // 大帧改由 gen 描述重建（Go 侧按同样规则生成），其余直接内联。
    ...(JSON.stringify(c.frame).length > BIG_BYTES
      ? { frameJson: null, gen: { repeatBytes: (c.frame as any).data.length, text: "x" } }
      : { frameJson: JSON.stringify(c.frame), gen: null }),
  };
  try {
    const wires = encodeTopicWireFrames(c.frame, {
      deliveryKind: "online",
      topic: c.frame.topic as string,
      subscriptionId: c.frame.subscriptionId as string,
      logicalFrameId: `lf-${index}`,
      logicalFrameOrdinal: index + 1,
      ...(c.maxPhysicalFrameBytes ? { maxPhysicalFrameBytes: c.maxPhysicalFrameBytes } : {}),
      measurePhysicalFrameBytes: measure,
    });
    const js = wires.map((w) => JSON.stringify(w));
    const inline = js.reduce((n, w) => n + w.length, 0) <= BIG_BYTES;
    return {
      ...base,
      error: null,
      wires: inline ? js : null,
      wireSha256: js.map(sha256),
      wireLengths: js.map((w) => Buffer.byteLength(w)),
      measured: wires.map((w) => measure(w)),
    };
  } catch (e: any) {
    // 编码器拒绝也是契约的一部分：Go 必须在同样的输入上给出同样的 reasonCode。
    return {
      ...base,
      error: e.reasonCode ?? String(e),
      wires: [],
      wireSha256: [],
      wireLengths: [],
      measured: [],
    };
  }
});

// ── 组装器 ──
function runAssembly(
  name: string,
  wires: any[],
  opts: Record<string, number> = {},
  times?: number[],
) {
  const asm = new TopicWireFrameAssembler(frameSchema, opts);
  const events = wires.map((w, i) =>
    asm.accept(w, times ? times[i] : 1_000).map((e) =>
      e.kind === "complete"
        ? { kind: "complete", deliveryKind: e.deliveryKind, frameJson: JSON.stringify(e.frame) }
        : {
            kind: "fault",
            reason: e.fault.reasonCode,
            ordinal: e.fault.logicalFrameOrdinal,
            id: e.fault.logicalFrameId,
            deliveryKind: e.fault.deliveryKind ?? null,
          },
    ),
  );
  return {
    name,
    options: opts,
    times: times ?? null,
    wires: wires.map((w) => JSON.stringify(w)),
    events,
    stats: asm.getStats(),
  };
}
const frag = (n: number) =>
  encodeTopicWireFrames(mkFrame(n), {
    deliveryKind: "online",
    topic: "conversation/s1",
    subscriptionId: "sub-1",
    logicalFrameId: "L1",
    logicalFrameOrdinal: 1,
    maxPhysicalFrameBytes: 4096,
    measurePhysicalFrameBytes: measure,
  }) as any[];
const f = frag(12000);
const assemblyCases = [
  runAssembly("complete-single", [
    encodeTopicWireFrames(mkFrame(10), {
      deliveryKind: "initial",
      topic: "conversation/s1",
      subscriptionId: "sub-1",
      logicalFrameId: "A",
      logicalFrameOrdinal: 1,
      measurePhysicalFrameBytes: measure,
    })[0],
  ]),
  runAssembly("in-order", f),
  runAssembly("reversed", [...f].reverse()),
  runAssembly("duplicate-identical", [f[0], f[0], ...f.slice(1)]),
  runAssembly("duplicate-conflicting", [
    f[0],
    { ...f[0], dataBase64: f[1].dataBase64 },
    ...f.slice(1),
  ]),
  runAssembly(
    "bad-checksum",
    f.map((w, i) =>
      i === 0
        ? { ...w, checksum: { algorithm: "crc32", value: "00000000" } }
        : { ...w, checksum: { algorithm: "crc32", value: "00000000" } },
    ),
  ),
  runAssembly("bad-base64", [{ ...f[0], dataBase64: "!!!!" }]),
  runAssembly("partial-no-timeout-yet", [f[0], f[1]], {}, [1_000, 1_001]),
  runAssembly(
    "timeout-by-next-call",
    [f[0], { ...f[0], logicalFrameOrdinal: 2, logicalFrameId: "L2" }],
    {},
    [1_000, 40_000],
  ),
  runAssembly("older-ordinal-ignored", [
    { ...f[0], logicalFrameOrdinal: 5, logicalFrameId: "L5" },
    f[0],
  ]),
  runAssembly("same-ordinal-different-id", [f[0], { ...f[1], logicalFrameId: "OTHER" }]),
  runAssembly("recovery-supersedes", [
    f[0],
    ...(encodeTopicWireFrames(mkFrame(10), {
      deliveryKind: "recovery",
      topic: "conversation/s1",
      subscriptionId: "sub-1",
      logicalFrameId: "R",
      logicalFrameOrdinal: 2,
      measurePhysicalFrameBytes: measure,
    }) as any[]),
  ]),
  runAssembly("online-supersedes-emits-fault", [
    f[0],
    ...(encodeTopicWireFrames(mkFrame(10), {
      deliveryKind: "online",
      topic: "conversation/s1",
      subscriptionId: "sub-1",
      logicalFrameId: "N",
      logicalFrameOrdinal: 2,
      measurePhysicalFrameBytes: measure,
    }) as any[]),
  ]),
  runAssembly("bad-delivery-kind", [{ ...f[0], deliveryKind: "bogus" }]),
  runAssembly("count-exceeds-bytes", [{ ...f[0], fragmentCount: 3, logicalBytes: 2 }]),
  runAssembly("fragment-index-out-of-range", [{ ...f[0], fragmentIndex: 99 }]),
  runAssembly("unknown-key", [{ ...f[0], extra: 1 }]),
  runAssembly(
    "topic-mismatch-in-frame",
    (() => {
      const w = encodeTopicWireFrames(
        { topic: "other", subscriptionId: "sub-1", seq: 1, pad: "y".repeat(12000) } as any,
        {
          deliveryKind: "online",
          topic: "conversation/s1",
          subscriptionId: "sub-1",
          logicalFrameId: "M",
          logicalFrameOrdinal: 1,
          maxPhysicalFrameBytes: 4096,
          measurePhysicalFrameBytes: measure,
        },
      ) as any[];
      return w;
    })(),
  ),
  runAssembly(
    "concurrent-limit",
    Array.from({ length: 3 }, (_, i) => ({
      ...f[0],
      topic: `conversation/c${i}`,
      subscriptionId: `s${i}`,
      logicalFrameId: `C${i}`,
    })),
    { maxConcurrentAssemblies: 2 },
  ),
  runAssembly(
    "staged-budget",
    [f[0], { ...f[0], topic: "conversation/b", subscriptionId: "sb", logicalFrameId: "B" }],
    { maxStagedDecodedBytes: f[0].dataBase64.length },
  ),
  runAssembly("assembly-too-large", [f[0]], { maxAssemblyBytes: 100 }),
  runAssembly("too-many-fragments", [f[0]], { maxFragments: 2 }),
  runAssembly(
    "invalid-utf8-json",
    (() => {
      const bytes = new Uint8Array([0x7b, 0xff, 0xfe, 0x7d]);
      const b64 = encodeWireBytesBase64(bytes);
      return [
        {
          wireVersion: 3,
          kind: "fragment",
          deliveryKind: "online",
          logicalFrameId: "U",
          logicalFrameOrdinal: 1,
          topic: "conversation/s1",
          subscriptionId: "sub-1",
          fragmentIndex: 0,
          fragmentCount: 1,
          logicalBytes: 4,
          checksum: { algorithm: "crc32", value: crc32WireBytes(bytes) },
          dataBase64: b64,
        },
      ];
    })(),
  ),
  runAssembly(
    "invalid-json",
    (() => {
      const bytes = enc.encode("{not json");
      return [
        {
          wireVersion: 3,
          kind: "fragment",
          deliveryKind: "online",
          logicalFrameId: "J",
          logicalFrameOrdinal: 1,
          topic: "conversation/s1",
          subscriptionId: "sub-1",
          fragmentIndex: 0,
          fragmentCount: 1,
          logicalBytes: bytes.length,
          checksum: { algorithm: "crc32", value: crc32WireBytes(bytes) },
          dataBase64: encodeWireBytesBase64(bytes),
        },
      ];
    })(),
  ),
  runAssembly(
    "invalid-schema",
    (() => {
      const bytes = enc.encode(
        JSON.stringify({ topic: "conversation/s1", subscriptionId: "sub-1", seq: "not-a-number" }),
      );
      return [
        {
          wireVersion: 3,
          kind: "fragment",
          deliveryKind: "online",
          logicalFrameId: "S",
          logicalFrameOrdinal: 1,
          topic: "conversation/s1",
          subscriptionId: "sub-1",
          fragmentIndex: 0,
          fragmentCount: 1,
          logicalBytes: bytes.length,
          checksum: { algorithm: "crc32", value: crc32WireBytes(bytes) },
          dataBase64: encodeWireBytesBase64(bytes),
        },
      ];
    })(),
  ),
];

writeFileSync(
  new URL("../../go/testdata/vectors/wire.json", import.meta.url),
  JSON.stringify(
    {
      limits: PROTOCOL_V4_LIMITS,
      crcBase64,
      badBase64,
      encodeCases,
      assemblyCases,
    },
    null,
    1,
  ) + "\n",
);
console.log(
  `crc=${crcBase64.length} badB64=${badBase64.length} encode=${encodeCases.map((c) => c.name + ":" + (c.error ?? c.wireSha256.length)).join(",")} assembly=${assemblyCases.length}`,
);
