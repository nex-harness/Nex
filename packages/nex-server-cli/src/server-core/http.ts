import { randomUUID, timingSafeEqual } from "node:crypto";
import { hostname } from "node:os";
import { resolveServerOsUser } from "@nex/shared/node";
import { serve } from "@hono/node-server";
import { createNodeWebSocket } from "@hono/node-ws";
import { Hono, type Context } from "hono";
import type { WebSocket } from "ws";
import type { WebSocketServer } from "ws";
import {
  Emitter,
  Event,
  VSBuffer,
  SocketProtocol,
  ChannelServer,
  ChannelClient,
  LoggingChannelServer,
  type ISocket,
  type IChannel,
  type CancellationToken,
} from "@nex/rpc";
import {
  createNexAgentConnectionScope,
  IProviderProvisioningTargetService,
  INexAgentService,
  IProjectService,
  ServiceCollection,
} from "@nex/services";
import {
  createServiceLogger,
  type TopicResourcePeers,
  SourceServerWorkspaceIdentityChannelServer,
  type SourceServerWorkspaceIdentityMapper,
} from "@nex/services/node";
import {
  SERVER_REMOTE_PROTOCOL_VERSION,
  TOPIC_RESOURCE_RELAY_CHANNEL,
  serverRemoteInfoOutputSchema,
  buildServerRemoteWorkspaceIdentity,
  NEX_COMPATIBILITY_MANIFEST,
  NEX_RPC_HOST_CAPABILITY_HEADER,
  NEX_VERSION,
  type ServerRemoteInfo,
} from "@nex/shared";
import { createHostCapabilityStore, type HostCapabilityStore } from "./hostCapability.js";
import { isLoopbackHost } from "./serveConfig.js";
import { createStaticWebHandler } from "./staticWeb.js";
import { createWebAuth } from "./webAuth.js";

export interface CoreHttpServer {
  host: string;
  port: number;
  close: () => Promise<void>;
}

const WEBSOCKET_DRAIN_TIMEOUT_MS = 250;
const log = createServiceLogger("server-core");

async function closeWebSocketServer(wss: WebSocketServer): Promise<void> {
  for (const client of wss.clients) {
    // HTTP server.close() 不会收敛已经 upgrade 的 WebSocket，活跃 desktop
    // continuous 连接会让 Core 的 shutdown ack 永远发不出去。先发 close frame 给正常
    // 客户端一个短暂排空窗口，再 terminate 兜底，保证 Supervisor 能在预算内释放资源。
    client.close(1001, "Server shutting down");
  }
  const deadline = Date.now() + WEBSOCKET_DRAIN_TIMEOUT_MS;
  while (wss.clients.size > 0 && Date.now() < deadline) {
    await new Promise<void>((resolve) => setTimeout(resolve, 10));
  }
  for (const client of wss.clients) client.terminate();
  await new Promise<void>((resolve, reject) => {
    wss.close((error?: Error) => (error ? reject(error) : resolve()));
  });
}

function wrapWebSocket(ws: WebSocket): ISocket {
  const data = new Emitter<VSBuffer>();
  const close = new Emitter<void>();
  ws.on("message", (raw) =>
    data.fire(VSBuffer.wrap(Buffer.isBuffer(raw) ? raw : Buffer.from(raw as ArrayBuffer))),
  );
  ws.on("close", () => close.fire());
  ws.on("error", () => close.fire());
  return {
    onData: data.event,
    onClose: close.event,
    onEnd: close.event,
    write(buffer) {
      if (ws.readyState === ws.OPEN) ws.send(buffer.buffer);
    },
    end() {
      ws.close();
    },
    drain() {
      return Promise.resolve();
    },
    dispose() {
      ws.close();
    },
  };
}

