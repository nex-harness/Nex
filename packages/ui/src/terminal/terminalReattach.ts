/**
 * side pane terminal 的刷新重连辅助（docs/next/workspace-tools.md Decision 5）。
 *
 * 页面刷新会丢掉模块级 registry，但 tab.id 稳定。把 create 返回的 `terminalId` 按 tab
 * 记到 sessionStorage；刷新后带 `terminalId` 再调 create，runner 侧仍存活时以同一 id 重挂
 * 并回放序列化屏幕。桌面/local host 会忽略 `terminalId` 并返回新 id，此时按“新建”处理。
 */

/** sessionStorage key 前缀；与其它 UI 草稿存储一样只存活于当前 tab。 */
const TERMINAL_ID_STORAGE_PREFIX = "nex.sidePaneTerminalId:";

function getSessionStorage(): Storage | null {
  // SSR / 测试环境可能没有 sessionStorage；隐私模式写入也可能抛错，调用方按“无持久化”处理。
  try {
    return typeof window === "undefined" ? null : window.sessionStorage;
  } catch {
    return null;
  }
}

function buildTerminalIdStorageKey(persistentKey: string): string {
  return `${TERMINAL_ID_STORAGE_PREFIX}${persistentKey}`;
}

/** 读取该 tab 上次记录的终端 id；没有或为空时返回 null。 */
export function readPersistedTerminalId(persistentKey: string): string | null {
  const storage = getSessionStorage();
  if (!storage) return null;
  try {
    const value = storage.getItem(buildTerminalIdStorageKey(persistentKey));
    return value && value.trim() ? value : null;
  } catch {
    return null;
  }
}

/** 记录该 tab 当前终端 id（create 成功、含重挂成功之后）。 */
export function persistTerminalId(persistentKey: string, terminalId: string): void {
  const storage = getSessionStorage();
  if (!storage) return;
  try {
    storage.setItem(buildTerminalIdStorageKey(persistentKey), terminalId);
  } catch {
    // 配额/隐私模式失败不影响终端本身，只是刷新后退化为新建。
  }
}

/** tab 真正关闭（dispose）时移除记录。 */
export function clearPersistedTerminalId(persistentKey: string): void {
  const storage = getSessionStorage();
  if (!storage) return;
  try {
    storage.removeItem(buildTerminalIdStorageKey(persistentKey));
  } catch {
    // 同上：清理失败只可能留下一条过期记录，下一次 create 会因 id 不存在而退回新建。
  }
}

/**
 * 判断 create 的结果是“重挂成功”还是“新建”。
 *
 * 只有请求了重挂且返回 id 与请求一致才算重挂；桌面/local host 忽略 terminalId 并返回新 id，
 * 因此天然按新建处理（不 reset、不假设首块是序列化屏幕）。
 */
export function resolveTerminalCreateOutcome(params: {
  sentTerminalId: string | null;
  returnedTerminalId: string;
}): boolean {
  return Boolean(params.sentTerminalId && params.returnedTerminalId === params.sentTerminalId);
}
