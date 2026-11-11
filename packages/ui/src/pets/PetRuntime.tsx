/**
 * 宠物运行时容器：把「设置里的开关/选中宠物」+「sessions-index 派生状态」
 * 接到双端呈现（Web 主窗口挂件 / Desktop 独立悬浮窗）。
 *
 * 挂载点：`Root` 应用级（不依赖 workspace）；主窗口隐藏后悬浮窗仍在桌面上。
 * 详见 docs/specs/desktop-pets.md。
 */
import { useCallback, useEffect, useMemo, useState } from "react";
import {
  clampPetSize,
  mergePetSettings,
  type AppSettings,
  type PetAnchor,
  type PetSemanticState,
  type PetSummary,
} from "@nex/shared";

import { usePlatform } from "@/hooks/usePlatform.js";
import { useServices } from "@/hooks/useServices.js";
import { useSettings } from "@/hooks/useSettingService.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { setPendingSettingsSection } from "@/lib/settingsNavigation.js";
import { logger } from "@/logger.js";
import { useTabStore } from "@/store/TabStoreProvider.js";

import { PetFloatingWidget } from "./PetFloatingWidget.js";
import type { PetRuntimeContract } from "./contract.js";
import { usePetState } from "./usePetState.js";

function usePetCatalog(enabled: boolean) {
  const { petService } = useServices();
  const [pets, setPets] = useState<PetSummary[]>([]);

  useEffect(() => {
    if (!enabled || !petService) {
      setPets([]);
      return;
    }
    let cancelled = false;
    petService
      .listPets()
      .then((result) => {
        if (!cancelled) setPets(result.pets);
      })
      .catch((error: unknown) => {
        logger.warn(
          `[pets] listPets 失败: ${error instanceof Error ? error.message : String(error)}`,
        );
      });
    return () => {
      cancelled = true;
    };
  }, [enabled, petService]);

  return pets;
}

/**
 * 精灵图 URL：desktop 经 petService.getPetSpritesheetUrl 授权（nex-media 协议）；
 * web 端回落到 server 静态路由 /api/pets/:id/spritesheet（token 保护）。
 */
