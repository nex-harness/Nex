import { NEX_VERSION, type NexEnv } from "@nex/shared";

declare const __NEX_REMOTE_CDN_RELEASE_ROOTS__: readonly string[] | undefined;

// 远程 Server 资产与 App 同一个 GitHub Release：.../releases/download/v<版本>/。
// 详见 docs/specs/remote-server-release-assets.md。
const GITHUB_RELEASE_DOWNLOAD_ROOT = "https://github.com/nex-harness/Nex/releases/download";
const TEST_REMOTE_CDN_RELEASE_ROOT = "http://intranet.example.invalid:12345/ssh-remote-assets";

export interface ResolveRemoteCdnOptions {
  env?: NexEnv;
  overrideBaseUrl?: string;
  version?: string;
}

export function resolveRemoteCdnBaseUrls(options: ResolveRemoteCdnOptions = {}): string[] {
  const overrideBaseUrl = options.overrideBaseUrl?.trim();
  if (overrideBaseUrl) {
    return [normalizeBaseUrl(overrideBaseUrl)];
  }

  const version = options.version ?? NEX_VERSION;

  if (options.env === "test") {
    return [`${TEST_REMOTE_CDN_RELEASE_ROOT}/${version}`];
  }

  const configuredReleaseRoots =
    typeof __NEX_REMOTE_CDN_RELEASE_ROOTS__ === "undefined" ? [] : __NEX_REMOTE_CDN_RELEASE_ROOTS__;
  if (configuredReleaseRoots.length > 0) {
    // 构建期可以用 CI 的 CDN_DOMAIN/OSS_PATH_PREFIX 注入有序列表覆盖默认来源。
    return configuredReleaseRoots.map(
      (releaseRoot) => `${normalizeBaseUrl(releaseRoot)}/${version}`,
    );
  }

  // GitHub 的下载路径把版本放在 tag 里（v<版本>），不是 <根>/<版本>。
  return [`${GITHUB_RELEASE_DOWNLOAD_ROOT}/v${version}`];
}

export function resolvePrimaryRemoteCdnBaseUrl(
  options: ResolveRemoteCdnOptions = {},
): string | undefined {
  return resolveRemoteCdnBaseUrls(options)[0];
}

function normalizeBaseUrl(baseUrl: string): string {
  return baseUrl.replace(/\/+$/, "");
}
