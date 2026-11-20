import { enUS, type MessageKey } from './locales/en-US';
import { zhCN } from './locales/zh-CN';

export type { MessageKey };

export interface LocaleDefinition {
  readonly code: LocaleCode;
  readonly label: string;
  readonly nativeName: string;
}

/**
 * Single registry for all supported UI languages.
 * Adding a new language (e.g. `ja-JP`):
 *   1. Add `clients/web/src/i18n/locales/ja-JP.ts` with `satisfies Record<MessageKey, string>`
 *   2. Register it in `LOCALE_DICTS` and `SUPPORTED_LOCALES` below.
 */
export const LOCALE_DICTS = {
  'zh-CN': zhCN,
  'en-US': enUS,
} as const satisfies Record<string, Record<MessageKey, string>>;

export type LocaleCode = keyof typeof LOCALE_DICTS;

export const SUPPORTED_LOCALES: readonly LocaleDefinition[] = [
  { code: 'zh-CN', label: '简体中文', nativeName: '简体中文' },
  { code: 'en-US', label: 'English', nativeName: 'English' },
] as const;

export const DEFAULT_LOCALE: LocaleCode = 'zh-CN';
export const FALLBACK_LOCALE: LocaleCode = 'en-US';

export function isSupportedLocale(value: unknown): value is LocaleCode {
  return typeof value === 'string' && Object.prototype.hasOwnProperty.call(LOCALE_DICTS, value);
}

/**
 * Resolves the best matching supported locale from browser settings (e.g. `en-GB` -> `en-US`, `zh-Hans` -> `zh-CN`).
 */
export function detectBrowserLocale(saved?: string | null): LocaleCode {
  if (isSupportedLocale(saved)) return saved;
  if (typeof navigator !== 'undefined') {
    const candidates = [
      ...(Array.isArray(navigator.languages) ? navigator.languages : []),
      navigator.language,
    ].filter((x): x is string => Boolean(x));

    for (const raw of candidates) {
      const normalized = raw.trim();
      if (isSupportedLocale(normalized)) return normalized;
      const lower = normalized.toLowerCase();
      for (const item of SUPPORTED_LOCALES) {
        if (item.code.toLowerCase() === lower) return item.code;
      }
      const primary = lower.split('-')[0];
      for (const item of SUPPORTED_LOCALES) {
        if (item.code.toLowerCase().split('-')[0] === primary) return item.code;
      }
    }
  }
  return DEFAULT_LOCALE;
}

export function applyDocumentLocale(locale: LocaleCode): void {
  if (typeof document !== 'undefined' && document.documentElement) {
    document.documentElement.lang = locale;
  }
}

export function formatMsg(
  locale: LocaleCode,
  key: MessageKey,
  vars?: Record<string, string | number>,
): string {
  const dict = LOCALE_DICTS[locale] ?? LOCALE_DICTS[FALLBACK_LOCALE];
  let template: string = dict[key] ?? LOCALE_DICTS[FALLBACK_LOCALE][key] ?? key;
  if (vars) {
    for (const [k, v] of Object.entries(vars)) {
      template = template.replaceAll(`{${k}}`, String(v));
    }
  }
  return template;
}

export function formatRelativeTimeShort(ts: number, locale: LocaleCode): string {
  if (!ts) return '';
  const diffSec = Math.max(0, Math.floor((Date.now() - ts) / 1000));
  if (diffSec < 60) {
    return formatMsg(locale, 'common.justNow');
  }
  if (typeof Intl !== 'undefined' && typeof Intl.RelativeTimeFormat === 'function') {
    try {
      const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto', style: 'narrow' });
      if (diffSec < 3600) return rtf.format(-Math.floor(diffSec / 60), 'minute');
      if (diffSec < 86400) return rtf.format(-Math.floor(diffSec / 3600), 'hour');
      return rtf.format(-Math.floor(diffSec / 86400), 'day');
    } catch {
      // fall back below
    }
  }
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`;
  return `${Math.floor(diffSec / 86400)}d`;
}
