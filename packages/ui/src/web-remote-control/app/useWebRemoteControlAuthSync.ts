import { useEffect, useRef } from "react";
import type { WebRemoteControlAuthPlatform } from "@/web-remote-control/contract.js";

/**
 * Nex 无账号体系：OAuth 会话事件已移除，远控只同步一次「未登录」投影。
 * 保留该 hook 以维持调用方结构与平台通知时序。
 */
export function useWebRemoteControlAuthSync(
  platform: WebRemoteControlAuthPlatform,
  _oauth: unknown,
  _userId: string | undefined,
  restoring: boolean,
): void {
  const initialStateSynced = useRef(false);
  useEffect(() => {
    if (restoring || initialStateSynced.current) return;
    initialStateSynced.current = true;
    platform.notifyWebRemoteControlAuthStateChanged?.({ state: "signed-out" });
  }, [platform, restoring]);
}
