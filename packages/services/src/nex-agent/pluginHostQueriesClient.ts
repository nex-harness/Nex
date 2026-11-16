// App 侧插件宿主查询：plugins/readUserOptions 与 plugins/mentionItems（plugin-host-queries 模块）。
// 规格：apps/nex-cli/docs/design/v2/plugin-compat.md「App 读取扩展设置」「Composer Mention Items」；
// 总设计 docs/plugins/extension-host.md §16.6、§16.7。
//
// 不在模块顶层创建 logger：本文件经 nexAgent 可达 Renderer，createServiceLogger 读 process.pid 会白屏。
// readPluginUserOptions 的结果含敏感值，这里不打任何日志。
//
// 两个方法的暴露面不同：
// - getPluginMentionItems 挂在 INexAgentService 上（随 NexAgent 频道经 RPC 暴露），工具调用走会话的授权链。
// - readPluginUserOptions 只在 Window Host 进程内：nexAgentService 把它放进侧表，node.ts 的
//   getPluginUserOptionsReader(services) 取出后作为依赖交给扩展网关。它不是任何已登记服务对象的属性，
//   ProxyChannel 按属性名分派，所以任何 RPC 频道都调不到（结构上 fail-closed，不靠黑名单剥离）。
import {
  nexPluginsMentionItemsParamsSchema,
  nexPluginsMentionItemsResultSchema,
  nexPluginsReadUserOptionsParamsSchema,
  nexPluginsReadUserOptionsResultSchema,
  nexProtocolMethods,
  type NexPluginMentionItem,
  type NexPluginsReadUserOptionsResult,
} from "@nex/shared";
import type { NexAgentWorkspaceTarget } from "#src/nex-agent/nexAgentPluginParams.js";
import type { NexProtocolClient } from "#src/nex-agent/nexProtocolClient.js";

/** JSON-RPC method-not-found：旧 CLI 不认识查询方法。 */
const METHOD_NOT_FOUND_CODE = -32601;

export interface NexAgentReadPluginUserOptionsParams {
  pluginId: string;
}

/** `supported: false`：旧 CLI 没有 plugins/readUserOptions，调用方只用非敏感值。 */
export type NexAgentPluginUserOptionsResult =
  | { supported: true; values: NexPluginsReadUserOptionsResult["values"] }
  | { supported: false };

export interface NexAgentPluginMentionItemsParams extends NexAgentWorkspaceTarget {
  sessionId: string;
  pluginId: string;
  /** 插件 MCP server 上的原始工具名（清单 `target.tool`）。 */
  tool: string;
  query: string;
}

/** `supported: false`：旧 CLI 没有 plugins/mentionItems，`@` 面板不显示这一组。 */
export type NexAgentPluginMentionItemsResult =
  | { supported: true; items: NexPluginMentionItem[] }
  | { supported: false };

/** 只在 Window Host 进程内持有；不登记为服务、不挂在任何服务对象上。 */
export interface PluginUserOptionsReader {
  readPluginUserOptions(
    params: NexAgentReadPluginUserOptionsParams,
  ): Promise<NexAgentPluginUserOptionsResult>;
}

export interface PluginHostQueriesClientDeps {
  /** 插件管理面的本机 CLI：user 层设置与工作区无关，和设置页的插件管理走同一个进程。 */
  getPluginManagementClient(): Promise<NexProtocolClient>;
  /** existing-only：持有会话的工作区 CLI；没有运行中的 CLI 时抛 runtime-unavailable，不启动进程。 */
  getExistingClient(target: NexAgentWorkspaceTarget): Promise<NexProtocolClient>;
}

export interface PluginHostQueriesClient extends PluginUserOptionsReader {
  getPluginMentionItems(
    params: NexAgentPluginMentionItemsParams,
  ): Promise<NexAgentPluginMentionItemsResult>;
}

export function createPluginHostQueriesClient(
  deps: PluginHostQueriesClientDeps,
): PluginHostQueriesClient {
  return {
    async readPluginUserOptions(params) {
      const wire = nexPluginsReadUserOptionsParamsSchema.parse({ pluginId: params.pluginId });
      const client = await deps.getPluginManagementClient();
      try {
        const result = await client.request(
          nexProtocolMethods.pluginsReadUserOptions,
          wire,
          nexPluginsReadUserOptionsResultSchema,
        );
        return { supported: true, values: result.values };
      } catch (error) {
        // 旧 CLI：当作「不支持」，不是错误。其他错误（如 data.code plugin_not_found）向上抛。
        if (isMethodNotFoundError(error)) return { supported: false };
        throw error;
      }
    },
    async getPluginMentionItems(params) {
      const wire = nexPluginsMentionItemsParamsSchema.parse({
        sessionId: params.sessionId,
        pluginId: params.pluginId,
        tool: params.tool,
        query: params.query,
      });
      // existing-only：会话必须已在 CLI 内存里；没有运行中的 CLI 时 runtime-unavailable 错误向上抛。
      const client = await deps.getExistingClient(params);
      try {
        const result = await client.request(
          nexProtocolMethods.pluginsMentionItems,
          wire,
          nexPluginsMentionItemsResultSchema,
          // 首次调用可能在会话里等用户审批（CLI 不设超时）。等得久不说明 CLI 卡死：超时只让这次查询失败，
          // 不能按默认语义上报「连接不可信」，否则进程管理器会回收整个工作区的 CLI。
          { invalidateConnectionOnTimeout: false },
        );
        return { supported: true, items: result.items };
      } catch (error) {
        if (isMethodNotFoundError(error)) return { supported: false };
        throw error;
      }
    },
  };
}

function isMethodNotFoundError(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    (error as { code?: unknown }).code === METHOD_NOT_FOUND_CODE
  );
}
