import {
  nexProtocolMethods,
  nexPluginsReferenceCatalogResultSchema,
  type NexPluginsReferenceCatalogParams,
} from "@nex/shared";
import type { NexProtocolClient } from "#src/nex-agent/nexProtocolClient.js";

/** 旧协议严格校验响应；新展示字段走独立入口，只有 -32601 能证明旧 Agent 不支持。 */
export async function requestPluginReferenceCatalog(
  client: Pick<NexProtocolClient, "request">,
  params: NexPluginsReferenceCatalogParams,
) {
  try {
    return await client.request(
      nexProtocolMethods.pluginsReferenceCatalogWithCategory,
      params,
      nexPluginsReferenceCatalogResultSchema,
    );
  } catch (error) {
    if (!(typeof error === "object" && error !== null && "code" in error && error.code === -32601))
      throw error;
    return client.request(
      nexProtocolMethods.pluginsReferenceCatalog,
      params,
      nexPluginsReferenceCatalogResultSchema,
    );
  }
}
