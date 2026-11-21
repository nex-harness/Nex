import { formatMsg, type LocaleCode, type MessageKey } from '../i18n/messages';
import { ModelMeta, ThinkingEffort } from './types';

export interface EffortLevelOption {
  value: ThinkingEffort;
  labelKey?: MessageKey;
  customLabel?: string;
  rank: number;
}

const EFFORT_CATALOG: Record<string, EffortLevelOption> = {
  off: { value: '', labelKey: 'chat.thought.off', rank: 0 },
  none: { value: '', labelKey: 'chat.thought.off', rank: 0 },
  '': { value: '', labelKey: 'chat.thought.off', rank: 0 },
  minimal: { value: 'minimal', labelKey: 'chat.thought.minimal', rank: 1 },
  low: { value: 'low', labelKey: 'chat.thought.low', rank: 2 },
  medium: { value: 'medium', labelKey: 'chat.thought.medium', rank: 3 },
  on: { value: 'on', labelKey: 'chat.thought.on', rank: 3 },
  high: { value: 'high', labelKey: 'chat.thought.high', rank: 4 },
  xhigh: { value: 'xhigh', labelKey: 'chat.thought.xhigh', rank: 5 },
  max: { value: 'max', labelKey: 'chat.thought.max', rank: 6 },
};

const DEFAULT_EFFORT_LEVELS: EffortLevelOption[] = [
  EFFORT_CATALOG.off,
  EFFORT_CATALOG.low,
  EFFORT_CATALOG.medium,
  EFFORT_CATALOG.high,
];

export function formatEffortLabel(opt: EffortLevelOption | undefined, locale: LocaleCode): string {
  if (!opt) return '';
  if (opt.labelKey) return formatMsg(locale, opt.labelKey);
  return opt.customLabel || String(opt.value || '');
}

function canonicalEffortRank(val: string): number {
  const lower = (val || '').trim().toLowerCase();
  const known = EFFORT_CATALOG[lower];
  return known ? known.rank : -1;
}

export function resolveEffortLevels(model?: ModelMeta): EffortLevelOption[] {
  const raw = model?.reasoning_levels;
  if (model && !model.supports_reasoning && (!raw || raw.length === 0)) {
    return [];
  }
  if (!raw || raw.length === 0) return DEFAULT_EFFORT_LEVELS;
  const seen = new Set<string>();
  const resolved: EffortLevelOption[] = [];
  for (let i = 0; i < raw.length; i++) {
    const clean = raw[i].trim();
    if (!clean) continue;
    const lower = clean.toLowerCase();
    const known = EFFORT_CATALOG[lower];
    const entry: EffortLevelOption = known
      ? known
      : {
          value: clean,
          customLabel: clean.charAt(0).toUpperCase() + clean.slice(1),
          // Interpolate rank from position so custom words (e.g. turbo/ultra) clamp cleanly.
          rank: Math.max(1, Math.round((i / Math.max(1, raw.length - 1)) * 6)),
        };
    if (seen.has(entry.value)) continue;
    seen.add(entry.value);
    resolved.push(entry);
  }
  return resolved.length >= 1 ? resolved : DEFAULT_EFFORT_LEVELS;
}

export function clampEffortIndex(levels: EffortLevelOption[], requested: ThinkingEffort): number {
  if (levels.length === 0) return 0;
  const exactIdx = levels.findIndex((o) => o.value === requested);
  if (exactIdx >= 0) return exactIdx;

  const reqRank = canonicalEffortRank(String(requested));
  if (reqRank === 0) {
    const offIdx = levels.findIndex((o) => o.rank === 0);
    return offIdx >= 0 ? offIdx : 0;
  }

  // Clamp by canonical rank: find the highest supported non-off level <= reqRank (or closest positive level)
  if (reqRank > 0) {
    let bestIdx = -1;
    let bestRank = -1;
    for (let i = 0; i < levels.length; i++) {
      if (levels[i].rank > 0 && levels[i].rank <= reqRank && levels[i].rank > bestRank) {
        bestRank = levels[i].rank;
        bestIdx = i;
      }
    }
    if (bestIdx >= 0) return bestIdx;
    const firstPos = levels.findIndex((o) => o.rank > 0);
    if (firstPos >= 0) return firstPos;
  }

  const medIdx = levels.findIndex((o) => o.value === 'medium' || o.value === 'on');
  return medIdx >= 0 ? medIdx : Math.min(1, levels.length - 1);
}

/**
 * Resolves the exact wire effort value to send for a model so the backend always
 * receives the level shown on the Composer Effort slider.
 */
export function resolveEffectiveEffort(model: ModelMeta | undefined, requested: ThinkingEffort): ThinkingEffort {
  const levels = resolveEffortLevels(model);
  if (levels.length === 0) return '';
  if (levels.length === 1) return levels[0]?.value ?? '';
  const idx = clampEffortIndex(levels, requested);
  return levels[idx]?.value ?? '';
}
