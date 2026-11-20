import { useCallback } from 'react';
import { useNexStore } from '../core/store';
import { formatMsg, type MessageKey } from './index';

export function useT() {
  const locale = useNexStore((s) => s.locale);
  return useCallback(
    (key: MessageKey, vars?: Record<string, string | number>) => formatMsg(locale, key, vars),
    [locale],
  );
}
