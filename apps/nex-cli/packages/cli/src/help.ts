import { getNexCopy, type SupportedLocale, type UiLocale } from "@nex/i18n";

export function formatCliHelp(
  version: string,
  locale?: UiLocale,
  detectedLocale?: SupportedLocale,
): string {
  return getNexCopy(locale, detectedLocale).cli.help(version);
}
