import {
  buildRemoteHostWorkspaceIdentity,
  buildRemoteWorkspaceIdentity,
  parseRemoteWorkspaceIdentity,
  remoteHostIdFromWorkspaceIdentity,
  type AppSettings,
  type PersistedWorkspaceSessionEntry,
} from "@nex/shared";
import type { VirtualProject } from "#src/project-storage/contract.js";
import type { RemoteProjectDirectory, RemoteProjectHost } from "#src/remote-projects/contract.js";

const key = (entry: PersistedWorkspaceSessionEntry) =>
  entry.kind === "local"
    ? entry.workspacePath
    : entry.workspaceIdentity?.trim() ||
      buildRemoteWorkspaceIdentity(entry.workspacePath, entry.target);

/** 单向兼容投影。输入必须是同一 SQLite 快照，不读取旧集合重建成员。 */
export function projectWorkspaceSettings(
  projects: readonly VirtualProject[],
  directory: RemoteProjectDirectory,
  settings: AppSettings,
): Pick<
  AppSettings,
  "lastActiveWorkspace" | "lastWorkspaceSession" | "recentProjects" | "lastActiveTabIndex"
> {
  const localPaths = [
    ...new Set(
      projects
        .filter((project) => !project.deleted)
        .flatMap((project) => project.roots.map((root) => root.rootPath)),
    ),
  ];
  const localPathSet = new Set(localPaths);
  const lastWorkspaceSession: PersistedWorkspaceSessionEntry[] = [
    // Bug 原因：项目表迁移后，整块替换 lastWorkspaceSession 会丢掉 1.x 保留的 tab 顺序；
    // 已删除项目又会被过滤旧值的操作重新带回。只保留仍被项目树覆盖的旧条目，再追加新根。
    ...(settings.lastWorkspaceSession ?? []).filter(
      (entry) => entry.kind === "local" && localPathSet.has(entry.workspacePath),
    ),
    ...localPaths
      .filter(
        (workspacePath) =>
          !settings.lastWorkspaceSession?.some(
            (entry) => entry.kind === "local" && entry.workspacePath === workspacePath,
          ),
      )
      .map(
        (workspacePath): PersistedWorkspaceSessionEntry => ({
          kind: "local",
          workspacePath,
          workspacePurpose: "project",
        }),
      ),
  ];
  const hosts = new Map(directory.hosts.map((host) => [host.id, host]));
  const identityOrder = lastWorkspaceSession.map(key);
  const seen = new Set<string>();
  for (const entry of directory.entries) {
    const host = hosts.get(entry.hostId);
    // 与本地已删项目一致，远端已删除的入口不导出给旧版本。
    if (!host?.target || entry.deleted) continue;
    const paths = entry.roots.length
      ? entry.roots.map((root) => root.rootPath)
      : entry.legacyWorkspace
        ? [entry.legacyWorkspace.workspacePath]
        : [];
    for (const workspacePath of paths) {
      const stableKey = buildRemoteHostWorkspaceIdentity(host.id, workspacePath);
      if (seen.has(stableKey)) continue;
      seen.add(stableKey);
      identityOrder.push(stableKey);
      const legacy =
        entry.legacyWorkspace?.workspacePath === workspacePath ? entry.legacyWorkspace : null;
      const target = legacy?.target ?? host.target;
      // 旧版通过 snapshot 中的实际 credential key 取凭据；不制造无值的镜像 key。
      lastWorkspaceSession.push({
        ...legacy,
        kind: "remote",
        workspacePath,
        workspaceIdentity:
          legacy?.workspaceIdentity &&
          parseRemoteWorkspaceIdentity(legacy.workspaceIdentity)?.kind !== "server"
            ? legacy.workspaceIdentity
            : buildRemoteWorkspaceIdentity(workspacePath, target),
        workspacePurpose: "project",
        target,
        lastOpenedAt: legacy?.lastOpenedAt ?? host.lastOpenedAt ?? 0,
        lastConnectionStatus: legacy?.lastConnectionStatus ?? "connected",
      });
    }
  }

  // 只在新字段缺席的首次升级取旧选择；null 是已经清空，不再被旧 index 填回。
  let active =
    settings.lastActiveWorkspace === undefined
      ? (settings.lastWorkspaceSession?.[settings.lastActiveTabIndex ?? 0] ?? null)
      : settings.lastActiveWorkspace;
  if (active?.kind === "remote") {
    const identity = key(active);
    let host: RemoteProjectHost | undefined = hosts.get(
      remoteHostIdFromWorkspaceIdentity(identity) ?? "",
    );
    if (!host && parseRemoteWorkspaceIdentity(identity)?.kind !== "server") {
      const route = remoteHostIdFromWorkspaceIdentity(
        buildRemoteWorkspaceIdentity(active.workspacePath, active.target),
      );
      const matches = directory.hosts.filter(
        (candidate) =>
          candidate.target &&
          remoteHostIdFromWorkspaceIdentity(
            buildRemoteWorkspaceIdentity(active!.workspacePath, candidate.target),
          ) === route,
      );
      if (matches.length === 1) host = matches[0];
    }
    const retained =
      host &&
      directory.entries.some(
        (entry) =>
          entry.hostId === host.id &&
          (entry.roots.some((root) => root.rootPath === active!.workspacePath) ||
            entry.legacyWorkspace?.workspacePath === active!.workspacePath),
      );
    active =
      host?.target && (retained || active.workspacePurpose === "conversation")
        ? {
            ...active,
            target: host.target,
            workspaceIdentity: buildRemoteHostWorkspaceIdentity(host.id, active.workspacePath),
          }
        : null;
  }
  const activeIndex = active ? identityOrder.indexOf(key(active)) : -1;
  return {
    lastActiveWorkspace: active,
    lastWorkspaceSession,
    recentProjects: mergeRecentProjects(settings.recentProjects, localPaths),
    lastActiveTabIndex: Math.max(0, activeIndex),
  };
}

/**
 * 合并「旧的项目列表」与「项目树投影出的根目录」。
 *
 * 修复依据：`0005_projects` 迁移后 `recentProjects` 由 projects 表重建，1.x 升级用户的
 * 旧列表会被整体覆盖（12 个项目只剩 1 个），表现为「升级后对话历史没了」，而任务数据其实
 * 完好。这里保留旧值中仍被项目树覆盖的条目（维持原顺序），再追加项目树里新增的根。
 * 旧值中已从项目树消失的路径会被丢弃——它确实被用户移除了。
 */
function mergeRecentProjects(
  previous: readonly string[] | undefined,
  projectPaths: readonly string[],
): string[] {
  const projectSet = new Set(projectPaths);
  const merged = [...(previous ?? []).filter((path) => projectSet.has(path)), ...projectPaths];
  return [...new Set(merged)].slice(0, 10);
}
