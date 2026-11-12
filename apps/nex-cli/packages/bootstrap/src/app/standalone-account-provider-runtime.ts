import { createHash } from "node:crypto";
import type { SharedNexCredentialStore } from "@nex/adapters/auth";
import type { ProviderRuntimeHeadersPort } from "@nex/core";
import {
  createAccountProviderConfigSnapshot,
  ProviderConfig,
  ProviderConfigMap,
  ZhipuAccountAccessConfig,
  type AccountProviderConfigSnapshot,
  type ProviderConfigLayerSnapshot,
} from "@nex/provider";
import {
  NodeNexBuiltinProviderConfigSource,
  NEX_BUILTIN_PROVIDER_CONFIG_FILE_ENV,
} from "@nex/provider-node";
import type { ProviderFamilyDomain } from "@nex/shared";

// Nex 无账号体系：OAuth 登录与请求期换证（api.z.ai / bigmodel.cn 项目 Token）已移除。
// 这里只保留 Coding Plan Provider 的手工 API Key 连接方式；用户在 Provider 设置里粘贴 Key，
// 不读取账号 access token，也不向账号服务换取短期 Token。

export interface StandaloneCodingPlanProvider {
  readonly family: ProviderFamilyDomain;
  readonly modelId: string;
  readonly providerId: string;
}

export async function readStandaloneCodingPlanProviders(
  env: Readonly<Record<string, string | undefined>>,
): Promise<readonly StandaloneCodingPlanProvider[]> {
  return (await readStandaloneCodingPlanCatalog(env)).providers;
}

async function readStandaloneCodingPlanCatalog(
  env: Readonly<Record<string, string | undefined>>,
  config?: Pick<ProviderConfigLayerSnapshot, "revision" | "providers">,
): Promise<{
  readonly nexBuiltinRevision: string;
  readonly providers: readonly StandaloneCodingPlanProvider[];
}> {
  if (config)
    return {
      nexBuiltinRevision: config.revision,
      providers: config.providers.entries().flatMap(([providerId, provider]) => {
        const access = provider.access;
        const modelId = provider.builtinModelIds?.find((candidate) => candidate.trim())?.trim();
        return access?.type === "zhipu-account" &&
          access.mode === "individual-coding-plan" &&
          access.accountType &&
          modelId
          ? [{ family: access.accountType, modelId, providerId }]
          : [];
      }),
    };
  const filePath = env[NEX_BUILTIN_PROVIDER_CONFIG_FILE_ENV]?.trim();
  if (!filePath) {
    throw new Error(`${NEX_BUILTIN_PROVIDER_CONFIG_FILE_ENV} is required`);
  }
  const source = new NodeNexBuiltinProviderConfigSource({
    bundledFilePath: filePath,
    watch: false,
  });
  try {
    const snapshot = await source.read();
    return readStandaloneCodingPlanCatalog(env, snapshot);
  } finally {
    source.dispose();
  }
}

export async function resolveStandaloneCodingPlanProvider(
  family: ProviderFamilyDomain,
  env: Readonly<Record<string, string | undefined>>,
): Promise<StandaloneCodingPlanProvider> {
  const matches = (await readStandaloneCodingPlanProviders(env)).filter(
    (provider) => provider.family === family,
  );
  if (matches.length !== 1) {
    throw new Error(
      `Nex Built-in Config 必须为 ${family} 声明唯一 Individual Coding Plan Provider`,
    );
  }
  return matches[0]!;
}

export function standaloneAccountIdentityCredentialKey(providerId: string): string {
  const normalized = providerId.trim();
  if (!normalized) throw new Error("Standalone Account Provider ID 不能为空");
  return `account-provider:${normalized}:identity`;
}

export function standaloneAccountAuthSourceCredentialKey(providerId: string): string {
  return `account-provider:${providerId}:auth-source`;
}

/** Standalone Credential Store 私有键；不得进入 Provider Config、Model 或 Protocol。 */
export function standaloneAccountProviderCredentialKey(input: {
  readonly providerId: string;
  readonly accountIdentity: string;
}): string {
  const providerId = input.providerId.trim();
  const accountIdentity = input.accountIdentity.trim();
  if (!providerId) throw new Error("Standalone Account Provider ID 不能为空");
  if (!accountIdentity) throw new Error("Standalone Account Identity 不能为空");
  return `account-provider:coding-plan:${providerId}:account:${encodeURIComponent(accountIdentity)}:api-key`;
}

