import { useEffect, useRef } from "react";
import type { WebRemoteControlAuthPlatform } from "@/web-remote-control/contract.js";

/**
 * Nex 无账号体系：远控只同步一次「未登录」投影，保留该 hook 以维持平台通知时序。
 */
export function useWebRemoteControlAuthSync(platform: WebRemoteControlAuthPlatform): void {
  const initialStateSynced = useRef(false);
  useEffect(() => {
    if (initialStateSynced.current) return;
    initialStateSynced.current = true;
    platform.notifyWebRemoteControlAuthStateChanged?.({ state: "signed-out" });
  }, [platform]);
}
