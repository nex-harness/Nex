/**
 * Golden fixture generator for the Go rpcwire package.
 *
 * Every payload byte in this directory is produced by the real TypeScript
 * implementation under packages/rpc/src. Nothing here re-implements the wire
 * format:
 *
 * - Serialization uses `serialize` / `BufferWriter` from serialization.ts.
 * - Client payloads are captured from a real `ChannelClient` driving
 *   `call`, `listen` and cancellation (channelClient.ts).
 * - Server payloads are captured from a real `ChannelServer` responding to
 *   those captured client payloads (channelServer.ts).
 * - Frame bytes come from `writeProtocolMessage` (protocol.ts).
 *
 * `ChannelClient.sendRequest` / `ChannelServer.sendResponse` are private, so
 * the header layouts are exercised through the public classes rather than by
 * copying their code.
 *
 * Regenerate from the repo root:
 *   node_modules/.bin/tsx next/server/internal/rpcwire/testdata/gen.ts
 */

import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { VSBuffer } from "../../../../../packages/rpc/src/buffer.js";
import { CancellationTokenSource, Emitter } from "../../../../../packages/rpc/src/foundation.js";
import type { IMessagePassingProtocol } from "../../../../../packages/rpc/src/protocol.js";
import {
  ProtocolMessage,
  ProtocolMessageType,
  writeProtocolMessage,
} from "../../../../../packages/rpc/src/protocol.js";
import { ChannelClient } from "../../../../../packages/rpc/src/channelClient.js";
import { ChannelServer } from "../../../../../packages/rpc/src/channelServer.js";
import type { IServerChannel } from "../../../../../packages/rpc/src/channels.shared.js";

const HERE = dirname(fileURLToPath(import.meta.url));

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

/** CaptureProtocol records every payload sent by a client or server. */
class CaptureProtocol implements IMessagePassingProtocol {
  readonly sent: VSBuffer[] = [];
  private readonly emitter = new Emitter<VSBuffer>();
  readonly onMessage = this.emitter.event;

  send(buffer: VSBuffer): void {
    this.sent.push(buffer);
  }

  deliver(payload: VSBuffer): void {
    this.emitter.fire(payload);
  }
}

const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

/** Initialize payload emitted by a real ChannelServer on construction. */
function captureInitialize(): VSBuffer {
  const protocol = new CaptureProtocol();
  new ChannelServer(protocol, "server");
  return protocol.sent[0];
}

const INITIALIZE = captureInitialize();

interface ClientCapture {
  sent: VSBuffer[];
  dispose(): void;
}

/** Drive a fresh real ChannelClient and record what it writes. */
async function withClient<T>(
  body: (channel: ReturnType<ChannelClient["getChannel"]>, protocol: CaptureProtocol) => T,
): Promise<{ result: T; sent: VSBuffer[] }> {
  const protocol = new CaptureProtocol();
  const client = new ChannelClient(protocol);
  protocol.deliver(INITIALIZE);
  await tick();
  const result = body(client.getChannel("demo"), protocol);
  await tick();
  const sent = protocol.sent.slice();
  client.dispose();
  return { result, sent };
}

/**
 * Build a canonical Promise request payload using the real client. Used as
 * input to the real ChannelServer for response fixtures.
 */
async function clientPromisePayload(arg: unknown): Promise<VSBuffer> {
  const { sent } = await withClient((channel) => {
    void channel.call("echo", arg).catch(() => {});
  });
  return sent[0];
}

/** Run a request through a real ChannelServer and return its single response. */
async function serverResponse(request: VSBuffer, channel: IServerChannel): Promise<VSBuffer> {
  const protocol = new CaptureProtocol();
  const server = new ChannelServer(protocol, "server");
  server.registerChannel("demo", channel);
  await tick();
  const before = protocol.sent.length;
  protocol.deliver(request);
  await tick();
  await tick();
  const responses = protocol.sent.slice(before);
  if (responses.length !== 1) {
    throw new Error(`expected exactly one response, got ${responses.length}`);
  }
  return responses[0];
}

// ---------------------------------------------------------------------------
// Fixture encoding helpers
// ---------------------------------------------------------------------------

