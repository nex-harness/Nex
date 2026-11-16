import { hostname } from "node:os";
import { join } from "node:path";
import { createLegacyServerCredentialCipher } from "@nex/shared/credential-security";
import {
  createServerHostServices,
  disposeServiceResourcesAndWait,
  materializeNexBuiltinProviderConfig,
  getAppConfigDir,
  setDataBaseDir,
  NEX_BUILTIN_PROVIDER_CONFIG_FILE_ENV,
  createTopicResourcePeers,
} from "@nex/services/node";
import { INexAgentService } from "@nex/services";
import { NEX_APP_VERSION_ENV, NEX_VERSION } from "@nex/shared";
import { createCoreHttpServer } from "./http.js";
import { createIngressToken } from "./ingressToken.js";
import { resolveServeOptions } from "./serveConfig.js";
import { assertWebRoot } from "./staticWeb.js";
import { resolveWebAccessToken } from "./webAccessToken.js";
import { installParentDisconnectHandler } from "./parentDisconnect.js";
import { resolveCoreServerId } from "./serverIdentity.js";
import { createTaskActivityTracker } from "./taskActivityTracker.js";
import {
  CORE_HEARTBEAT_INTERVAL_MS,
  SERVER_CREDENTIAL_FORMAT,
  SERVER_CREDENTIAL_FORMAT_ENV,
} from "../contracts.js";
import { resolveServerLayout } from "../runtime/paths.js";

declare const __NEX_BUILTIN_PROVIDER_CONFIG_JSON__: string | undefined;

