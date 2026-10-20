import { test } from "node:test";
import assert from "node:assert/strict";
import { WebSocket } from "ws";
import {
  ChannelClient,
  Emitter,
  ProxyChannel,
  SocketProtocol,
  VSBuffer,
  type ISocket,
} from "@nex/rpc";
import { commandAckSchema, conversationTopicWireFrameSchema } from "@nex/shared/nex-protocol-v4";

/**
 * Opt-in E2E against a real `next-server` + runner:
 *
 *   NEX_E2E_WS_URL=ws://127.0.0.1:3399/ws \
 *   NEX_E2E_COOKIE=nex_session=... \
 *   NEX_E2E_WORKSPACE=/tmp/workspace \
 *   RUN_NEXT_E2E=1 pnpm --filter @nex/next-runner test
 *
 * It exercises the exact browser transport (Channel RPC over WS + ProxyChannel), so a
 * runner-side wire regression fails here instead of in the web client.
 */
const enabled = process.env["RUN_NEXT_E2E"] === "1";
const wsUrl = process.env["NEX_E2E_WS_URL"] ?? "";
const cookie = process.env["NEX_E2E_COOKIE"] ?? "";
const workspacePath = process.env["NEX_E2E_WORKSPACE"] ?? "";

interface SocketLike {
  on(event: string, listener: (...args: unknown[]) => void): void;
  send(data: Uint8Array): void;
  close(): void;
  readonly readyState: number;
}

function wrapSocket(socket: SocketLike): ISocket {
  const onData = new Emitter<VSBuffer>();
  const onClose = new Emitter<void>();
  const onEnd = new Emitter<void>();
  socket.on("message", (data) => onData.fire(VSBuffer.wrap(new Uint8Array(data as ArrayBuffer))));
  socket.on("close", () => {
    onClose.fire();
    onEnd.fire();
  });
  socket.on("error", () => {
    onClose.fire();
    onEnd.fire();
  });
  return {
    onData: onData.event,
    onClose: onClose.event,
    onEnd: onEnd.event,
    write(buffer) {
      socket.send(buffer.buffer as Uint8Array);
    },
    end() {
      socket.close();
    },
    drain() {
      return Promise.resolve();
    },
    dispose() {
      socket.close();
    },
  };
}

async function connect(): Promise<{ agent: unknown; close(): void }> {
  const socket = new WebSocket(wsUrl, { headers: { Cookie: cookie } });
  // Wrap before `open`: the server sends its Initialize frame immediately after the
  // upgrade, and a listener attached after `open` would drop it and hang every call.
  const protocol = new SocketProtocol(wrapSocket(socket as unknown as SocketLike));
  const client = new ChannelClient(protocol);
  await new Promise<void>((resolve, reject) => {
    socket.once("open", () => resolve());
    socket.once("error", (error) => reject(error));
  });
  return {
    agent: ProxyChannel.toService(client.getChannel("nex-agent")),
    close: () => socket.close(),
  };
}

function waitFor<T>(check: () => T | undefined, label: string, timeoutMs = 30_000): Promise<T> {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + timeoutMs;
    const timer = setInterval(() => {
      const value = check();
      if (value !== undefined) {
        clearInterval(timer);
        resolve(value);
      } else if (Date.now() > deadline) {
        clearInterval(timer);
        reject(new Error(`timed out waiting for ${label}`));
      }
    }, 25);
  });
}