type Logical =
  | { t: "undef" }
  | { t: "str"; v: string }
  | { t: "buf"; v: string; vs: boolean }
  | { t: "arr"; v: Logical[] }
  | { t: "obj"; v: unknown }
  | { t: "int"; v: number }
  | { t: "jsonnum"; v: string };

function logical(value: unknown): Logical {
  if (value === undefined) return { t: "undef" };
  if (typeof value === "string") return { t: "str", v: value };
  if (value instanceof VSBuffer) {
    return { t: "buf", v: Buffer.from(value.buffer).toString("base64"), vs: true };
  }
  if (value instanceof Uint8Array) {
    return { t: "buf", v: Buffer.from(value).toString("base64"), vs: false };
  }
  if (Array.isArray(value)) return { t: "arr", v: value.map(logical) };
  if (typeof value === "number") {
    // serialize() only takes the VQL integer branch when (value | 0) === value.
    return (value | 0) === value
      ? { t: "int", v: value }
      : { t: "jsonnum", v: JSON.stringify(value) };
  }
  return { t: "obj", v: value };
}

interface FrameHeader {
  type: number;
  id: number;
  ack: number;
}

interface FixtureMeta {
  name: string;
  description: string;
  direction: "client-to-server" | "server-to-client" | "synthetic";
  frame: FrameHeader;
  expected:
    | {
        kind: "request";
        type: number;
        id: number;
        channel?: string;
        name?: string;
        arg: Logical;
      }
    | { kind: "response"; type: number; id?: number; body: Logical }
    | { kind: "frame" }
    | { kind: "frames"; frames: FrameHeader[] };
}

function writeFixture(name: string, frameBytes: Uint8Array, meta: FixtureMeta): void {
  writeFileSync(join(HERE, `${name}.bin`), frameBytes);
  writeFileSync(join(HERE, `${name}.json`), `${JSON.stringify(meta, null, 2)}\n`);
}

function frame(payload: VSBuffer, header: FrameHeader = { type: 1, id: 0, ack: 0 }): Uint8Array {
  const msg = new ProtocolMessage(
    header.type as ProtocolMessageType,
    header.id,
    header.ack,
    payload,
  );
  return new Uint8Array(writeProtocolMessage(msg).buffer);
}

function writeRequest(
  name: string,
  description: string,
  payload: VSBuffer,
  meta: Omit<FixtureMeta["expected"] & { kind: "request" }, "kind">,
): void {
  writeFixture(name, frame(payload), {
    name,
    description,
    direction: "client-to-server",
    frame: { type: 1, id: 0, ack: 0 },
    expected: { kind: "request", ...meta },
  });
}

function writeResponse(
  name: string,
  description: string,
  payload: VSBuffer,
  expected: Extract<FixtureMeta["expected"], { kind: "response" }>,
): void {
  writeFixture(name, frame(payload), {
    name,
    description,
    direction: "server-to-client",
    frame: { type: 1, id: 0, ack: 0 },
    expected,
  });
}

// ---------------------------------------------------------------------------
// Client requests
// ---------------------------------------------------------------------------