export async function runServerCore(generation: number): Promise<void> {
  // Bug 原因：Agent 的模型请求头从 NEX_APP_VERSION 读 App 版本，缺失时回退到 CLI 包版本
  // （0.17.0）。本地由 Desktop main 注入；远端 Core 由 SSH/WSL/Docker exec → Supervisor fork 拉起，
  // 整条链都没有这个 env，远端请求于是报 0.17.0（按客户端版本拦截的网关会拒绝）。升级时旧 Supervisor
  // 会拉起新 release 的 Core，所以只能由 Core 用自身编译期版本写入，Agent spawn 时继承。
  process.env[NEX_APP_VERSION_ENV] = NEX_VERSION;
  let shutdown: ((reason: string) => Promise<void>) | undefined;
  let parentDisconnected = false;
  let disposeParentDisconnectHandler = (): void => undefined;
  disposeParentDisconnectHandler = installParentDisconnectHandler(() => {
    if (shutdown) void shutdown("parent-disconnected");
    else parentDisconnected = true;
  });
  const layout = resolveServerLayout(process.env.NEX_SERVER_ROOT?.trim());
  // Bug 原因：自定义 --server-root 是安装目录，不等于应用数据 base；server layout 为隔离
  // 会把 dataBaseDir 设为 serverRoot。若 Core 再写回 env，task DB 会被读成 <root>/.nex/v2，
  // 与 Supervisor 传入的真实应用数据根分裂。只采用 Supervisor 已确认的 base，不从安装目录猜测。
  const dataBaseDir = process.env.NEX_DATA_BASE_DIR?.trim() || layout.dataBaseDir;
  setDataBaseDir(dataBaseDir);
  process.env.NEX_DATA_BASE_DIR = dataBaseDir;
  const topicResourcePeers = createTopicResourcePeers();
  const explicitNexBuiltinProviderConfigFilePath =
    process.env[NEX_BUILTIN_PROVIDER_CONFIG_FILE_ENV]?.trim();
  // Built-in Config 物化到所属 Environment 配置根（~/.nex/v2）而不是 release 自带的 runtime 目录：
  // release 目录是不可变的，同一 releaseId 重装时 assertExistingReleaseMatches 会重算整棵树的
  // 内容哈希，多出的物化文件会让校验失败。
  const nexBuiltinProviderConfigFilePath = explicitNexBuiltinProviderConfigFilePath
    ? explicitNexBuiltinProviderConfigFilePath
    : typeof __NEX_BUILTIN_PROVIDER_CONFIG_JSON__ === "string"
      ? await materializeNexBuiltinProviderConfig({
          environmentConfigRoot: getAppConfigDir(),
          content: __NEX_BUILTIN_PROVIDER_CONFIG_JSON__,
        })
      : undefined;
  if (!nexBuiltinProviderConfigFilePath) {
    throw new Error(
      `当前构建未嵌入 Nex Built-in Provider Config，且未设置 ${NEX_BUILTIN_PROVIDER_CONFIG_FILE_ENV}`,
    );
  }
  const sourceServerId = (await resolveCoreServerId()) ?? hostname() ?? "nex-server";
  // 旧 Supervisor 会自行回滚，候选 Core 在 owner 换代前必须保持旧格式可读。
  const legacyUpdateOwner =
    process.env[SERVER_CREDENTIAL_FORMAT_ENV] !== String(SERVER_CREDENTIAL_FORMAT);
  const credentialCipherProvider = legacyUpdateOwner
    ? await createLegacyServerCredentialCipher({
        filePath: join(getAppConfigDir(), "credentials.json"),
      })
    : undefined;
  const { services, workspaceIdentityMapper, migrationFailedPathCount } =
    await createServerHostServices({
      sourceServerId,
      nexBuiltinProviderConfigFilePath,
      providerProvisioningTargetEnabled: true,
      credentialCipherProvider,
      topicResourceRelayChannel: (request) => topicResourcePeers.getChannel(request),
    });
  const taskActivityTracker = createTaskActivityTracker(services.getOptional(INexAgentService));
  const ingressToken = await createIngressToken(layout);
  // 监听/托管参数来自 serve-config.json 与环境变量（docs/specs/server-web-login.md）；
  // 不带任何参数时保持回环 + 动态端口 + 仅 ingress token 的原行为。
  const serveOptions = await resolveServeOptions(layout);
  const webLogin = serveOptions.webLoginEnabled
    ? { token: (await resolveWebAccessToken(layout)).token, sessionsFilePath: layout.webSessionsFile }
    : undefined;
  const webRoot = serveOptions.webRoot ? await assertWebRoot(serveOptions.webRoot) : undefined;
  const http = await createCoreHttpServer(services, {
    ...(serveOptions.host ? { host: serveOptions.host } : {}),
    ...(serveOptions.port !== undefined ? { port: serveOptions.port } : {}),
    ...(webLogin ? { webLogin } : {}),
    ...(webRoot ? { webRoot } : {}),
    serverId: sourceServerId,
    ingressToken,
    migrationFailedPathCount,
    workspaceIdentityMapper,
    topicResourcePeers,
  });
  const send = (message: unknown): Promise<void> => {
    if (typeof process.send !== "function" || process.connected === false) return Promise.resolve();
    return new Promise((resolve) => {
      try {
        process.send?.(message, () => resolve());
      } catch {
        // 父进程断连后的最后一条生命周期消息不应阻塞资源释放。
        resolve();
      }
    });
  };
  // ready.version 的语义是 Core 版本；不能误发 Node runtime 版本常量
  // （22.16.0），否则消费方读取会拿到错误值。
  await send({
    type: "ready",
    host: http.host,
    port: http.port,
    version: NEX_VERSION,
    ...(process.env.NEX_SERVER_RELEASE_ID?.trim()
      ? { releaseId: process.env.NEX_SERVER_RELEASE_ID.trim() }
      : {}),
    generation,
  });
  let shutdownStarted = false;
  let lastRunningTaskCount = taskActivityTracker.readRunningTaskCount();
  const activitySubscription = taskActivityTracker.onDidChangeRunningTaskCount(
    (runningTaskCount) => {
      lastRunningTaskCount = runningTaskCount;
      void send({
        type: "task-activity",
        runningTasks: taskActivityTracker.readRunningTasks(),
        runningTaskCount,
      });
    },
  );
  let heartbeatInFlight: Promise<void> | undefined;
  const heartbeat = setInterval(() => {
    if (heartbeatInFlight) return;
    heartbeatInFlight = Promise.resolve(taskActivityTracker.readRunningTaskCount())
      .then((runningTaskCount) => {
        if (runningTaskCount !== lastRunningTaskCount) {
          lastRunningTaskCount = runningTaskCount;
          void send({
            type: "task-activity",
            runningTasks: taskActivityTracker.readRunningTasks(),
            runningTaskCount,
          });
        }
        void send({
          type: "heartbeat",
          runningTasks: taskActivityTracker.readRunningTasks(),
          at: Date.now(),
          runningTaskCount,
        });
      })
      .catch(() => {
        void send({
          type: "heartbeat",
          runningTasks: taskActivityTracker.readRunningTasks(),
          at: Date.now(),
          runningTaskCount: lastRunningTaskCount,
        });
      })
      .finally(() => {
        heartbeatInFlight = undefined;
      });
  }, CORE_HEARTBEAT_INTERVAL_MS);
  shutdown = async (reason: string): Promise<void> => {
    if (shutdownStarted) return;
    shutdownStarted = true;
    disposeParentDisconnectHandler();
    clearInterval(heartbeat);
    activitySubscription.dispose();
    taskActivityTracker.dispose();
    await http.close().catch(() => undefined);
    await disposeServiceResourcesAndWait(services).catch(() => undefined);
    await send({ type: "shutdown-ack" });
    await send({ type: "exit", reason });
    try {
      process.disconnect?.();
    } catch {
      // 父进程已断连时 disconnect 可能报告 IPC_CHANNEL_CLOSED；不影响资源已释放后的退出。
    }
    // 仅设置 exitCode 无法关闭 Agent/SQLite 等仍持有的事件循环；Supervisor 的有界停止
    // 会因此等待到超时。资源释放完成后显式退出，确保 stop/restart/uninstall 真正收口。
    process.exit(0);
  };
  if (parentDisconnected) void shutdown("parent-disconnected");
  process.on("message", (message: unknown) => {
    if (
      typeof message === "object" &&
      message !== null &&
      "command" in message &&
      message.command === "shutdown"
    ) {
      void shutdown("requested");
    }
  });
  process.once("SIGTERM", () => void shutdown("SIGTERM"));
  process.once("SIGINT", () => void shutdown("SIGINT"));
}
