import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import { buildRewardsUrl, resolveRewardsOrigin, NEX_ENV } from "@nex/shared";
import { usePlatform } from "@/hooks/usePlatform.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { useNexStore } from "@/store/StoreProvider.js";
import { resolveTheme } from "@/useTheme.js";
import { RewardsWebview } from "@/rewards/RewardsWebview.js";

const RewardsContext = createContext<(() => Promise<void>) | null>(null);
export const useOpenRewards = () => useContext(RewardsContext);
export function RewardsProvider({
  children,
  desktop,
}: {
  children: ReactNode;
  desktop: boolean;
}) {
  const [opened, setOpened] = useState(false);
  const platform = usePlatform();
  const { locale } = useNexIntl();
  const theme = useNexStore((s) => s.theme);
  const openPage = useCallback(async () => {
    if (desktop) setOpened(true);
    else
      await platform.openExternal(
        buildRewardsUrl(
          resolveRewardsOrigin({ env: NEX_ENV }),
          locale,
          resolveTheme(theme) === "dark" ? "zai-dark" : "zai-light",
        ),
      );
  }, [desktop, platform, locale, theme]);
  const open = useCallback(async () => {
    await openPage();
  }, [openPage]);
  return (
    <RewardsContext.Provider value={open}>
      {children}
      {opened && <RewardsWebview onClose={() => setOpened(false)} />}
    </RewardsContext.Provider>
  );
}