async function generateClientFixtures(): Promise<void> {
  const stringArg = "hello wire";
  {
    const { sent } = await withClient((channel) => {
      void channel.call("echo", stringArg).catch(() => {});
    });
    writeRequest("client-promise-string", "Promise request with a string argument", sent[0], {
      type: 100,
      id: 0,
      channel: "demo",
      name: "echo",
      arg: logical(stringArg),
    });
  }

  {
    const objectArg = {
      alpha: 1,
      beta: "two",
      nested: { ok: true, empty: null },
      list: [1, "x", false, null],
    };
    const { sent } = await withClient((channel) => {
      void channel.call("echo", objectArg).catch(() => {});
    });
    writeRequest(
      "client-promise-object",
      "Promise request with a plain object argument (tag 5 JSON fallback)",
      sent[0],
      { type: 100, id: 0, channel: "demo", name: "echo", arg: logical(objectArg) },
    );
  }

  {
    const arrayArg = ["a", 1, true, null, [2, 3]];
    const { sent } = await withClient((channel) => {
      void channel.call("echo", arrayArg).catch(() => {});
    });
    writeRequest("client-promise-array", "Promise request with a mixed array argument", sent[0], {
      type: 100,
      id: 0,
      channel: "demo",
      name: "echo",
      arg: logical(arrayArg),
    });
  }

  {
    const { sent } = await withClient((channel) => {
      void channel.call("echo").catch(() => {});
    });
    writeRequest(
      "client-promise-undefined",
      "Promise request with no argument (undefined tag 0)",
      sent[0],
      { type: 100, id: 0, channel: "demo", name: "echo", arg: logical(undefined) },
    );
  }

  {
    const bytes = new Uint8Array([0x00, 0x01, 0x02, 0xfa, 0xfe, 0xff]);
    const { sent } = await withClient((channel) => {
      void channel.call("echo", bytes).catch(() => {});
    });
    writeRequest(
      "client-promise-buffer",
      "Promise request with a Uint8Array argument (tag 2)",
      sent[0],
      { type: 100, id: 0, channel: "demo", name: "echo", arg: logical(bytes) },
    );
  }

  {
    const bytes = VSBuffer.wrap(new Uint8Array([0xde, 0xad, 0xbe, 0xef]));
    const { sent } = await withClient((channel) => {
      void channel.call("echo", bytes).catch(() => {});
    });
    writeRequest(
      "client-promise-vsbuffer",
      "Promise request with a VSBuffer argument (tag 3)",
      sent[0],
      { type: 100, id: 0, channel: "demo", name: "echo", arg: logical(bytes) },
    );
  }

  {
    const { sent } = await withClient((channel) => {
      const cts = new CancellationTokenSource();
      const pending = channel.call("echo", "cancel me", cts.token);
      cts.cancel();
      void pending.catch(() => {});
    });
    // sent[0] is the Promise request, sent[1] the PromiseCancel.
    writeRequest(
      "client-promise-cancel",
      "PromiseCancel request emitted by CancellationToken",
      sent[1],
      { type: 101, id: 0, arg: logical(undefined) },
    );
  }

  {
    const arg = { topic: "workspace" };
    const { sent } = await withClient((channel) => {
      const event = channel.listen("onDidChange", arg);
      const subscription = event(() => {});
      subscription.dispose();
    });
    writeRequest(
      "client-event-listen",
      "EventListen request emitted when the first listener subscribes",
      sent[0],
      {
        type: 102,
        id: 0,
        channel: "demo",
        name: "onDidChange",
        arg: logical(arg),
      },
    );
    writeRequest(
      "client-event-dispose",
      "EventDispose request emitted when the last listener unsubscribes",
      sent[1],
      { type: 103, id: 0, arg: logical(undefined) },
    );
  }

  {
    const unicode =
      'unicode: 你好, мир, café, 😀🚀, quote " backslash \\ newline \n tab \t nul \u0000, ls \u2028 ps \u2029 end';
    const { sent } = await withClient((channel) => {
      void channel.call("echo", unicode).catch(() => {});
    });
    writeRequest(
      "client-promise-unicode",
      "Promise request carrying unicode and JSON-hostile characters",
      sent[0],
      { type: 100, id: 0, channel: "demo", name: "echo", arg: logical(unicode) },
    );
  }

  {
    const large = `${"a".repeat(70_000)}-tail`;
    const { sent } = await withClient((channel) => {
      void channel.call("echo", large).catch(() => {});
    });
    writeRequest(
      "client-promise-large",
      "Promise request with a >64 KiB string argument (multi-byte VQL length)",
      sent[0],
      { type: 100, id: 0, channel: "demo", name: "echo", arg: logical(large) },
    );
  }

  {
    // Header array index 1 carries the request id. ChannelClient starts at 0,
    // so a non-zero id needs one preceding request; the second capture is the
    // one with id 1.
    const { sent } = await withClient((channel) => {
      void channel.call("echo", "first").catch(() => {});
      void channel.call("echo", "second").catch(() => {});
    });
    writeRequest(
      "client-promise-id1",
      "Promise request with request id 1 (second call on the same client)",
      sent[1],
      { type: 100, id: 1, channel: "demo", name: "echo", arg: logical("second") },
    );
  }
}

