import type { WorkspacePurpose, NexTaskMeta } from "@nex/shared";

export type NexTaskListKind = "pinned" | "archived" | "timeline" | "active";
export type NexTaskListSortBy = "created" | "updated";

export interface NexTaskListWorkspaceScope {
  workspacePath: string;
  workspaceIdentity?: string;
  workspacePurpose?: WorkspacePurpose;
}

export interface NexTaskListQuery {
  kind: NexTaskListKind | "all";
  /** host 由存储 owner 确定全部范围，不接受 UI 已打开工作区作为历史目录。 */
  workspaceScopes: NexTaskListWorkspaceScope[] | "host";
  sortBy: NexTaskListSortBy;
  search?: string;
  limit?: number;
  /** 只返回这些会话（例如分享管理按来源会话 ID 解析跳转目标）；空数组返回空结果。 */
  taskIds?: readonly string[];
}

export type NexTaskListItem = NexTaskMeta & {
  searchSnippet?: string;
  searchSnippets?: string[];
};

export interface NexTaskListResult {
  items: NexTaskListItem[];
  total: number;
  hasMore: boolean;
}

export interface NexWorkspaceEventSubscriptionParams {
  workspacePath: string;
  workspaceIdentity?: string;
}