function usePetSpriteUrl(pet: PetSummary | null, isDesktop: boolean): string | null {
  const { petService } = useServices();
  const [url, setUrl] = useState<string | null>(null);

  useEffect(() => {
    if (!pet || !petService) {
      setUrl(null);
      return;
    }
    if (!isDesktop) {
      // web：静态路由只依赖宠物 id，不需要 RPC。
      setUrl(`/api/pets/${encodeURIComponent(pet.id)}/spritesheet`);
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const result = await petService.getPetSpritesheetUrl({ petId: pet.id });
        if (!cancelled) setUrl(result?.url ?? null);
      } catch (error) {
        if (!cancelled) {
          logger.warn(
            `[pets] 解析精灵图 URL 失败: ${error instanceof Error ? error.message : String(error)}`,
          );
          setUrl(null);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [pet, petService, isDesktop]);

  return url;
}

/** Desktop：把状态经 IPlatformService 推到 main 的悬浮窗。 */
function useSyncDesktopPetWindow(input: {
  platform: ReturnType<typeof usePlatform>;
  supportsOverlayWindow: boolean;
  enabled: boolean;
  selectedPet: PetSummary | null;
  spriteUrl: string | null;
  ready: boolean;
  state: PetSemanticState;
  petSettings: AppSettings["pet"];
  intl: ReturnType<typeof useNexIntl>["intl"];
}) {
  const { platform, supportsOverlayWindow, enabled, selectedPet, spriteUrl, ready, state } = input;
  const { petSettings, intl } = input;
  // 关键设计：宠物是桌面伴侣，没有 workspace 订阅（ready=false）时也显示 idle；
  // 主窗口隐藏/关闭后 renderer 仍存活（macOS window-all-closed 不退出），悬浮窗继续显示。
  useEffect(() => {
    if (!supportsOverlayWindow || !platform.syncPetState) return;
    if (!enabled || !selectedPet || !spriteUrl) {
      platform.syncPetState(null);
      return;
    }
    platform.syncPetState({
      petId: selectedPet.id,
      // 无订阅就绪时回落 idle（不因 ready=false 把窗口销毁）。
      animation: ready ? state : "idle",
      spriteUrl,
      manifest: selectedPet.manifest,
      spriteRows: selectedPet.spriteRows,
      sizePx: clampPetSize(petSettings?.size),
      visibility: petSettings?.visibility ?? "always",
      labels: {
        newChat: intl.formatMessage({ id: "pets.controls.newChat" }),
        voice: intl.formatMessage({ id: "pets.controls.voice" }),
        comingSoon: intl.formatMessage({ id: "pets.controls.comingSoon" }),
      },
      ...(petSettings?.windowPosition
        ? {
            placement: {
              x: petSettings.windowPosition.x,
              y: petSettings.windowPosition.y,
              ...(petSettings.windowDisplayId !== undefined
                ? { displayId: petSettings.windowDisplayId }
                : {}),
              ...(petSettings.windowSnapZone ? { snapZone: petSettings.windowSnapZone } : {}),
            },
          }
        : {}),
    });
    // 注意：effect cleanup 不再 syncPetState(null)——主窗口隐藏不等于关闭宠物；
    // 销毁只在「开关关闭 / 宠物被删 / spriteUrl 丢失」的分支里显式触发。
  }, [
    supportsOverlayWindow,
    platform,
    enabled,
    selectedPet,
    spriteUrl,
    ready,
    state,
    petSettings?.windowPosition,
    petSettings?.windowDisplayId,
    petSettings?.windowSnapZone,
    petSettings?.size,
    petSettings?.visibility,
    intl,
  ]);
}

export function PetRuntime({ isDesktop }: PetRuntimeContract) {
  const platform = usePlatform();
  const { intl } = useNexIntl();
  const openSettingsTab = useTabStore((state) => state.openSettingsTab);
  const { settings, update } = useSettings();
  const petSettings = settings?.pet;
  const enabled = Boolean(petSettings?.enabled && petSettings.petId);

  const pets = usePetCatalog(enabled);
  const selectedPet = useMemo(
    () => pets.find((pet) => pet.id === petSettings?.petId) ?? null,
    [pets, petSettings?.petId],
  );
  const spriteUrl = usePetSpriteUrl(selectedPet, isDesktop);
  const { state, ready } = usePetState(enabled && Boolean(selectedPet));

  const supportsOverlayWindow = isDesktop && Boolean(platform.syncPetState);

  useSyncDesktopPetWindow({
    platform,
    supportsOverlayWindow,
    enabled,
    selectedPet,
    spriteUrl,
    ready,
    state,
    petSettings,
    intl,
  });

  // 宠物右键「设置」：main 先把主窗口带到前台，再要求打开宠物设置分区。
  useEffect(() => {
    if (!platform.onOpenSettingsSection) return;
    return platform.onOpenSettingsSection((section) => {
      if (section !== "pets") return;
      setPendingSettingsSection("pets");
      openSettingsTab();
    });
  }, [platform, openSettingsTab]);

  const handleWidgetAnchorChange = useCallback(
    (anchor: PetAnchor) => {
      void update({ pet: mergePetSettings(petSettings, { anchor }) });
    },
    [update, petSettings],
  );

  // Web（或 desktop 悬浮窗不可用时的降级）：主窗口内浮动挂件。
  // 挂件在 workspace 内呈现；desktop 悬浮窗走独立窗口，不依赖 workspace。
  if (!enabled || !selectedPet || !spriteUrl || !ready || supportsOverlayWindow) {
    return null;
  }

  return (
    <PetFloatingWidget
      spriteUrl={spriteUrl}
      manifest={selectedPet.manifest}
      state={state}
      spriteRows={selectedPet.spriteRows}
      sizePx={clampPetSize(petSettings?.size)}
      anchor={petSettings?.anchor ?? "bottom-right"}
      onAnchorChange={handleWidgetAnchorChange}
    />
  );
}

export type { PetSemanticState };