// ---------------------------------------------------------------------------
// Server responses
// ---------------------------------------------------------------------------

const FIXED_STACK = [
  "BoomError: boom",
  "    at alpha (file:///app/alpha.ts:1:2)",
  "    at beta (file:///app/beta.ts:3:4)",
];

const ERROR_BODY = {
  message: 'boom "quoted"\nsecond line',
  name: "BoomError",
  stack: FIXED_STACK,
};

function stubError(): Error {
  const error = new Error(ERROR_BODY.message);
  error.name = ERROR_BODY.name;
  error.stack = FIXED_STACK.join("\n");
  return error;
}

const ECHO_CHANNEL: IServerChannel = {
  async call(_ctx, command, arg) {
    if (command === "throw") throw stubError();
    if (command === "throw-object") {
      // Non-Error rejection values travel through PromiseErrorObj.
      throw { code: "E_FAIL", detail: { reason: "nope" } };
    }
    return arg;
  },
  listen(_ctx, _event, _arg) {
    return (listener: (value: unknown) => void) => {
      listener({ kind: "tick", n: 1 });
      return { dispose() {} };
    };
  },
};

async function generateServerFixtures(): Promise<void> {
  writeResponse("server-initialize", "Initialize response sent on connect", INITIALIZE, {
    kind: "response",
    type: 200,
    body: logical(undefined),
  });

  const successCases: Array<{ name: string; description: string; value: unknown }> = [
    {
      name: "server-promise-success-object",
      description: "PromiseSuccess with a plain object body",
      value: { ok: true, items: [1, 2, 3], nested: { name: "nested" } },
    },
    {
      name: "server-promise-success-undefined",
      description: "PromiseSuccess with an undefined body",
      value: undefined,
    },
    {
      name: "server-promise-success-string",
      description: "PromiseSuccess with a string body",
      value: "result text",
    },
    {
      name: "server-promise-success-int",
      description: "PromiseSuccess with a VQL integer body",
      value: 42,
    },
    {
      name: "server-promise-success-int-negative",
      description: "PromiseSuccess with a negative VQL integer body",
      value: -5,
    },
    {
      name: "server-promise-success-int-overflow",
      description: "PromiseSuccess with 2^31, which TypeScript serializes via the JSON fallback",
      value: 2 ** 31,
    },
    {
      name: "server-promise-success-buffer",
      description: "PromiseSuccess with a Uint8Array body",
      value: new Uint8Array([0x0a, 0x0b, 0x0c, 0xff]),
    },
  ];

  for (const testCase of successCases) {
    const request = await clientPromisePayload(testCase.value);
    const response = await serverResponse(request, ECHO_CHANNEL);
    writeResponse(testCase.name, testCase.description, response, {
      kind: "response",
      type: 201,
      id: 0,
      body: logical(testCase.value),
    });
  }

  {
    const request = await clientPromisePayload("ignored");
    const response = await serverResponse(request, {
      ...ECHO_CHANNEL,
      async call() {
        throw stubError();
      },
    } as IServerChannel);
    writeResponse("server-promise-error", "PromiseError with message, name and stack", response, {
      kind: "response",
      type: 202,
      id: 0,
      body: logical(ERROR_BODY),
    });
  }

  {
    const request = await clientPromisePayload("ignored");
    const rejection = { code: "E_FAIL", detail: { reason: "nope" } };
    const response = await serverResponse(request, {
      ...ECHO_CHANNEL,
      async call() {
        throw rejection;
      },
    } as IServerChannel);
    writeResponse(
      "server-promise-error-obj",
      "PromiseErrorObj with a non-Error rejection object",
      response,
      { kind: "response", type: 203, id: 0, body: logical(rejection) },
    );
  }

  {
    // EventListen request produced by the real client, then a real EventFire
    // response emitted by ChannelServer when the channel's event fires.
    const eventArg = { topic: "workspace" };
    const { sent } = await withClient((channel) => {
      const subscription = channel.listen("onDidChange", eventArg)(() => {});
      subscription.dispose();
    });
    const response = await serverResponse(sent[0], ECHO_CHANNEL);
    writeResponse("server-event-fire", "EventFire response for a subscribed event", response, {
      kind: "response",
      type: 204,
      id: 0,
      body: logical({ kind: "tick", n: 1 }),
    });
  }
}

