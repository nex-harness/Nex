/**
 * 宠物悬浮窗 preload：最小攻击面，只暴露状态订阅与点击回传。
 */
import { contextBridge, ipcRenderer } from "electron";
import { PlatformChannels, type PetWindowAction, type PetWindowState } from "@nex/shared";

contextBridge.exposeInMainWorld("nexPet", {
  /** 订阅 main 推送的宠物状态（null 表示隐藏）。 */
  onState: (handler: (state: PetWindowState | null) => void) => {
    const listener = (_event: unknown, state: PetWindowState | null) => handler(state);
    ipcRenderer.on(PlatformChannels.PetWindowState, listener);
    return () => {
      ipcRenderer.removeListener(PlatformChannels.PetWindowState, listener);
    };
  },
  /** 点击宠物（非拖拽）时回传。 */
  sendAction: (action: PetWindowAction) => {
    ipcRenderer.send(PlatformChannels.PetWindowAction, action);
  },
});
