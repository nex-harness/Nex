// G0.9: record RPC (UI <-> server) frames deterministically.
//
// A live @nex/server needs a full build (the committed dist is stale, and entry-http.ts
// needs a build-time embedded provider config), so this recorder drives the REAL @nex/rpc
// ChannelServer + ChannelClient over createQueuePair() instead of a WebSocket. That is the
// same code path the WebSocket transport uses once framing is stripped, and it makes the
// capture deterministic: no ports, no timing races, no model backend.
//
// Each scenario registers small stub channels on the server side, runs a client action, and
// records every serialized RPC message as {direction, hex, decoded} to
// go/testdata/traces/rpc/<ID>/frames.jsonl. record-rpc.test.mjs re-decodes every frame and
// proves re-serialization is byte-identical, so the capture is faithful to the codec.
//
// Scenarios (phase-0 step 0.8):
//   R01 connect + Initialize handshake
//   R02 promise call returning a value
//   R03 event listen + dispose
//   R04 promise cancel (CancellationToken)
//   R05 call on an unregistered channel (server buffers, then PromiseError)
//   R06 Uint8Array argument and result
//   R07 nested Uint8Array in an object (the __nex_rpc_nested_uint8array_v1 marker)
//   R08 structured error (name/message/stack/code)
//   R09 large message (>1 MiB) and a burst of many small messages
//   R11 serialization of a subscribe-shaped payload (clientMode envelope)
//
// R10 (auth: 401 without token, login, then connect) is an HTTP/WebSocket-level concern and
// is NOT recorded here; see docs/specs/go-rewrite/phase-0-status.md.
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const ROOT = new URL("../../..", import.meta.url).pathname.replace(/\/$/, "");
const OUT = join(ROOT, "go/testdata/traces/rpc");

const rpc = await import(join(ROOT, "packages/rpc/dist/index.js"));
const {
  deserialize,
  BufferReader,
  createQueuePair,
  ChannelServer,
  ChannelClient,
  CancellationTokenSource,
  Event,
} = rpc;

/** Subscribe to the client's Initialize event, then release the server, then await it. */
async function initialize(client, server) {
  const ready = Event.toPromise(client.onDidInitialize);
  server.ready();
  await ready;
}

/**
 * Normalize a stack-trace array: absolute paths are reduced to a basename and each frame's
 * line:column is dropped, so fixtures are deterministic and free of machine paths (the
 * G0.10 scrubber fails on absolute home paths).
 */
function normalizeStackFrames(frames) {
  if (!Array.isArray(frames)) return frames;
  return frames.map((line) =>
    String(line)
      .replace(/file:\/\/\/[^\s)]+/g, (match) => match.split("/").pop() ?? match)
      .replace(/\(.*?:\d+:\d+\)/g, "(<path>)"),
  );
}

function toJson(value) {
  if (value === undefined) return { __undefined: true };
  if (value instanceof Uint8Array) return { __bytes: [...value] };
  if (Array.isArray(value)) return value.map(toJson);
  if (value && typeof value === "object" && value.constructor === Object) {
    const out = {};
    for (const [k, v] of Object.entries(value)) {
      out[k] = k === "stack" ? normalizeStackFrames(v) : toJson(v);
    }
    return out;
  }
  return value;
}

function decodeFrame(buffer) {
  try {
    const reader = new BufferReader(buffer);
    return { header: toJson(deserialize(reader)), body: toJson(deserialize(reader)) };
  } catch {
    return null;
  }
}

/**
 * Wrap a protocol so every sent message is recorded. `onMessage` is the inner protocol's
 * event function itself (an Event is callable): calling it subscribes a listener.
 * `direction` labels the sender.
 */
function recordingProtocol(inner, frames, direction) {
  return {
    send: (buffer) => {
      frames.push({
        direction,
        hex: Buffer.from(buffer.buffer).toString("hex"),
        decoded: decodeFrame(buffer),
      });
      inner.send(buffer);
    },
    onMessage: inner.onMessage,
  };
}

function makeStructuredError() {
  const error = new Error("channel exploded");
  error.name = "ExplodedError";
  error.code = "E_EXPLODE";
  error.retryAfterMs = 42;
  return error;
}

