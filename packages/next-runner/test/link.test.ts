import { test } from "node:test";
import assert from "node:assert/strict";
import { WebSocketServer, type WebSocket as WsSocket } from "ws";
import type { AddressInfo } from "node:net";
import {
  commandAckSchema,
  conversationTopicWireFrameSchema,
  sessionsIndexTopicWireFrameSchema,
} from "@nex/shared/nex-protocol-v4";
import { RunnerLink } from "../src/link/client.js";
import { ChannelRegistry } from "../src/link/registry.js";
import { ConversationPublisher } from "../src/v4/publisher.js";
import { registerChannels } from "../src/channels/register.js";
import { FakeEngine } from "./helpers/fakeEngine.js";
import { testRoots } from "./helpers/testRoots.js";
import { createLogger } from "../src/logger.js";

const WORKSPACE = "/workspace/demo";
const logger = createLogger("error");

interface FrameReader {
  next(): Promise<Record<string, unknown>>;
  /** Reads until a result for `id`, buffering every other frame into `events`. */
  waitResult(id: string, events: Array<Record<string, unknown>>): Promise<Record<string, unknown>>;
  /** Resolves after `events` contains a frame matching `predicate` (pump keeps draining). */
  waitEvent(
    events: Array<Record<string, unknown>>,
    predicate: (frame: Record<string, unknown>) => boolean,
    timeoutMs?: number,
  ): Promise<Record<string, unknown>>;
}

function frameReader(socket: WsSocket): FrameReader {
  // A background pump drains every inbound frame; unsolicited event frames must not be
  // left in the socket while the test awaits a result, or later polls never see them.
  const log: Record<string, unknown>[] = [];
  let consumed = 0;
  const reader: FrameReader = {
    next() {
      const buffered = log[consumed];
      if (buffered) {
        consumed += 1;
        return Promise.resolve(buffered);
      }
      return new Promise((resolve) => {
        const timer = setInterval(() => {
          const next = log[consumed];
          if (next) {
            clearInterval(timer);
            consumed += 1;
            resolve(next);
          }
        }, 2);
        timer.unref?.();
      });
    },
    async waitResult(id, events) {
      for (;;) {
        const frame = await reader.next();
        if (frame["t"] === "result" && frame["id"] === id) return frame;
        events.push(frame);
      }
    },
    async waitEvent(events, predicate, timeoutMs = 3000) {
      const found = events.find(predicate);
      if (found) return found;
      const deadline = Date.now() + timeoutMs;
      while (Date.now() < deadline) {
        const frame = await reader.next();
        events.push(frame);
        if (predicate(frame)) return frame;
      }
      throw new Error("timed out waiting for event");
    },
  };
  socket.on("message", (data) => {
    log.push(JSON.parse(data.toString("utf8")) as Record<string, unknown>);
  });
  return reader;
}

async function startFakeServer(
  onConnection: (socket: WsSocket) => void,
): Promise<{ url: string; close(): Promise<void> }> {
  const server = new WebSocketServer({ port: 0, host: "127.0.0.1" });
  await new Promise<void>((resolve) => server.once("listening", () => resolve()));
  server.on("connection", (socket, request) => {
    assert.equal(request.headers.authorization, "Bearer test-token");
    onConnection(socket);
  });
  const address = server.address() as AddressInfo;
  return {
    url: `ws://127.0.0.1:${address.port}/runner`,
    close: () =>
      new Promise<void>((resolve, reject) => {
        for (const client of server.clients) client.terminate();
        server.close((error) => (error ? reject(error) : resolve()));
      }),
  };
}

function harness() {
  const engine = new FakeEngine();
  const publisher = new ConversationPublisher({ engine, logger });
  const registry = new ChannelRegistry();
  registerChannels(registry, {
    engine,
    publisher,
    logger,
    roots: testRoots([WORKSPACE]),
    runnerId: "runner_test",
  });
  return { engine, publisher, registry };
}