function exposeWebSocket(
  ws: WebSocket,
  services: ServiceCollection,
  clientMode: "desktop-continuous" | "web-remote-replayable",
  workspaceIdentityMapper?: SourceServerWorkspaceIdentityMapper,
  resourcePeers?: TopicResourcePeers,
): void {
  const socket = wrapWebSocket(ws);
  const protocol = new SocketProtocol(socket);
  const rawServer = new ChannelServer(protocol, "server");
  const loggedServer = new LoggingChannelServer(rawServer, (...args) =>
    log.debug(undefined, ...args),
  );
  const server = workspaceIdentityMapper
    ? new SourceServerWorkspaceIdentityChannelServer(loggedServer, workspaceIdentityMapper)
    : loggedServer;
  const agentService = services.getOptional(INexAgentService);
  const reverseClient =
    clientMode === "desktop-continuous" && resourcePeers ? new ChannelClient(protocol) : undefined;
  const rawResourceChannel = reverseClient?.getChannel(TOPIC_RESOURCE_RELAY_CHANNEL);
  // Source 内部 route 已 canonicalize；反向请求必须还原 Desktop 的 stable identity，
  // 否则同一 attachment 会被 registry 判成跨工作区请求。
  const resourceChannel: IChannel | undefined =
    rawResourceChannel && workspaceIdentityMapper
      ? {
          async call<T>(command: string, args?: unknown, token?: CancellationToken) {
            const result = await rawResourceChannel.call<T>(
              command,
              workspaceIdentityMapper.mapOutgoing(args),
              token,
            );
            return workspaceIdentityMapper.mapIncoming(result);
          },
          listen<T>(event: string, args?: unknown) {
            return Event.map(
              rawResourceChannel.listen<T>(event, workspaceIdentityMapper.mapOutgoing(args)),
              (value) => workspaceIdentityMapper.mapIncoming(value),
            );
          },
        }
      : rawResourceChannel;
  const peer = resourceChannel ? resourcePeers?.attach(resourceChannel) : undefined;
  const connectionContext = {
    connectionId: `server-core-ws-${randomUUID()}`,
    clientMode,
    role:
      clientMode === "desktop-continuous"
        ? ("trusted-host-relay" as const)
        : ("terminal-client" as const),
  };
  const scope = agentService
    ? createNexAgentConnectionScope(
        peer ? peer.wrapAgent(agentService) : agentService,
        connectionContext,
      )
    : undefined;
  const overrides = new Map<string, unknown>();
  if (scope) {
    overrides.set(INexAgentService.channelName, scope.service);
  }
  // Provisioning envelope 携带跨 Environment 的 Personal 配置与凭据，只允许受信 Desktop Host
  // 通过 /ws/host 下发；/ws 上的 Web / 手机 replayable 客户端即使知道频道名，也只能拿到抛错 stub，
  // 不能把凭据写进本 Source Server。
  if (
    clientMode !== "desktop-continuous" &&
    services.getOptional(IProviderProvisioningTargetService)
  ) {
    overrides.set(IProviderProvisioningTargetService.channelName, {
      apply: async () => {
        throw new Error("Provider Provisioning 仅支持受信 Desktop Host");
      },
    });
  }
  services.exposeOnChannelServer(server, overrides);
  socket.onClose(() => {
    peer?.dispose();
    reverseClient?.dispose();
    void scope?.dispose();
    rawServer.dispose();
  });
}