function buildServer(protocol) {
  const server = new ChannelServer(protocol, "ctx", 300, true);
  server.registerChannel("demo", {
    call: async (_ctx, command, arg) => (command === "echo" ? arg : undefined),
    listen: (_ctx, _event) => (listener) => {
      setTimeout(() => listener("hello-event"), 10);
      return { dispose: () => {} };
    },
  });
  server.registerChannel("slow", {
    call: async () => {
      await delay(5000);
      return "too late";
    },
    listen: () => () => ({ dispose: () => {} }),
  });
  server.registerChannel("binary", {
    call: async (_ctx, _cmd, arg) => {
      const out = new Uint8Array(arg.length);
      for (let i = 0; i < arg.length; i++) out[i] = arg[arg.length - 1 - i];
      return out;
    },
    listen: () => () => ({ dispose: () => {} }),
  });
  server.registerChannel("nested", {
    call: async (_ctx, _cmd, arg) => ({ payload: arg.payload, name: arg.name }),
    listen: () => () => ({ dispose: () => {} }),
  });
  server.registerChannel("failing", {
    call: async () => {
      throw makeStructuredError();
    },
    listen: () => () => ({ dispose: () => {} }),
  });
  server.registerChannel("large", {
    call: async (_ctx, _cmd, arg) => arg.length,
    listen: () => () => ({ dispose: () => {} }),
  });
  server.registerChannel("burst", {
    call: async (_ctx, _cmd, arg) => arg,
    listen: () => () => ({ dispose: () => {} }),
  });
  server.registerChannel("subscribe-shaped", {
    call: async () => ({ ack: { subscriptionId: "s1", mode: "snapshot", logEpoch: "e1" } }),
    listen: () => () => ({ dispose: () => {} }),
  });
  return server;
}

const scenarios = {
  async R01(frames, client, server) {
    await initialize(client, server);
  },

  async R02(frames, client, server) {
    await initialize(client, server);
    const result = await client.getChannel("demo").call("echo", { value: 7 });
    if (result?.value !== 7) throw new Error("R02 echo mismatch");
  },

  async R03(frames, client, server) {
    await initialize(client, server);
    const seen = [];
    const disposable = client.getChannel("demo").listen("tick")((value) => seen.push(value));
    await delay(30);
    disposable.dispose();
    await delay(30);
    if (!seen.includes("hello-event")) throw new Error("R03 did not receive event");
  },

  async R04(frames, client, server) {
    await initialize(client, server);
    const source = new CancellationTokenSource();
    const promise = client.getChannel("slow").call("wait", undefined, source.token);
    source.cancel();
    try {
      await promise;
    } catch {
      /* expected: cancellation rejects the promise */
    }
    await delay(20);
  },

  async R05(frames, client, server) {
    await initialize(client, server);
    try {
      await client.getChannel("unregistered").call("nope");
      throw new Error("R05 unexpectedly succeeded");
    } catch (error) {
      if (!/timed out|Unknown channel/i.test(String(error?.message ?? error))) {
        throw new Error(`R05 unexpected error: ${error}`);
      }
    }
  },

  async R06(frames, client, server) {
    await initialize(client, server);
    const result = await client
      .getChannel("binary")
      .call("reverse", new Uint8Array([1, 2, 3, 255]));
    if (!(result instanceof Uint8Array) || result[0] !== 255)
      throw new Error("R06 binary mismatch");
  },

  async R07(frames, client, server) {
    await initialize(client, server);
    const result = await client
      .getChannel("nested")
      .call("wrap", { payload: new Uint8Array([9, 8, 7]), name: "x" });
    if (!(result?.payload instanceof Uint8Array) || result.payload.length !== 3) {
      throw new Error("R07 nested binary not restored");
    }
  },

  async R08(frames, client, server) {
    await initialize(client, server);
    try {
      await client.getChannel("failing").call("boom");
      throw new Error("R08 unexpectedly succeeded");
    } catch (error) {
      if (error?.code !== "E_EXPLODE") throw new Error(`R08 lost error.code: ${error}`);
    }
  },

  async R09(frames, client, server) {
    await initialize(client, server);
    const big = "x".repeat(1024 * 1024 + 1024);
    const length = await client.getChannel("large").call("length", big);
    if (length !== big.length) throw new Error("R09 large payload mismatch");
    await Promise.all(
      Array.from({ length: 200 }, (_, i) => client.getChannel("burst").call("noop", i)),
    );
  },

  async R11(frames, client, server) {
    await initialize(client, server);
    await client.getChannel("subscribe-shaped").call("subscribe", {
      topic: "conversation/abc",
      connectionId: "conn-1",
      clientMode: "desktop-continuous",
      workspace: { workspacePath: "/w", workspaceKey: "/w" },
    });
  },
};

async function main() {
  rmSync(OUT, { recursive: true, force: true });
  const counts = {};
  for (const id of Object.keys(scenarios).sort()) {
    const frames = [];
    const [sideA, sideB] = createQueuePair();
    const server = buildServer(recordingProtocol(sideB, frames, "server-to-client"));
    const client = new ChannelClient(recordingProtocol(sideA, frames, "client-to-server"));
    await scenarios[id](frames, client, server);
    client.dispose();
    server.dispose();
    const dir = join(OUT, id);
    mkdirSync(dir, { recursive: true });
    writeFileSync(
      join(dir, "frames.jsonl"),
      `${frames.map((f) => JSON.stringify(f)).join("\n")}\n`,
    );
    writeFileSync(
      join(dir, "meta.json"),
      `${JSON.stringify(
        {
          id,
          transport: "in-process-queue-pair",
          note: "same codec as the WebSocket transport, without the 13-byte framing",
        },
        null,
        2,
      )}\n`,
    );
    counts[id] = frames.length;
  }
  console.log("recorded scenarios:", JSON.stringify(counts));
}

await main();
