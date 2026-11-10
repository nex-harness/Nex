import type { ProviderSettingsFormProvider } from "@/lib/providerSettingsFormTypes.js";

export interface ModelProviderNavItem {
  key: string;
  type: "custom";
  label: string;
  provider: ProviderSettingsFormProvider;
  statusActive: boolean;
}

export interface ModelProviderNavGroup {
  id: "custom";
  title: string;
  items: ModelProviderNavItem[];
}
