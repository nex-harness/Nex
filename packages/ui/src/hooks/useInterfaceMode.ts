import { useNexStoreWithDefault } from "@/store/StoreProvider.js";

export function useIsOfficeMode(): boolean {
  return useNexStoreWithDefault((state) => state.interfaceMode === "office", false);
}