export async function createCoreHttpServer(
  services: ServiceCollection,
  options: {
    host?: string;
    port?: number;
    serverId?: string;
    ingressToken?: string;
    migrationFailedPathCount?: number;
    hostCapabilityStore?: HostCapabilityStore;
    workspaceIdentityMapper?: SourceServerWorkspaceIdentityMapper;
    topicResourcePeers?: TopicResourcePeers;
    /** 浏览器登录（docs/specs/server-web-login.md）；缺省时保持仅 ingress token 的旧行为。 */
    webLogin?: { token: string; sessionsFilePath: string };
    /** 已校验含 index.html 的 Web UI 构建目录；提供时必须同时提供 webLogin。 */
    webRoot?: string;
  } = {},
): Promise<CoreHttpServer> {
  const app = new Hono();
  const { injectWebSocket, upgradeWebSocket, wss } = createNodeWebSocket({
    app,
  });
  const host = options.host ?? "127.0.0.1";
  if (!isLoopbackHost(host) && !options.webLogin) {
    // 对外监听必须有浏览器登录；ingress token 只适合本机/SSH 隧道/可信反代这类程序化入口。
    throw new Error(
      `Non-loopback host ${host} requires authentication before the server can listen`,
    );
  }
  if (options.webRoot && !options.webLogin) {
    throw new Error("Hosting the web UI requires web login to be enabled");
  }
  const ingressToken = options.ingressToken?.trim();
  const serverId = options.serverId ?? hostname() ?? "nex-server";
  const projects = await services.get(IProjectService).readProjects();
  const projectWorkspaces = projects
    .filter((project) => !project.deleted)
    .map((project) => {
      const path =
        project.roots.find((root) => root.isPrimary)?.rootPath ?? project.roots[0]?.rootPath;
      if (!path) return undefined;
      return {
        path,
        label: project.name,
        workspaceIdentity: buildServerRemoteWorkspaceIdentity({
          serverId,
          workspacePath: path,
        }),
      };
    })
    .filter((workspace) => workspace !== undefined);
  // Bug 原因：Core 自 R2 起固定返回空 workspaces；升级已经恢复的项目明明在 SQLite 中，
  // Web 却没有初始目录，Root 又未允许兜底创建，最终整页空白。这里复用项目服务声明可打开目录；
  // 不把它们反向导入桌面 settings，避免历史任务恢复出的项目复活已移除的入口。
  const fallbackPath = process.env.NEX_SERVER_WORKSPACE?.trim() || process.cwd();
  const workspaces = projectWorkspaces.length
    ? projectWorkspaces
    : [
        {
          path: fallbackPath,
          workspaceIdentity: buildServerRemoteWorkspaceIdentity({
            serverId,
            workspacePath: fallbackPath,
          }),
        },
      ];
  const info: ServerRemoteInfo = serverRemoteInfoOutputSchema.parse({
    serverId,
    version: NEX_VERSION,
    protocolVersion: SERVER_REMOTE_PROTOCOL_VERSION,
    authRequired: Boolean(ingressToken),
    workspaces,
    compatibility: NEX_COMPATIBILITY_MANIFEST,
    migration: { failedPathCount: options.migrationFailedPathCount ?? 0 },
    identity: { osUser: resolveServerOsUser(), hostname: hostname() },
    capabilities: {
      desktopContinuous: true,
      websocketRpc: true,
      virtualProjects: true,
      processResourceTelemetry: true,
      remoteConversationTelemetry: true,
    },
  });
  // 裸 Set 无法落实 expiresAt，未消费的 capability 会一直有效并持续累积。
  // 使用与 packages/server 兼容的 TTL 一次性 store，使有效期和消费语义与返回信息一致。
  const capabilities = options.hostCapabilityStore ?? createHostCapabilityStore();
  const hasValidIngressToken = (context: Context): boolean => {
    if (!ingressToken) return false;
    const authorization = context.req.header("authorization");
    const bearerToken = authorization?.startsWith("Bearer ")
      ? authorization.slice("Bearer ".length).trim()
      : undefined;
    const queryToken = new URL(context.req.url).searchParams.get("token")?.trim();
    const candidate = bearerToken || queryToken;
    if (!candidate) return false;
    const expected = Buffer.from(ingressToken);
    const received = Buffer.from(candidate);
    return expected.length === received.length && timingSafeEqual(expected, received);
  };
  const unauthenticatedServerInfo = () => ({
    version: info.version,
    protocolVersion: info.protocolVersion,
    authRequired: true,
    compatibility: info.compatibility,
  });
  const webAuth = options.webLogin
    ? createWebAuth({
        token: options.webLogin.token,
        sessionsFilePath: options.webLogin.sessionsFilePath,
        isIngressAuthenticated: hasValidIngressToken,
        unauthenticatedServerInfo,
        logger: { warn: (...args) => log.warn(undefined, ...args) },
      })
    : undefined;
  if (webAuth) {
    // 页面与 /api/auth/* 不鉴权；/api/*、/ws* 需要会话 Cookie 或 ingress token。
    app.use("*", webAuth.middleware());
    webAuth.mountRoutes(app);
  } else {
    app.use("*", async (context, next) => {
      if (!ingressToken || hasValidIngressToken(context)) {
        await next();
        return;
      }
      if (context.req.path === "/api/server-info") {
        return context.json(unauthenticatedServerInfo());
      }
      return context.json({ error: "Ingress authentication required" }, 401);
    });
  }
  app.get("/api/server-info", (context) => context.json(info));
  app.get(
    "/ws",
    upgradeWebSocket(() => ({
      onOpen(_event, socket) {
        exposeWebSocket(
          socket.raw as WebSocket,
          services,
          "web-remote-replayable",
          options.workspaceIdentityMapper,
        );
      },
    })),
  );
  app.use("/ws/host", async (context, next) => {
    const capability = context.req.header(NEX_RPC_HOST_CAPABILITY_HEADER);
    if (!capabilities.consume(capability)) {
      return context.json({ error: "Invalid or expired host capability" }, 401);
    }
    await next();
  });
  app.get(
    "/ws/host",
    upgradeWebSocket(() => ({
      onOpen(_event, socket) {
        exposeWebSocket(
          socket.raw as WebSocket,
          services,
          "desktop-continuous",
          options.workspaceIdentityMapper,
          options.topicResourcePeers,
        );
      },
    })),
  );
  app.post("/api/rpc-host-capability", (context) => context.json(capabilities.issue()));
  if (options.webRoot) app.get("*", createStaticWebHandler(options.webRoot));
  let resolveListening: (value: { port: number }) => void = () => undefined;
  const listening = new Promise<{ port: number }>((resolve) => {
    resolveListening = resolve;
  });
  const server = serve({ fetch: app.fetch, hostname: host, port: options.port ?? 0 }, () => {
    const address = server.address();
    resolveListening({
      port: typeof address === "object" && address ? address.port : (options.port ?? 0),
    });
  });
  injectWebSocket(server);
  const { port } = await listening;
  return {
    host,
    port,
    close: async () => {
      await closeWebSocketServer(wss);
      await new Promise<void>((resolve, reject) =>
        server.close((error?: Error) => (error ? reject(error) : resolve())),
      );
    },
  };
}
