import { createConfig } from "@nex/adapters/config";
import type { RuntimeConfigPatch } from "@nex/contracts";
import { resolveLocale, type SupportedLocale } from "@nex/i18n";
import type { GlobalOptions } from "@nex/shared-types";
import type { RunDependencies } from "./cli-types.js";

type ResolveTuiStartupLocaleInput = {
  deps: RunDependencies;
  options: GlobalOptions;
  workingDirectory: string;
};

const localeCliOverrides = (locale: GlobalOptions["locale"]): RuntimeConfigPatch | undefined =>
  locale
    ? {
        ui: {
          locale,
        },
      }
    : undefined;

export function resolveTuiStartupLocale({
  deps,
  options,
  workingDirectory,
}: ResolveTuiStartupLocaleInput): SupportedLocale {
  const configResult = createConfig({
    cliOverrides: localeCliOverrides(options.locale),
    env: deps.env ?? process.env,
    projectConfigPath: deps.projectConfigPath,
    skipUserConfig: deps.skipUserConfig,
    userConfigPath: deps.userConfigPath,
    workingDirectory,
  });

  // TUI 启动时 NexApp 可能尚未就绪，CLI 边界自己解析持久化的 ui.locale。
  return resolveLocale(configResult.config.ui.locale, options.detectedLocale);
}
