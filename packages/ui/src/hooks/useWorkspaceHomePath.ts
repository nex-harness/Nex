import type { IServiceAccessor } from "@nex/services";
import { useEffect, useState } from "react";
import { useWorkspaceBoundServices } from "@/hooks/useWorkspaceServices.js";
import { isAbsoluteFilePath } from "@/lib/path.js";

interface WorkspaceHomePathParams {
  workspacePath: string;
  workspaceIdentity?: string | null;
  remoteSessionId?: string | null;
}

interface WorkspaceHomeCacheEntry {
  homePath?: string;
  promise?: Promise<string | null>;
  systemService: IServiceAccessor["systemService"];
}

const workspaceHomePathCache = new Map<string, WorkspaceHomeCacheEntry>();

function buildWorkspaceHomeCacheKey(params: WorkspaceHomePathParams): string {
  const workspaceKey = params.workspaceIdentity?.trim() || params.workspacePath;
  return `${workspaceKey}::${params.remoteSessionId?.trim() || "local"}`;
}

function normalizeHostHomePath(homePath: string): string | null {
  const trimmed = homePath.trim();
  return trimmed && isAbsoluteFilePath(trimmed) ? trimmed : null;
}

export function useWorkspaceHomePath(params: WorkspaceHomePathParams): string | undefined {
  // system.info 的 homedir 必须来自 workspace 所在的机器：多 runner 绑定后由 server 路由到
  // 该 workspace 的 runner（无目标时回落第一台 runner）；桌面/local host 行为不变。
  const { systemService } = useWorkspaceBoundServices(
    params.workspacePath,
    params.remoteSessionId,
    params.workspaceIdentity,
  );
  const cacheKey = buildWorkspaceHomeCacheKey(params);
  const [state, setState] = useState<{ cacheKey: string; homePath?: string }>(() => {
    const cached = workspaceHomePathCache.get(cacheKey);
    return {
      cacheKey,
      homePath: cached && cached.systemService === systemService ? cached.homePath : undefined,
    };
  });

  useEffect(() => {
    let disposed = false;
    if (typeof systemService?.info !== "function") {
      return () => {
        disposed = true;
      };
    }
    const cached = workspaceHomePathCache.get(cacheKey);
    if (cached && cached.systemService === systemService && cached.homePath) {
      setState({ cacheKey, homePath: cached.homePath });
      return () => {
        disposed = true;
      };
    }

    const promise =
      cached?.systemService === systemService && cached.promise
        ? cached.promise
        : systemService.info().then(
            (info) => normalizeHostHomePath(info.homedir),
            () => null,
          );
    workspaceHomePathCache.set(cacheKey, { systemService, promise });

    void promise.then((homePath) => {
      if (disposed) return;
      const current = workspaceHomePathCache.get(cacheKey);
      if (!current || current.systemService !== systemService) return;
      if (homePath) {
        workspaceHomePathCache.set(cacheKey, { systemService, homePath });
      } else {
        workspaceHomePathCache.delete(cacheKey);
      }
      setState({ cacheKey, ...(homePath ? { homePath } : {}) });
    });

    return () => {
      disposed = true;
    };
  }, [cacheKey, systemService]);

  return state.cacheKey === cacheKey ? state.homePath : undefined;
}
