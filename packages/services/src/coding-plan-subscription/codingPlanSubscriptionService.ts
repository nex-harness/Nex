import type { ApiClient, DynamicWorkflowClientConfig } from "@nex/shared";
import {
  applyDynamicWorkflowUserMode,
  buildRuntimeNexApiUrl,
  createDynamicWorkflowClientConfig,
  DEFAULT_DYNAMIC_WORKFLOW_MODE,
  NEX_DYNAMIC_WORKFLOW_MODE_ENV,
  NEX_VERSION,
  normalizeDynamicWorkflowMode,
  resolveDynamicWorkflowClientConfig,
} from "@nex/shared";
import { readApiJson } from "../providers/api/apiJson.js";
import { createServiceLogger } from "../logger/serviceLogger.js";
import type { ICodingPlanSubscriptionService } from "./codingPlanSubscription.js";

const NEX_CLIENT_CONFIG_API_PREFIX = "/api/v1/client/configs";
const REQUEST_TIMEOUT_MS = 15_000;
const CLIENT_CONFIG_CACHE_TTL_MS = 60 * 60 * 1000;
const log = createServiceLogger("codingPlanSubscription");

interface NexClientConfigEnvelope {
  data?: {
    configs?: {
      // mode 的取值域由 shared 的 normalizeDynamicWorkflowMode 裁决，这里不在类型层假设服务端合法。
      dynamicWorkflow?: {
        mode?: unknown;
      } | null;
    } | null;
  } | null;
}

export function createCodingPlanSubscriptionService(dependencies: {
  apiClient: ApiClient;
  resolveDynamicWorkflowUserMode?: () => Promise<unknown>;
}): ICodingPlanSubscriptionService {
  let snapshot: NexClientConfigEnvelope | null = null;
  let snapshotExpiresAt = 0;
  let pending: Promise<NexClientConfigEnvelope> | null = null;

  async function getClientConfigs(): Promise<NexClientConfigEnvelope> {
    if (snapshot && snapshotExpiresAt > Date.now()) {
      return snapshot;
    }
    if (pending) {
      return await pending;
    }
    // client/configs 和其他 Nex 平台接口共享运行时 endpoint；E2E 通过 NEX_BASE_URL 指向本地 mock。
    const url = new URL(buildRuntimeNexApiUrl(process.env, NEX_CLIENT_CONFIG_API_PREFIX));
    url.searchParams.set("app_version", NEX_VERSION);
    url.searchParams.set("platform", `${process.platform}-${process.arch}`);
    pending = readApiJson<NexClientConfigEnvelope>(dependencies.apiClient, url, {
      method: "GET",
      timeoutMs: REQUEST_TIMEOUT_MS,
    });
    try {
      const payload = await pending;
      snapshot = payload;
      snapshotExpiresAt = Date.now() + CLIENT_CONFIG_CACHE_TTL_MS;
      return payload;
    } finally {
      pending = null;
    }
  }

  return {
    /**
     * 三条边界：
     *   1. 本地覆盖（NEX_DYNAMIC_WORKFLOW_MODE）在任何网络动作之前裁决，命中即返回；
     *   2. forceRefresh 清掉快照后重拉（灰度翻转最长 1h 不可见）；
     *   3. 请求失败 fail-closed：返回 default（disabled）并 warn，绝不把异常抛给调用方——
     *      调用方在 session create/client 就绪路径上，灰度读失败不能阻断普通聊天。
     */
    async getDynamicWorkflowClientConfig(options): Promise<DynamicWorkflowClientConfig> {
      if (normalizeDynamicWorkflowMode(process.env[NEX_DYNAMIC_WORKFLOW_MODE_ENV])) {
        return resolveDynamicWorkflowClientConfig({ remote: undefined, env: process.env });
      }
      if (options?.forceRefresh) {
        snapshot = null;
        snapshotExpiresAt = 0;
      }
      try {
        const payload = await getClientConfigs();
        const offer = resolveDynamicWorkflowClientConfig({
          remote: payload.data?.configs?.dynamicWorkflow,
          env: process.env,
        });
        // 用户在设置里的明确选择取代服务端「提供」的模式（launch.md「The user's choice」）。
        const resolveUserMode = dependencies.resolveDynamicWorkflowUserMode;
        return resolveUserMode ? applyDynamicWorkflowUserMode(offer, await resolveUserMode()) : offer;
      } catch (error) {
        log.warn(undefined, "动态工作流灰度配置读取失败，按关闭处理", {
          errorMessage: error instanceof Error ? error.message : String(error),
        });
        return createDynamicWorkflowClientConfig(DEFAULT_DYNAMIC_WORKFLOW_MODE, "default");
      }
    },
  };
}
