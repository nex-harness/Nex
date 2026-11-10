import {
  buildRuntimeNexEndpointUrls,
  NEX_ENV,
  type RuntimeNexEndpointEnv,
} from "@nex/shared";

interface RendererImportMetaEnv {
  VITE_NEX_BASE_URL?: string;
  VITE_NEX_ENDPOINT_ORIGIN?: string;
}

function readRendererImportMetaEnv(): RendererImportMetaEnv {
  return ((import.meta as ImportMeta & { env?: RendererImportMetaEnv }).env ??
    {}) as RendererImportMetaEnv;
}

export function createRendererNexEndpointEnv(
  env: RendererImportMetaEnv = readRendererImportMetaEnv(),
): RuntimeNexEndpointEnv {
  return {
    NEX_ENV,
    // UI 侧的 nex-plan 占位 provider 以前只看 NEX_ENV，
    // 没有消费 Vite 注入的 base url，导致自定义测试域名时 renderer 和 host/service 可能不一致。
    NEX_BASE_URL: env.VITE_NEX_BASE_URL,
    NEX_ENDPOINT_ORIGIN: env.VITE_NEX_ENDPOINT_ORIGIN,
  };
}

export const RENDERER_NEX_ENDPOINT_URLS = buildRuntimeNexEndpointUrls(
  createRendererNexEndpointEnv(),
);