test(
  "next-server e2e: command → subscription → streamed snapshot",
  {
    skip: enabled ? false : "set RUN_NEXT_E2E=1 with NEX_E2E_* env vars",
    timeout: 240_000,
  },
  async () => {
    assert.ok(wsUrl && cookie && workspacePath, "NEX_E2E_WS_URL/COOKIE/WORKSPACE are required");
    const connection = await connect();
    // Unique ids: a re-run against the same runner must not hit its idempotency cache.
    const runId = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    const agent = connection.agent as {
      onDynamicConversationFrame(arg: unknown): (listener: (frame: unknown) => void) => {
        dispose(): void;
      };
      subscribeConversationV4(arg: unknown): Promise<{
        ack: { subscriptionId: string; mode: string; logEpoch: string };
      }>;
      sendConversationCommandV4(arg: unknown): Promise<unknown>;
      onDynamicSessionsIndexFrame(arg: unknown): (listener: (frame: unknown) => void) => {
        dispose(): void;
      };
      subscribeSessionsIndexV4(arg: unknown): Promise<{ ack: { subscriptionId: string } }>;
    };

    const frames: unknown[] = [];
    const dispose = agent.onDynamicConversationFrame({ workspacePath })((frame) =>
      frames.push(frame),
    );
    const indexFrames: unknown[] = [];
    const disposeIndex = agent.onDynamicSessionsIndexFrame({ workspacePath })((frame) =>
      indexFrames.push(frame),
    );
    try {
      const created = commandAckSchema.parse(
        await agent.sendConversationCommandV4({
          workspacePath,
          envelope: {
            commandId: `e2e-create-${runId}`,
            clientId: "e2e-client",
            sessionId: null,
            type: "createSession",
            payload: { workspaceId: workspacePath },
            issuedAt: Date.now(),
          },
        }),
      );
      assert.equal(created.status, "accepted");
      const sessionId = created.result?.type === "createSession" ? created.result.sessionId : "";
      assert.ok(sessionId);

      const subscribed = await agent.subscribeConversationV4({ workspacePath, sessionId });
      assert.equal(subscribed.ack.mode, "snapshot");

      const initial = await waitFor(
        () =>
          frames
            .map((frame) => frame as { deliveryKind?: string })
            .find((frame) => frame.deliveryKind === "initial"),
        "initial frame",
      );
      conversationTopicWireFrameSchema.parse(initial);

      const sent = commandAckSchema.parse(
        await agent.sendConversationCommandV4({
          workspacePath,
          envelope: {
            commandId: `e2e-send-${runId}`,
            clientId: "e2e-client",
            sessionId,
            type: "sendText",
            payload: { text: "Reply with exactly: pong" },
            issuedAt: Date.now(),
          },
        }),
      );
      assert.equal(sent.status, "accepted");

      const completed = await waitFor(
        () =>
          frames
            .map((frame) => frame as { frame?: { payload?: { snapshot?: ConversationLike } } })
            .find((frame) => frame.frame?.payload?.snapshot?.control?.phase === "completedSuccess"),
        "completedSuccess snapshot",
        180_000,
      );
      const wire = conversationTopicWireFrameSchema.parse(completed);
      assert.equal(wire.kind, "complete");
      const rows =
        wire.kind === "complete" && wire.frame.payload.kind === "snapshot"
          ? wire.frame.payload.snapshot.rows.window
          : [];
      const text = rows.flatMap((row) => (row.kind === "assistantText" ? [row.text] : [])).join("");
      assert.match(text, /pong/iu);

      // Sidebar surface: sessions index must list the same session.
      await agent.subscribeSessionsIndexV4({ workspacePath });
      const indexFrame = await waitFor(
        () =>
          indexFrames
            .map(
              (frame) => frame as { frame?: { payload?: { snapshot?: { sessions?: unknown[] } } } },
            )
            .find((frame) => (frame.frame?.payload?.snapshot?.sessions?.length ?? 0) > 0),
        "sessions index snapshot",
      );
      const indexWire = indexFrame as {
        frame: { payload: { snapshot: { sessions: Array<{ sessionId: string }> } } };
      };
      assert.ok(
        indexWire.frame.payload.snapshot.sessions.some((item) => item.sessionId === sessionId),
      );
    } finally {
      dispose.dispose();
      disposeIndex.dispose();
      connection.close();
    }
  },
);

interface ConversationLike {
  control?: { phase?: string };
}