function startLink(url: string, registry: ChannelRegistry): RunnerLink {
  const link = new RunnerLink({
    url,
    token: "test-token",
    logger,
    registry,
    hello: {
      runnerId: "runner_test",
      name: "test",
      version: "0.1.0",
      os: "darwin",
      arch: "arm64",
      // docs/next/runner-update.md decision 2: build identity + capabilities.
      buildId: "0123456789abcdef",
      capabilities: ["runner-workspaces", "machine-scope", "search", "self-update"],
      workspaces: [{ path: WORKSPACE, label: "demo" }],
    },
    pingIntervalMs: 60_000,
  });
  link.start();
  return link;
}

test("link: hello → createSession → listen → subscribe → sendText → initial + online completedSuccess", async () => {
  const { engine, publisher, registry } = harness();
  const serverEvents: Array<Record<string, unknown>> = [];
  let doneResolve: (() => void) | null = null;
  let doneReject: ((error: Error) => void) | null = null;
  const done = new Promise<void>((resolve, reject) => {
    doneResolve = resolve;
    doneReject = reject;
  });

  const server = await startFakeServer(async (socket) => {
    try {
      const reader = frameReader(socket);
      const hello = await reader.next();
      assert.equal(hello["t"], "hello");
      assert.equal(hello["runnerId"], "runner_test");
      assert.deepEqual(hello["workspaces"], [{ path: WORKSPACE, label: "demo" }]);
      socket.send(JSON.stringify({ t: "welcome", serverVersion: "test" }));

      let callId = 0;
      const call = async (
        channel: string,
        method: string,
        arg: unknown,
        sink: Array<Record<string, unknown>>,
      ) => {
        callId += 1;
        const id = `call-${callId}`;
        socket.send(JSON.stringify({ t: "call", id, channel, method, arg: [arg] }));
        return reader.waitResult(id, sink);
      };

      // Drain unsolicited events into `serverEvents` while awaiting results.
      const created = await call(
        "nex-agent",
        "sendConversationCommandV4",
        {
          workspacePath: WORKSPACE,
          envelope: {
            commandId: "cmd-create",
            clientId: "client-1",
            sessionId: null,
            type: "createSession",
            payload: { workspaceId: WORKSPACE },
            issuedAt: Date.now(),
          },
        },
        serverEvents,
      );
      assert.equal(created["ok"], true);
      const createAck = commandAckSchema.parse(created["value"]);
      const sessionId =
        createAck.result?.type === "createSession" ? createAck.result.sessionId : "";
      assert.ok(sessionId);

      socket.send(
        JSON.stringify({
          t: "listen",
          id: "listen-conv",
          channel: "nex-agent",
          event: "onDynamicConversationFrame",
          arg: [{ workspacePath: WORKSPACE }],
        }),
      );

      const subscribed = await call(
        "nex-agent",
        "subscribeConversationV4",
        { workspacePath: WORKSPACE, sessionId },
        serverEvents,
      );
      assert.equal(subscribed["ok"], true);
      const ack = subscribed["value"] as { ack: { subscriptionId: string; mode: string } };
      assert.equal(ack.ack.mode, "snapshot");
      assert.ok(ack.ack.subscriptionId);

      const sent = await call(
        "nex-agent",
        "sendConversationCommandV4",
        {
          workspacePath: WORKSPACE,
          envelope: {
            commandId: "cmd-send",
            clientId: "client-1",
            sessionId,
            type: "sendText",
            payload: { text: "ping" },
            issuedAt: Date.now(),
          },
        },
        serverEvents,
      );
      assert.equal(sent["ok"], true);
      assert.equal(commandAckSchema.parse(sent["value"]).status, "accepted");

      // Real pi order: the user input is persisted first, then the run streams.
      engine.emit(sessionId, { type: "assistant_start", at: 1_700_000_000_001 });
      engine.emit(sessionId, { type: "text_delta", at: 1_700_000_000_002, delta: "po" });
      engine.emit(sessionId, { type: "text_delta", at: 1_700_000_000_003, delta: "ng" });
      engine.emit(sessionId, { type: "assistant_end", at: 1_700_000_000_004, stopReason: "stop" });
      engine.emit(sessionId, { type: "settled", at: 1_700_000_000_005, aborted: false });

      const completionFrame = await reader.waitEvent(
        serverEvents,
        (frame) =>
          frame["t"] === "event" &&
          (
            frame["value"] as {
              frame?: { payload?: { snapshot?: { control?: { phase?: string } } } };
            }
          )?.frame?.payload?.snapshot?.control?.phase === "completedSuccess",
      );
      const wire = conversationTopicWireFrameSchema.parse(
        (completionFrame as { value: unknown }).value,
      );
      assert.equal(wire.deliveryKind, "online");
      assert.equal(wire.subscriptionId, ack.ack.subscriptionId);
      assert.ok(wire.logicalFrameOrdinal >= 2);
      doneResolve?.();
    } catch (error) {
      doneReject?.(error instanceof Error ? error : new Error(String(error)));
    }
  });

  const link = startLink(server.url, registry);
  try {
    await Promise.race([
      done,
      new Promise<void>((_, reject) =>
        setTimeout(() => reject(new Error("link scenario timed out")), 5000),
      ),
    ]);
    const initial = serverEvents
      .filter((frame) => frame["t"] === "event")
      .map((frame) => frame["value"])
      .find((value) => (value as { deliveryKind?: string }).deliveryKind === "initial");
    assert.ok(initial, "initial frame must be pushed");
    conversationTopicWireFrameSchema.parse(initial);
  } finally {
    await link.stop();
    publisher.dispose();
    await server.close();
    await engine.dispose();
  }
});

