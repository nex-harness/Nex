import {
  createSharedNexCredentialStore,
  SHARED_NEX_CREDENTIAL_KEYS,
  type SharedNexCredentialStore,
} from "@nex/adapters";
import type { EnvRecord } from "@nex/adapters/model";
import {
  NodeModelSelectionConfigRepository,
  NodePersonalProviderConfigRepository,
  PERSONAL_PROVIDER_CONFIG_FILE_NAME,
  NEX_PERSONAL_PROVIDER_CONFIG_FILE_ENV,
} from "@nex/provider-node";
import { dirname, join } from "node:path";
import type {
  CodingPlanProviderId,
  LogoutNexCliOptions,
  LogoutNexCliResult,
} from "./auth-login-contract.js";
import { readLegacyCliPersonalProviderConfig } from "./app/legacy-cli-personal-provider-config-importer.js";
import {
  hasStandaloneCodingPlanAccess,
  readStandaloneCodingPlanProviders,
  resolveStandaloneCodingPlanProvider,
  standaloneAccountIdentityCredentialKey,
  standaloneAccountAuthSourceCredentialKey,
  standaloneAccountProviderCredentialKey,
} from "./app/standalone-account-provider-runtime.js";

export interface StandaloneCodingPlanPersistenceResult {
  readonly mainModel: string;
  readonly path: string;
}

export async function persistStandaloneCodingPlanConnection(input: {
  readonly accountIdentity: string;
  readonly apiKey?: string;
  readonly authSource?: "oauth";
  readonly credentialStore: SharedNexCredentialStore;
  readonly env: EnvRecord;
  readonly personalProviderConfigPath?: string;
  readonly providerId: CodingPlanProviderId;
}): Promise<StandaloneCodingPlanPersistenceResult> {
  const configuredProvider = await resolveStandaloneCodingPlanProvider(input.providerId, input.env);
  const providerId = configuredProvider.providerId;
  const modelId = configuredProvider.modelId;
  const credentialKey = standaloneAccountProviderCredentialKey({
    providerId,
    accountIdentity: input.accountIdentity,
  });
  await input.credentialStore.saveMany({
    [standaloneAccountIdentityCredentialKey(providerId)]: input.accountIdentity,
    [standaloneAccountAuthSourceCredentialKey(providerId)]: input.authSource ?? "manual",
    ...(input.apiKey ? { [credentialKey]: input.apiKey } : {}),
  });
  const path =
    input.personalProviderConfigPath ??
    input.env[NEX_PERSONAL_PROVIDER_CONFIG_FILE_ENV]?.trim() ??
    join(dirname(input.credentialStore.filePath), PERSONAL_PROVIDER_CONFIG_FILE_NAME);
  // 登录与运行时共享文件和事务；首次写入仍先保留旧用户 Provider，不能仅写默认值。
  const personalRepository = new NodePersonalProviderConfigRepository({
    filePath: path,
    importLegacy: () => readLegacyCliPersonalProviderConfig({}),
    pollingIntervalMs: false,
  });
  const repository = new NodeModelSelectionConfigRepository({
    personalRepository,
  });
  try {
    await repository.saveConfiguredDefault({ providerId, modelId });
  } finally {
    repository.dispose();
    personalRepository.dispose();
  }
  return {
    mainModel: `${providerId}/${modelId}`,
    path,
  };
}

export async function hasConfiguredStandaloneCodingPlan(
  options: {
    credentialStore?: SharedNexCredentialStore;
    env?: EnvRecord;
  } = {},
): Promise<boolean> {
  const credentialStore =
    options.credentialStore ?? createSharedNexCredentialStore({ env: options.env });
  return hasStandaloneCodingPlanAccess(credentialStore, options.env ?? process.env);
}

export async function logoutNexCli(
  options: LogoutNexCliOptions = {},
): Promise<LogoutNexCliResult> {
  const credentialStore =
    options.credentialStore ?? createSharedNexCredentialStore({ env: options.env });
  const providerIds = (await readStandaloneCodingPlanProviders(options.env ?? process.env)).map(
    ({ providerId }) => providerId,
  );
  const identityKeys = providerIds.map(standaloneAccountIdentityCredentialKey);
  const sourceKeys = providerIds.map(standaloneAccountAuthSourceCredentialKey);
  const identities = await credentialStore.loadMany([...identityKeys, ...sourceKeys]);
  const dynamicApiKeyKeys = providerIds.flatMap((providerId) => {
    // OAuth 退出不能解密旧派生 Key；坏密文不应阻止清理当前登录态。
    if (identities[standaloneAccountAuthSourceCredentialKey(providerId)] !== "manual") return [];
    const identity = identities[standaloneAccountIdentityCredentialKey(providerId)]?.trim();
    return identity
      ? [
          standaloneAccountProviderCredentialKey({
            providerId,
            accountIdentity: identity,
          }),
        ]
      : [];
  });
  const keys = [
    ...Object.values(SHARED_NEX_CREDENTIAL_KEYS),
    ...identityKeys,
    ...sourceKeys,
    ...dynamicApiKeyKeys,
  ];
  const current = await credentialStore.loadMany(keys);
  await credentialStore.deleteIfValues(
    Object.fromEntries(
      Object.entries(current).flatMap(([key, value]) => (value === null ? [] : [[key, value]])),
    ),
  );
  return {
    credentialsPath: credentialStore.filePath,
  };
}
