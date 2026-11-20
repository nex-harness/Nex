/// <reference types="vite/client" />
import { describe, expect, it } from 'vitest';
import {
  DEFAULT_LOCALE,
  FALLBACK_LOCALE,
  LOCALE_DICTS,
  SUPPORTED_LOCALES,
  detectBrowserLocale,
  formatMsg,
  formatRelativeTimeShort,
  isSupportedLocale,
  type LocaleCode,
  type MessageKey,
} from '../i18n';

function extractPlaceholders(template: string): string[] {
  const matches = template.match(/\{([a-zA-Z0-9_]+)\}/g) || [];
  return Array.from(new Set(matches)).sort();
}

const RAW_SRC_FILES = import.meta.glob<string>(['../**/*.ts', '../**/*.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
});

describe('i18n architecture & locale parity', () => {
  it('registers every SUPPORTED_LOCALES entry in LOCALE_DICTS with 100% key and placeholder parity', () => {
    const canonical = LOCALE_DICTS[FALLBACK_LOCALE];
    const canonicalKeys = Object.keys(canonical).sort() as MessageKey[];
    expect(canonicalKeys.length).toBeGreaterThan(500);

    const registeredCodes = Object.keys(LOCALE_DICTS).sort();
    const supportedCodes = SUPPORTED_LOCALES.map((item) => item.code).sort();
    expect(supportedCodes).toEqual(registeredCodes);

    for (const { code } of SUPPORTED_LOCALES) {
      const dict = LOCALE_DICTS[code];
      expect(dict, `Missing dictionary for ${code}`).toBeDefined();

      const keys = Object.keys(dict).sort();
      const missing = canonicalKeys.filter((k) => !(k in dict));
      const extra = keys.filter((k) => !(k in canonical));
      expect(missing, `Missing keys in ${code}`).toEqual([]);
      expect(extra, `Extra keys in ${code}`).toEqual([]);

      for (const key of canonicalKeys) {
        const val = dict[key];
        expect(typeof val, `${code}.${key} must be a string`).toBe('string');
        expect(val.trim().length, `${code}.${key} must not be empty`).toBeGreaterThan(0);

        const expectedPlaceholders = extractPlaceholders(canonical[key]);
        const actualPlaceholders = extractPlaceholders(val);
        expect(
          actualPlaceholders,
          `Placeholder mismatch for ${code}.${key}: expected ${expectedPlaceholders.join(',')}, got ${actualPlaceholders.join(',')}`,
        ).toEqual(expectedPlaceholders);
      }
    }
  });

  it('resolves saved and browser locales cleanly via detectBrowserLocale', () => {
    expect(isSupportedLocale('zh-CN')).toBe(true);
    expect(isSupportedLocale('en-US')).toBe(true);
    expect(isSupportedLocale('fr-FR')).toBe(false);

    // Saved valid locale always wins
    expect(detectBrowserLocale('en-US')).toBe('en-US');
    expect(detectBrowserLocale('zh-CN')).toBe('zh-CN');

    const origLanguages = Object.getOwnPropertyDescriptor(window.navigator, 'languages');
    const origLanguage = Object.getOwnPropertyDescriptor(window.navigator, 'language');
    try {
      Object.defineProperty(window.navigator, 'languages', { value: ['en-GB', 'fr-FR'], configurable: true });
      Object.defineProperty(window.navigator, 'language', { value: 'en-GB', configurable: true });
      expect(detectBrowserLocale(null)).toBe('en-US');

      Object.defineProperty(window.navigator, 'languages', { value: ['zh-Hans-CN'], configurable: true });
      Object.defineProperty(window.navigator, 'language', { value: 'zh-Hans-CN', configurable: true });
      expect(detectBrowserLocale('')).toBe('zh-CN');

      Object.defineProperty(window.navigator, 'languages', { value: ['de-DE'], configurable: true });
      Object.defineProperty(window.navigator, 'language', { value: 'de-DE', configurable: true });
      expect(detectBrowserLocale('invalid-locale')).toBe(DEFAULT_LOCALE);
    } finally {
      if (origLanguages) Object.defineProperty(window.navigator, 'languages', origLanguages);
      if (origLanguage) Object.defineProperty(window.navigator, 'language', origLanguage);
    }
  });

  it('interpolates formatMsg variables and formats localized relative timestamps', () => {
    expect(formatMsg('en-US', 'runners.sshOk', { name: 'prod', os: 'linux', arch: 'amd64' })).toBe(
      'SSH OK: prod (linux/amd64)',
    );
    expect(formatMsg('zh-CN', 'runners.sshOk', { name: 'prod', os: 'linux', arch: 'amd64' })).toBe(
      'SSH 连接成功：prod (linux/amd64)',
    );

    const now = Date.now();
    expect(formatRelativeTimeShort(now - 5_000, 'en-US')).toBe(formatMsg('en-US', 'common.justNow'));
    expect(formatRelativeTimeShort(now - 5_000, 'zh-CN')).toBe(formatMsg('zh-CN', 'common.justNow'));
    expect(formatRelativeTimeShort(now - 300_000, 'en-US').length).toBeGreaterThan(0);
    expect(formatRelativeTimeShort(now - 300_000, 'zh-CN').length).toBeGreaterThan(0);
    expect(formatRelativeTimeShort(0, 'en-US')).toBe('');

    // Unknown runtime locale gracefully falls back to en-US
    expect(formatMsg('xx-XX' as LocaleCode, 'sidebar.newTask')).toBe('New task');
  });

  it('has zero hardcoded binary locale ternaries or union types outside src/i18n/', () => {
    const entries = Object.entries(RAW_SRC_FILES).filter(
      ([relPath]) => !relPath.startsWith('../i18n/') && !relPath.startsWith('../__tests__/'),
    );
    expect(entries.length).toBeGreaterThan(15);

    const forbidden: Array<{ label: string; regex: RegExp }> = [
      { label: 'inline locale equality check', regex: /\blocale\s*===?\s*['"](?:en-US|zh-CN)['"]/ },
      { label: 'const isEn helper', regex: /\bconst\s+isEn\b/ },
      { label: 'const isZh helper', regex: /\bconst\s+isZh\b/ },
      { label: 'hardcoded binary locale union', regex: /['"]zh-CN['"]\s*\|\s*['"]en-US['"]|['"]en-US['"]\s*\|\s*['"]zh-CN['"]/ },
    ];

    const violations: string[] = [];
    for (const [relPath, text] of entries) {
      for (const rule of forbidden) {
        if (rule.regex.test(text)) {
          violations.push(`${relPath}: matched ${rule.label}`);
        }
      }
    }
    expect(violations).toEqual([]);
  });
});
