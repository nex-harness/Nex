import { z } from "zod";

export const SERVER_REMOTE_PROTOCOL_VERSION = 1;

export const serverRemoteWorkspaceInfoSchema = z.object({
  path: z.string().trim().min(1),
  label: z.string().trim().min(1).optional(),
  workspaceIdentity: z.string().trim().min(1).optional(),
  // 多 runner 拓扑新增字段；旧 server 不返回这些键时保持 undefined，
  // web 客户端据此回退到目录浏览器，不能把缺失当成“runner 已连接但没有 workspace”。
  runnerId: z.string().trim().min(1).optional(),
  runnerName: z.string().trim().min(1).optional(),
  runnerOs: z.string().trim().min(1).optional(),
  // docs/next/runner-update.md 决策 2：runner 自报构建身份与能力。
  // 旧 server/旧 runner 不返回，UI 按“无任何可选能力”处理。
  runnerBuildId: z.string().trim().min(1).optional(),
  runnerCapabilities: z.array(z.string().trim().min(1)).optional(),
});

export const serverRemoteInfoSchema = z.object({
  serverId: z.string().trim().min(1),
  name: z.string().trim().min(1).optional(),
  version: z.string(),
  protocolVersion: z.literal(SERVER_REMOTE_PROTOCOL_VERSION),
  authRequired: z.boolean(),
  workspaces: z.array(serverRemoteWorkspaceInfoSchema),
  capabilities: z.object({
    desktopContinuous: z.literal(true),
    websocketRpc: z.literal(true),
    // 旧 Server 缺少新增 dynamic event，必须先声明能力再订阅，避免异常打进对端读循环。
    processResourceTelemetry: z.boolean().optional(),
  }),
});

export type ServerRemoteWorkspaceInfo = z.infer<typeof serverRemoteWorkspaceInfoSchema>;

export type ServerRemoteInfo = z.infer<typeof serverRemoteInfoSchema>;

export const serverRemoteHostCapabilitySchema = z
  .object({
    capability: z.string().trim().min(1),
    expiresAt: z.number().int().positive(),
  })
  .strict();

export type ServerRemoteHostCapability = z.infer<typeof serverRemoteHostCapabilitySchema>;