/** 手工 Key 连接使用稳定、不可逆的连接身份。 */
export function createStandaloneAccountIdentityFromSecret(secret: string): string {
  const normalized = secret.trim();
  if (!normalized) throw new Error("Standalone Account Secret 不能为空");
  return `key-${createHash("sha256").update(normalized).digest("hex").slice(0, 24)}`;
}

export async function readStandaloneAccountProviderConfigSnapshot(
  credentialStore: Pick<SharedNexCredentialStore, "loadMany">,
  env: Readonly<Record<string, string | undefined>>,
  config?: Pick<ProviderConfigLayerSnapshot, "revision" | "providers">,
): Promise<AccountProviderConfigSnapshot> {
  const catalog = await readStandaloneCodingPlanCatalog(env, config);
  const configuredProviders = catalog.providers;
  const identityKeys = configuredProviders.map(({ providerId }) =>
    standaloneAccountIdentityCredentialKey(providerId),
  );
  const identities = await credentialStore.loadMany(identityKeys);
  const candidates = configuredProviders.flatMap(({ providerId }) => {
    const accountIdentity = identities[standaloneAccountIdentityCredentialKey(providerId)]?.trim();
    if (!accountIdentity) return [];
    const credentialKey = standaloneAccountProviderCredentialKey({
      providerId,
      accountIdentity,
    });
    return [{ accountIdentity, credentialKey, providerId }] as const;
  });
  const credentials = await credentialStore.loadMany(
    candidates.map(({ credentialKey }) => credentialKey),
  );
  const candidateByProviderId = new Map(
    candidates.map((candidate) => [candidate.providerId, candidate]),
  );
  const providers = new ProviderConfigMap(
    configuredProviders.map(({ providerId }) => {
      const candidate = candidateByProviderId.get(providerId);
      const authorized = Boolean(
        candidate && credentials[candidate.credentialKey]?.trim(),
      );
      if (!authorized) {
        // 账号 Overlay 缺少成员表示“不覆盖”，不能表达账号已断开；必须显式
        // entitled=false，才能让 Built-in 账号 Provider 在凭据删除后从 Registry 退出。
        return [
          providerId,
          new ProviderConfig({
            access: new ZhipuAccountAccessConfig({ entitled: false }),
          }),
        ] as const;
      }
      return [
        providerId,
        new ProviderConfig({
          access: new ZhipuAccountAccessConfig({ entitled: true }),
        }),
      ] as const;
    }),
  );
  return createAccountProviderConfigSnapshot(catalog.nexBuiltinRevision, providers);
}

export async function hasStandaloneCodingPlanAccess(
  credentialStore: Pick<SharedNexCredentialStore, "loadMany">,
  env: Readonly<Record<string, string | undefined>>,
): Promise<boolean> {
  return (await readStandaloneAccountProviderConfigSnapshot(credentialStore, env)).providers
    .entries()
    .some(([, provider]) =>
      provider.access?.type === "zhipu-account" ? provider.access.entitled === true : false,
    );
}

export function createStandaloneProviderRuntimeHeadersPort(
  credentialStore: Pick<SharedNexCredentialStore, "load">,
): ProviderRuntimeHeadersPort {
  return {
    shouldRefreshBeforeModelRequest() {
      return true;
    },
    async refreshBeforeModelRequest(input) {
      input.abortSignal?.throwIfAborted();
      const providerId = input.providerId.trim();
      const access = input.accountAccess;
      if (!access || access.mode !== "individual-coding-plan") {
        throw new Error(`Standalone Account Provider 请求身份无效: ${providerId}`);
      }
      const currentIdentity = (
        await credentialStore.load(standaloneAccountIdentityCredentialKey(providerId))
      )?.trim();
      if (!currentIdentity) {
        throw new Error(`Standalone Account Provider 凭据已经失效: ${providerId}`);
      }
      const apiKey = (
        await credentialStore.load(
          standaloneAccountProviderCredentialKey({
            providerId,
            accountIdentity: currentIdentity,
          }),
        )
      )?.trim();
      input.abortSignal?.throwIfAborted();
      if (!apiKey) {
        throw new Error(`Standalone Account Provider 缺少请求凭据: ${providerId}`);
      }
      return {
        headersApplied: true,
        requestAuth: { apiKey },
      };
    },
  };
}
