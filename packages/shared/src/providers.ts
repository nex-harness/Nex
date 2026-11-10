import { z } from "zod";

/**
 * Nex agent 提供方的单一真源。
 *
 * 类型 NexProvider、运行时 schema nexProviderSchema 都从这里派生,
 * 避免各处内联 z.enum([...]) 副本随新增/删除 provider 漂移。
 * 本模块只依赖 zod(叶子),可被 validation / nex-protocol 等无环引用。
 */
export const NEX_PROVIDERS = ["glm"] as const;

export const nexProviderSchema = z.enum(NEX_PROVIDERS);

export type NexProvider = (typeof NEX_PROVIDERS)[number];