test("link: unknown method returns the documented error message", async () => {
  const { engine, publisher, registry } = harness();
  const resultPromise = { resolve: null as ((value: Record<string, unknown>) => void) | null };
  const result = new Promise<Record<string, unknown>>((resolve) => {
    resultPromise.resolve = resolve;
  });

  const server = await startFakeServer(async (socket) => {
    const reader = frameReader(socket);
    await reader.next();
    socket.send(JSON.stringify({ t: "welcome" }));
    socket.send(
      JSON.stringify({
        t: "call",
        id: "unknown-1",
        channel: "nex-agent",
        method: "doesNotExist",
        arg: [{}],
      }),
    );
    const frame = await reader.next();
    resultPromise.resolve?.(frame);
  });

  const link = startLink(server.url, registry);
  try {
    const answer = await result;
    assert.equal(answer["ok"], false);
    assert.deepEqual(answer["error"], {
      message: "Unknown method nex-agent.doesNotExist",
      name: "UnknownMethod",
    });
  } finally {
    await link.stop();
    publisher.dispose();
    await server.close();
    await engine.dispose();
  }
});

test("link: sessions-index subscribe pushes an initial frame", async () => {
  const { engine, publisher, registry } = harness();
  const pushPromise = { resolve: null as ((value: Record<string, unknown>) => void) | null };
  const pushed = new Promise<Record<string, unknown>>((resolve) => {
    pushPromise.resolve = resolve;
  });

  const server = await startFakeServer(async (socket) => {
    const reader = frameReader(socket);
    await reader.next();
    socket.send(JSON.stringify({ t: "welcome" }));
    socket.send(
      JSON.stringify({
        t: "listen",
        id: "listen-index",
        channel: "nex-agent",
        event: "onDynamicSessionsIndexFrame",
        arg: [{ workspacePath: WORKSPACE }],
      }),
    );
    socket.send(
      JSON.stringify({
        t: "call",
        id: "si-1",
        channel: "nex-agent",
        method: "subscribeSessionsIndexV4",
        arg: [{ workspacePath: WORKSPACE }],
      }),
    );
    for (;;) {
      const frame = await reader.next();
      if (frame["t"] === "event") {
        pushPromise.resolve?.(frame);
        return;
      }
    }
  });

  const link = startLink(server.url, registry);
  try {
    const frame = await pushed;
    const value = frame["value"];
    sessionsIndexTopicWireFrameSchema.parse(value);
    assert.equal((value as { deliveryKind: string }).deliveryKind, "initial");
  } finally {
    await link.stop();
    publisher.dispose();
    await server.close();
    await engine.dispose();
  }
});
