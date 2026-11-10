import type { IPlatformService } from "@nex/shared";
import type { WebRemoteControlWorkspaceSwitcherApi } from "@/root/types.js";

export type WebRemoteControlDeviceNavigation = NonNullable<
  WebRemoteControlWorkspaceSwitcherApi["deviceNavigation"]
>;
export type WebRemoteControlAuthPlatform = Pick<
  IPlatformService,
  "notifyWebRemoteControlAuthStateChanged"
>;
export type { WebRemoteControlMobileTaskView } from "@nex/shared";
export type WebRemoteControlTaskViewSwitcher = Pick<
  WebRemoteControlWorkspaceSwitcherApi,
  "updateMobileTaskView" | "updateMobileViewState"
>;
