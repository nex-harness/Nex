import { access } from "node:fs/promises";
import { join } from "node:path";

export interface BundledAgentWiring {
  NEX_AGENT_SERVER_COMMAND: string;
  NEX_AGENT_SERVER_ARGS_JSON: string;
}

export function createReleaseAgentWiring(
  runtimeRoot: string,
  runtimeNode: string,
  env: Record<string, string | undefined>,
): BundledAgentWiring | null {
  if (env.NEX_AGENT_SERVER_COMMAND?.trim()) return null;
  return {
    NEX_AGENT_SERVER_COMMAND: runtimeNode,
    NEX_AGENT_SERVER_ARGS_JSON: JSON.stringify([
      join(runtimeRoot, "nex.cjs"),
      "app-server",
      "--stdio",
    ]),
  };
}

/**
 * 发行包内 Core 既不在 monorepo、也没有 Electron runtime，`nexAgentProcessManager`
 * 的默认解析链（monorepo dev → Electron → 远端已部署 binary）会全部落空。这里把随包
 * `nex.cjs` 注入为 agent 启动命令；env 覆盖是该解析链的最高优先级，因此显式配置的
 * `NEX_AGENT_SERVER_COMMAND` 永远优先，开发态（入口同目录无 nex.cjs）不受影响。
 */
export async function resolveBundledAgentWiring(
  entryDir: string,
  env: Record<string, string | undefined>,
): Promise<BundledAgentWiring | null> {
  if (env.NEX_AGENT_SERVER_COMMAND?.trim()) {
    return null;
  }
  const bundlePath = join(entryDir, "nex.cjs");
  try {
    await access(bundlePath);
  } catch {
    return null;
  }
  return {
    NEX_AGENT_SERVER_COMMAND: process.execPath,
    NEX_AGENT_SERVER_ARGS_JSON: JSON.stringify([bundlePath, "app-server", "--stdio"]),
  };
}
