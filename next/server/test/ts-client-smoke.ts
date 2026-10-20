/**
 * TS cross-check for the Go server.
 *
 * Uses the REAL @nex/client `connectViaWebSocket` (which builds the real
 * packages/rpc ChannelClient + SocketProtocol) against a running
 * nex-next-server, then calls `setting.get()` and
 * `nex-agent.helloConversationV4()`.
 *
 * Auth: the WS endpoint accepts `?token=` on protected paths (same rule as
 * packages/server/src/webAuth.ts), so the script does not need custom headers
 * on the WebSocket. It also performs the real login POST first to prove the
 * cookie flow, and prints the cookie attributes.
 *
 * Run from the repository root:
 *   PATH=<node-24-bin>:$PATH \
 *   node_modules/.bin/tsx next/server/test/ts-client-smoke.ts
 *
 * Env:
 *   NEX_SERVER_URL         default http://127.0.0.1:3030
 *   NEX_SERVER_AUTH_TOKEN  default browser-secret
 */
import { connectViaWebSocket } from "../../../packages/client/src/websocket.ts";

const serverURL = (process.env.NEX_SERVER_URL ?? "http://127.0.0.1:3030").replace(/\/+$/, "");
const token = process.env.NEX_SERVER_AUTH_TOKEN ?? "browser-secret";

function fail(message: string): never {
  console.error(`FAIL: ${message}`);
  process.exit(1);
}

async function main(): Promise<void> {
  const loginResponse = await fetch(`${serverURL}/api/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ token }),
  });
  const setCookie = loginResponse.headers.get("set-cookie") ?? "";
  console.log(`login: ${loginResponse.status} ${JSON.stringify(await loginResponse.json())}`);
  console.log(`login set-cookie: ${setCookie}`);
  if (loginResponse.status !== 200) {
    fail(`login returned ${loginResponse.status}`);
  }
  if (
    !setCookie.includes("nex_session=") ||
    !/HttpOnly/i.test(setCookie) ||
    !/SameSite=Lax/i.test(setCookie)
  ) {
    fail("login cookie attributes are wrong");
  }

  const sessionResponse = await fetch(`${serverURL}/api/auth/session`, {
    headers: { cookie: setCookie.split(";")[0] ?? "" },
  });
  const session = (await sessionResponse.json()) as {
    authRequired: boolean;
    authenticated: boolean;
  };
  console.log(`session: ${JSON.stringify(session)}`);
  if (!session.authRequired || !session.authenticated) {
    fail("session endpoint did not confirm the login");
  }

  const wsURL = `${serverURL.replace(/^http/, "ws")}/ws?token=${encodeURIComponent(token)}`;
  const services = await connectViaWebSocket(wsURL);
  console.log(`ws connected: ${wsURL}`);

  const settings = (await services.settingService.get()) as {
    locale?: string;
    recentProjects?: string[];
    nexInteractionBehavior?: string;
  };
  console.log(
    `setting.get: locale=${settings.locale} nexInteractionBehavior=${settings.nexInteractionBehavior} ` +
      `recentProjects=${JSON.stringify(settings.recentProjects)}`,
  );
  if (settings.locale !== "zh-CN") {
    fail(`expected the default locale zh-CN, got ${String(settings.locale)}`);
  }

  // Update round-trip proves the client's `[arg]` call envelope is unwrapped
  // by the local channel and persisted.
  await services.settingService.update({
    locale: "en-US",
    recentProjects: ["/smoke/user-project"],
  });
  const updated = (await services.settingService.get()) as {
    locale?: string;
    recentProjects?: string[];
  };
  console.log(
    `setting.update → get: locale=${updated.locale} recentProjects=${JSON.stringify(updated.recentProjects)}`,
  );
  if (updated.locale !== "en-US" || updated.recentProjects?.[0] !== "/smoke/user-project") {
    fail("setting.update did not persist through the real client");
  }

  // broadcast.send fans out to every connection's onMessage listener.
  const broadcastReceived = new Promise<unknown>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("broadcast onMessage timed out")), 3000);
    const disposable = services.broadcastService.onMessage((message) => {
      if (message.channel !== "smoke:test") return;
      clearTimeout(timer);
      disposable.dispose();
      resolve(message.payload);
    });
  });
  await services.broadcastService.send({ channel: "smoke:test", payload: { ok: true } });
  const payload = await broadcastReceived;
  console.log(`broadcast onMessage: ${JSON.stringify(payload)}`);
  if (JSON.stringify(payload) !== JSON.stringify({ ok: true })) {
    fail("broadcast payload mismatch");
  }

  // window-controller: the pinned section subscribes and expects a snapshot
  // frame notification after the ACK.
  const controllerFrame = new Promise<unknown>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("controller frame timed out")), 3000);
    const disposable = services.windowControllerService.onDynamicControllerFrame()((frame) => {
      clearTimeout(timer);
      disposable.dispose();
      resolve(frame);
    });
  });
  const controllerAck = await services.windowControllerService.subscribeControllerV4({
    topic: "controller/tasks-index",
    visibility: "foreground",
  });
  console.log(`window-controller.subscribeControllerV4: ${JSON.stringify(controllerAck)}`);
  const frame = (await controllerFrame) as {
    topic?: string;
    payload?: { kind?: string; snapshot?: { protocolVersion?: number } };
  };
  console.log(`window-controller frame: topic=${frame.topic} kind=${frame.payload?.kind}`);
  if (frame.topic !== "controller/tasks-index" || frame.payload?.kind !== "snapshot") {
    fail("controller snapshot frame is invalid");
  }

  // settings-sync must report the first-run prompt as handled, otherwise the
  // onboarding dialog opens on the first workspace.
  const firstRun = (await services.settingsSyncService.getFirstRunPromptState()) as {
    handled?: boolean;
  };
  console.log(`settings-sync.getFirstRunPromptState: ${JSON.stringify(firstRun)}`);
  if (firstRun.handled !== true) {
    fail("getFirstRunPromptState must return handled:true");
  }

  const hello = (await services.nexAgentService.helloConversationV4()) as {
    kind?: string;
    protocolVersion?: number;
    clientMode?: string;
    deliveryProfile?: string;
    connectionId?: string;
    capabilities?: Record<string, unknown>;
  };
  console.log(`nex-agent.helloConversationV4: ${JSON.stringify(hello)}`);
  if (hello.kind !== "hello" || hello.protocolVersion !== 3) {
    fail("helloConversationV4 did not return a valid v4 hello");
  }
  if (hello.clientMode !== "desktop-continuous" || hello.deliveryProfile !== "continuous") {
    fail("hello mode/profile mismatch");
  }

  // Unknown channel must reject immediately with the ChannelServer message
  // rather than waiting for the 1 s timeout.
  const startedAt = Date.now();
  try {
    await services.nexSessionService.readWorkspacePresentation({ workspacePath: "/nope" });
    fail("a call for an unknown workspace should reject");
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.log(`unknown workspace rejected in ${Date.now() - startedAt}ms: ${message}`);
    if (!message.includes("no runner connected for workspace")) {
      fail(`unexpected error for an unknown workspace: ${message}`);
    }
  }

  console.log("SMOKE OK");
  process.exit(0);
}

void main().catch((error: unknown) => {
  console.error("FAIL:", error);
  process.exit(1);
});
