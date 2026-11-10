import type { IProviderSettingsService, ProviderSettingsView } from "@nex/services";

export async function persistPersonalProviderDeletion(params: {
  providerId: string;
  providerSettingsService: Pick<IProviderSettingsService, "deletePersonalProvider">;
}): Promise<ProviderSettingsView> {
  return params.providerSettingsService.deletePersonalProvider(params.providerId);
}
