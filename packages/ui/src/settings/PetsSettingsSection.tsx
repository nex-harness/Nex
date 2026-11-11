/**
 * 设置页「宠物」分区：开关、宠物网格选择、刷新、创建占位。
 * 布局参考 Codex Mini & Pets 页：顶部当前宠物预览卡 + 下方 My pets 网格。
 * 产品规则见 docs/specs/desktop-pets.md。
 */
import { useCallback, useEffect, useMemo, useState } from "react";
import { Plus, RefreshCw } from "lucide-react";
import {
  PET_SIZE_DEFAULT_PX,
  PET_SIZE_MAX_PX,
  PET_SIZE_MIN_PX,
  PET_TOGGLE_ACCELERATOR,
  clampPetSize,
  mergePetSettings,
  type PetListResult,
  type PetSummary,
  type PetVisibility,
} from "@nex/shared";

import { Button } from "@/components/ui/button.js";
import { Switch } from "@/components/ui/switch.js";
import { useServices } from "@/hooks/useServices.js";
import { useSettings } from "@/hooks/useSettingService.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { logger } from "@/logger.js";
import type { PetsSettingsSectionContract } from "@/pets/contract.js";
import { PetSprite } from "@/pets/PetSprite.js";
import { SettingsGroupCard, SettingsRow } from "@/settings/SettingsPageParts.js";

/** 宠物目录加载 + 精灵图 URL 解析（desktop 经 petService 授权，web 用 server 静态路由）。 */
function usePetCatalog(isDesktop: boolean) {
  const { petService } = useServices();
  const [catalog, setCatalog] = useState<PetListResult>({ pets: [], errors: [] });
  const [loading, setLoading] = useState(false);
  const [spriteUrls, setSpriteUrls] = useState<Record<string, string>>({});

  const load = useCallback(
    async (refresh: boolean) => {
      if (!petService) return;
      setLoading(true);
      try {
        const result = refresh ? await petService.refreshPets() : await petService.listPets();
        setCatalog(result);
      } catch (error) {
        logger.warn(
          `[pets] 设置页加载宠物目录失败: ${error instanceof Error ? error.message : String(error)}`,
        );
      } finally {
        setLoading(false);
      }
    },
    [petService],
  );

  useEffect(() => {
    void load(false);
  }, [load]);

  useEffect(() => {
    if (!petService) return;
    if (!isDesktop) {
      // web：静态路由只依赖宠物 id，不需要逐只走 RPC。
      const next: Record<string, string> = {};
      for (const pet of catalog.pets) {
        next[pet.id] = `/api/pets/${encodeURIComponent(pet.id)}/spritesheet`;
      }
      setSpriteUrls(next);
      return;
    }
    let cancelled = false;
    const resolveAll = async () => {
      const next: Record<string, string> = {};
      for (const pet of catalog.pets) {
        try {
          const result = await petService.getPetSpritesheetUrl({ petId: pet.id });
          if (result) next[pet.id] = result.url;
        } catch {
          // 单只失败只影响该卡片占位。
        }
      }
      if (!cancelled) setSpriteUrls(next);
    };
    void resolveAll();
    return () => {
      cancelled = true;
    };
  }, [catalog.pets, petService, isDesktop]);

  return { catalog, loading, spriteUrls, hasService: Boolean(petService), load };
}

/** 单个宠物卡片。 */
function PetCard({
  pet,
  selected,
  spriteUrl,
  onSelect,
}: {
  pet: PetSummary;
  selected: boolean;
  spriteUrl: string | null;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={`flex min-h-[180px] flex-col items-center justify-center gap-2 rounded-xl border p-4 text-center transition-colors ${
        selected ? "border-primary bg-surface" : "border-border bg-card hover:bg-surface"
      }`}
    >
      {spriteUrl ? (
        <PetSprite
          spriteUrl={spriteUrl}
          manifest={pet.manifest}
          spriteRows={pet.spriteRows}
          animationName="idle"
          heightPx={72}
        />
      ) : (
        <div className="h-[72px] w-[72px] rounded-lg bg-surface" />
      )}
      <div className="text-ui-base font-medium text-foreground">{pet.displayName}</div>
      {pet.description ? (
        <div className="line-clamp-2 text-ui-sm text-foreground-subtle">{pet.description}</div>
      ) : null}
    </button>
  );
}