// ---------------------------------------------------------------------------
// Raw frame headers
// ---------------------------------------------------------------------------

const FRAME_TYPES: Array<[string, ProtocolMessageType, boolean]> = [
  ["none", ProtocolMessageType.None, false],
  ["regular", ProtocolMessageType.Regular, true],
  ["control", ProtocolMessageType.Control, false],
  ["ack", ProtocolMessageType.Ack, false],
  ["disconnect", ProtocolMessageType.Disconnect, false],
  ["replay-request", ProtocolMessageType.ReplayRequest, false],
  ["pause", ProtocolMessageType.Pause, false],
  ["resume", ProtocolMessageType.Resume, false],
  ["keep-alive", ProtocolMessageType.KeepAlive, false],
];

function generateFrameFixtures(): void {
  const probe = VSBuffer.fromString("probe");

  for (const [label, type, isRegular] of FRAME_TYPES) {
    const header: FrameHeader = isRegular
      ? { type, id: 0, ack: 0 }
      : { type, id: 0x01020304, ack: 0x05060708 };
    const name = `frame-${label}`;
    writeFixture(name, frame(probe, header), {
      name,
      description:
        label === "regular"
          ? "Regular frame with a 5-byte payload; the only type SocketProtocol sends or delivers"
          : `Frame header for ProtocolMessageType.${label}; defined by the enum but ignored by SocketProtocol`,
      direction: "synthetic",
      frame: header,
      expected: { kind: "frame" },
    });
  }

  {
    const name = "frame-regular-empty";
    writeFixture(name, frame(VSBuffer.alloc(0), { type: 1, id: 0, ack: 0 }), {
      name,
      description:
        "Regular frame with a zero-length payload (SocketProtocol fires an empty message)",
      direction: "synthetic",
      frame: { type: 1, id: 0, ack: 0 },
      expected: { kind: "frame" },
    });
  }

  {
    const name = "frame-regular-initialize";
    writeFixture(name, frame(INITIALIZE), {
      name,
      description: "Regular frame carrying a server Initialize payload",
      direction: "synthetic",
      frame: { type: 1, id: 0, ack: 0 },
      expected: { kind: "frame" },
    });
  }

  {
    // SocketProtocol ignores id and ack entirely (protocol.ts:267-270), so a
    // Regular frame with non-zero values must still deliver its payload.
    const name = "frame-regular-nonzero-id-ack";
    writeFixture(name, frame(INITIALIZE, { type: 1, id: 0x01020304, ack: 0x05060708 }), {
      name,
      description: "Regular frame with non-zero id/ack; SocketProtocol still delivers the payload",
      direction: "synthetic",
      frame: { type: 1, id: 0x01020304, ack: 0x05060708 },
      expected: { kind: "frame" },
    });
  }

  {
    // No TS sender concatenates frames into one WS message, but SocketProtocol's
    // ChunkStream accepts it on receive. This fixture pins that tolerance.
    const name = "frames-concatenated";
    const first = frame(INITIALIZE);
    const second = frame(VSBuffer.fromString("second"));
    const concat = new Uint8Array(first.byteLength + second.byteLength);
    concat.set(first, 0);
    concat.set(second, first.byteLength);
    writeFixture(name, concat, {
      name,
      description: "Two complete frames in one byte stream; receivers must decode both",
      direction: "synthetic",
      frame: { type: 1, id: 0, ack: 0 },
      expected: {
        kind: "frames",
        frames: [
          { type: 1, id: 0, ack: 0 },
          { type: 1, id: 0, ack: 0 },
        ],
      },
    });
  }
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

async function main(): Promise<void> {
  mkdirSync(HERE, { recursive: true });
  await generateClientFixtures();
  await generateServerFixtures();
  generateFrameFixtures();
  console.log(`wrote fixtures to ${HERE}`);
}

await main();
