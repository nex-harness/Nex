// 服务端路由 workspace identity 的统一构造/解析工具（多 runner 拓扑）。
//
// 格式契约（docs/next/multi-runner.md）：
//   runner:<runnerId>:<runner-local absolute path>
//
// 只有前两个 ":" 是结构分隔符：runner 本地路径允许自带 ":"（Windows 盘符
// `C:\Users\me`、UNC 共享、POSIX 文件名带冒号），因此解析时必须只消费
// 前两段，剩余整段作为 path。
//
// 构造侧权威是 Go server（next/server/internal/runners/identity.go），runner
// 只把 hello 里的 id/路径交给 server 拼身份；TS 侧的构造器供测试与需要复现
// server 结果的调用方使用，禁止业务代码手写拼接。
//
// 与 remote-workspace-identity.ts 的关系：`remote:` 身份只属于桌面 SSH/WSL/
// Docker 会话；`runner:` 身份走同一条 /ws 连接，由 server 按 runnerId 路由，
// 客户端不能把它当成 remote session（见 isClientRemoteWorkspaceIdentity）。
const SERVER_IDENTITY_PREFIX = "runner:";

/** runnerId 不允许包含 ":"（结构分隔符）或空白（server 侧 ValidRunnerID 同规则）。 */
function isValidRunnerId(runnerId: string): boolean {
  if (runnerId.length === 0) {
    return false;
  }
  for (const char of runnerId) {
    if (char === ":" || /\s/u.test(char)) {
      return false;
    }
  }
  return true;
}

export interface ParsedServerWorkspaceIdentity {
  runnerId: string;
  /** runner 本地绝对路径（未归一化，原样来自 server）。 */
  workspacePath: string;
}

/**
 * 构造 `runner:<runnerId>:<path>`。非法 runnerId 或空路径返回 null——
 * 调用方（测试/迁移工具）必须显式处理，不能产出无法路由的身份。
 */
export function buildServerWorkspaceIdentity(
  runnerId: string,
  workspacePath: string,
): string | null {
  const trimmedRunnerId = runnerId.trim();
  if (!isValidRunnerId(trimmedRunnerId) || workspacePath.length === 0) {
    return null;
  }
  return `${SERVER_IDENTITY_PREFIX}${trimmedRunnerId}:${workspacePath}`;
}

/**
 * 解析 `runner:<runnerId>:<path>`；非法或非 runner 身份返回 null。
 * 与 Go ParseIdentity 保持一致：先 trim 整体，再按前两个 ":" 切分。
 */
export function parseServerWorkspaceIdentity(
  identity: string,
): ParsedServerWorkspaceIdentity | null {
  const trimmed = identity.trim();
  if (!trimmed.startsWith(SERVER_IDENTITY_PREFIX)) {
    return null;
  }
  const rest = trimmed.slice(SERVER_IDENTITY_PREFIX.length);
  const separatorIndex = rest.indexOf(":");
  if (separatorIndex <= 0) {
    return null;
  }
  const runnerId = rest.slice(0, separatorIndex);
  const workspacePath = rest.slice(separatorIndex + 1);
  if (!isValidRunnerId(runnerId) || workspacePath.length === 0) {
    return null;
  }
  return { runnerId, workspacePath };
}

/** identity 是否是 server 路由身份（可被 parseServerWorkspaceIdentity 解析）。 */
export function isServerWorkspaceIdentity(identity: string): boolean {
  return parseServerWorkspaceIdentity(identity) !== null;
}
