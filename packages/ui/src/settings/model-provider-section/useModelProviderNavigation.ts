import { useEffect, useMemo } from "react";
import type { ProviderSettingsFormProvider } from "@/lib/providerSettingsFormTypes.js";
import { getProviderFormLabel } from "@/lib/providerSettingsFormTypes.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { type ModelProviderNavGroup } from "@/settings/model-provider-section/constants.js";
import { createCustomProviderNodeKey } from "@/settings/model-provider-section/utils.js";
import {
  sortModelProvidersForDisplay,
  type ProviderOrderView,
} from "@/lib/modelProviderOrdering.js";

interface UseModelProviderNavigationOptions {
  modelProviders: ProviderSettingsFormProvider[];
  displayOrder?: ProviderOrderView;
  selectedNodeKey: string | null;
  setSelectedNodeKey: (key: string | null) => void;
  intl: ReturnType<typeof useNexIntl>["intl"];
}

export function useModelProviderNavigation({
  modelProviders,
  displayOrder,
  selectedNodeKey,
  setSelectedNodeKey,
  intl,
}: UseModelProviderNavigationOptions) {
  const navigationGroups = useMemo<ModelProviderNavGroup[]>(() => {
    const customProviders = sortModelProvidersForDisplay(
      modelProviders.filter((provider) => provider.config.group === "standard-personal"),
      // 复用模型菜单的展示排序，确保设置页和聊天框供应商顺序一致。
      displayOrder,
    );
    return [
      {
        id: "custom",
        title: intl.formatMessage({ id: "settings.modelProvider.customTitle" }),
        items: customProviders.map((provider) => ({
          key: createCustomProviderNodeKey(provider.providerId),
          type: "custom" as const,
          label: getProviderFormLabel(provider),
          provider,
          statusActive: provider.executable === true,
        })),
      },
    ];
    // 分组标题在 memo 内格式化；语言切换时必须依赖 intl 才能刷新旧 locale 的文案。
  }, [displayOrder, intl, modelProviders]);

  const navigationItems = useMemo(
    () => navigationGroups.flatMap((group) => group.items),
    [navigationGroups],
  );
  const selectedNavItem = selectedNodeKey
    ? (navigationItems.find((item) => item.key === selectedNodeKey) ?? null)
    : null;
  const fallbackNodeKey = navigationItems[0]?.key ?? null;

  useEffect(() => {
    if (selectedNavItem) {
      return;
    }
    if (selectedNodeKey !== fallbackNodeKey) {
      setSelectedNodeKey(fallbackNodeKey);
    }
  }, [fallbackNodeKey, selectedNavItem, selectedNodeKey, setSelectedNodeKey]);

  return {
    navigationGroups,
    selectedNavItem,
  };
}