export function PetsSettingsSection({ isDesktop }: PetsSettingsSectionContract) {
  const { intl } = useNexIntl();
  const { settings, update } = useSettings();
  const petSettings = settings?.pet;
  const { catalog, loading, spriteUrls, hasService, load } = usePetCatalog(isDesktop);

  const enabled = petSettings?.enabled ?? false;
  const selectedPetId = petSettings?.petId ?? null;
  const selectedPet = useMemo(
    () => catalog.pets.find((pet) => pet.id === selectedPetId) ?? null,
    [catalog.pets, selectedPetId],
  );

  const handleToggle = useCallback(
    (next: boolean) => {
      void update({
        pet: mergePetSettings(petSettings, {
          enabled: next,
          petId: selectedPetId ?? catalog.pets[0]?.id ?? null,
        }),
      });
    },
    [update, petSettings, selectedPetId, catalog.pets],
  );

  const handleSelect = useCallback(
    (petId: string) => {
      void update({ pet: mergePetSettings(petSettings, { enabled: true, petId }) });
    },
    [update, petSettings],
  );

  const visibility: PetVisibility = petSettings?.visibility ?? "always";
  const handleVisibilityChange = useCallback(
    (next: PetVisibility) => {
      void update({
        pet: mergePetSettings(petSettings, { visibility: next === "always" ? undefined : next }),
      });
    },
    [update, petSettings],
  );

  const size = clampPetSize(petSettings?.size);
  // 滑杆拖动期间只改本地值，松手再落盘，避免每个像素都触发设置广播与悬浮窗重排。
  const [draftSize, setDraftSize] = useState<number | null>(null);
  const commitSize = useCallback(
    (next: number) => {
      setDraftSize(null);
      void update({
        pet: mergePetSettings(petSettings, {
          size: next === PET_SIZE_DEFAULT_PX ? undefined : next,
        }),
      });
    },
    [update, petSettings],
  );

  return (
    <div className="space-y-4">
      <SettingsGroupCard>
        <SettingsRow
          label={intl.formatMessage({ id: "settings.pets.enable" })}
          description={intl.formatMessage({ id: "settings.pets.enableDescription" })}
          control={
            <Switch
              checked={enabled}
              onCheckedChange={handleToggle}
              aria-label={intl.formatMessage({ id: "settings.pets.enable" })}
            />
          }
        />
        {isDesktop ? (
          <SettingsRow
            label={intl.formatMessage({ id: "settings.pets.visibility" })}
            description={intl.formatMessage(
              { id: "settings.pets.visibilityDescription" },
              { shortcut: PET_TOGGLE_ACCELERATOR.replace("CommandOrControl", "Ctrl/⌘") },
            )}
            control={
              <select
                value={visibility}
                onChange={(event) => handleVisibilityChange(event.target.value as PetVisibility)}
                aria-label={intl.formatMessage({ id: "settings.pets.visibility" })}
                className="h-8 rounded-md border border-border bg-card px-2 text-ui-sm text-foreground"
              >
                <option value="always">
                  {intl.formatMessage({ id: "settings.pets.visibilityAlways" })}
                </option>
                <option value="on-demand">
                  {intl.formatMessage({ id: "settings.pets.visibilityOnDemand" })}
                </option>
              </select>
            }
          />
        ) : null}
        <SettingsRow
          label={intl.formatMessage({ id: "settings.pets.size" })}
          description={intl.formatMessage({ id: "settings.pets.sizeDescription" })}
          control={
            <div className="flex items-center gap-2">
              <input
                type="range"
                min={PET_SIZE_MIN_PX}
                max={PET_SIZE_MAX_PX}
                step={4}
                value={draftSize ?? size}
                onChange={(event) => setDraftSize(Number(event.target.value))}
                onPointerUp={(event) => commitSize(Number(event.currentTarget.value))}
                onKeyUp={(event) => commitSize(Number(event.currentTarget.value))}
                aria-label={intl.formatMessage({ id: "settings.pets.size" })}
                className="w-32"
              />
              <span className="w-12 text-right text-ui-sm text-foreground-subtle">
                {draftSize ?? size}px
              </span>
              <Button
                variant="ghost"
                size="sm"
                disabled={size === PET_SIZE_DEFAULT_PX && draftSize === null}
                onClick={() => commitSize(PET_SIZE_DEFAULT_PX)}
              >
                {intl.formatMessage({ id: "settings.pets.sizeReset" })}
              </Button>
            </div>
          }
        />
      </SettingsGroupCard>

      {selectedPet ? (
        <SettingsGroupCard>
          <div className="flex flex-col items-center gap-3 px-4 py-6">
            <PetSprite
              spriteUrl={spriteUrls[selectedPet.id] ?? ""}
              manifest={selectedPet.manifest}
              spriteRows={selectedPet.spriteRows}
              animationName="idle"
              heightPx={104}
            />
            <div className="text-ui-base font-medium text-foreground">
              {selectedPet.displayName}
            </div>
          </div>
        </SettingsGroupCard>
      ) : null}

      <div>
        <div className="mb-2 flex items-center justify-between">
          <div className="text-ui-base font-medium text-foreground">
            {intl.formatMessage({ id: "settings.pets.myPets" })}
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void load(true)}
              disabled={loading || !hasService}
              aria-label={intl.formatMessage({ id: "settings.pets.refresh" })}
            >
              <RefreshCw className={loading ? "animate-spin" : ""} />
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled
              title={intl.formatMessage({ id: "settings.pets.createDisabledHint" })}
            >
              <Plus className="mr-1" />
              {intl.formatMessage({ id: "settings.pets.create" })}
            </Button>
          </div>
        </div>

        {catalog.pets.length === 0 ? (
          <SettingsGroupCard>
            <div className="px-4 py-8 text-center text-ui-base text-foreground-subtle">
              {intl.formatMessage({ id: "settings.pets.empty" })}
            </div>
          </SettingsGroupCard>
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
            {catalog.pets.map((pet) => (
              <PetCard
                key={pet.id}
                pet={pet}
                selected={pet.id === selectedPetId}
                spriteUrl={spriteUrls[pet.id] ?? null}
                onSelect={() => handleSelect(pet.id)}
              />
            ))}
          </div>
        )}

        {catalog.errors.length > 0 ? (
          <SettingsGroupCard>
            <div className="space-y-1 px-4 py-3">
              <div className="text-ui-base font-medium text-foreground">
                {intl.formatMessage({ id: "settings.pets.errorsTitle" })}
              </div>
              {catalog.errors.map((error) => (
                <div key={error.dirName} className="text-ui-sm text-foreground-subtle">
                  {error.dirName}: {error.reason}
                </div>
              ))}
            </div>
          </SettingsGroupCard>
        ) : null}
      </div>
    </div>
  );
}
