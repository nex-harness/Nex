import { Check, Copy, Keyboard, X } from 'lucide-react';
import { useEffect, useState } from 'react';
import { apiFetch } from '../core/protocol';
import { useNexStore } from '../core/store';
import { formatMsg } from '../i18n/messages';

interface HealthBuildInfo {
  version?: string;
  build_sha?: string;
  is_dev?: boolean;
}

/** Nex squircle brand mark aligned with clients/app/resources/launch-logo.png. */
export function NexLogo({ className = 'size-12' }: { className?: string }) {
  return (
    <div
      aria-hidden="true"
      className={`flex shrink-0 items-center justify-center rounded-[14px] bg-foreground text-background shadow-sm ${className}`}
    >
      <svg viewBox="0 0 64 64" fill="none" className="size-[62%]">
        <path
          d="M19 47V17L45 47V17"
          stroke="currentColor"
          strokeWidth="7.5"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </div>
  );
}

export function AboutModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const locale = useNexStore((s) => s.locale);
  const [health, setHealth] = useState<HealthBuildInfo | null>(null);
  const [copiedSha, setCopiedSha] = useState(false);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    apiFetch<HealthBuildInfo>('/api/v1/health')
      .then((res) => {
        if (!cancelled && res && typeof res === 'object') {
          setHealth(res);
        }
      })
      .catch(() => {
        if (!cancelled) setHealth(null);
      });

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => {
      cancelled = true;
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [open, onClose]);

  if (!open) return null;

  const fallbackVersion = typeof __NEX_APP_VERSION__ !== 'undefined' ? __NEX_APP_VERSION__ : 'dev';
  const fallbackSha = typeof __NEX_BUILD_SHA__ !== 'undefined' ? __NEX_BUILD_SHA__ : '';
  const fallbackIsDev = typeof __NEX_IS_DEV__ !== 'undefined' ? __NEX_IS_DEV__ : false;

  const displayVersion = (health?.version || '').trim() || fallbackVersion;
  const displaySha = (health?.build_sha || '').trim() || fallbackSha;
  const lowerVer = displayVersion.toLowerCase();
  const isDebug =
    Boolean(health?.is_dev ?? fallbackIsDev) ||
    lowerVer === 'dev' ||
    lowerVer.includes('-dev') ||
    lowerVer.includes('-dirty') ||
    lowerVer.includes('debug');

  const shortcuts: Array<{ label: string; keys: string }> = [
    { label: formatMsg(locale, 'chat.sendPrompt'), keys: 'Enter' },
    { label: formatMsg(locale, 'chat.newLine'), keys: 'Shift+Enter' },
    { label: formatMsg(locale, 'chat.cycleReasoningEffort'), keys: '^ T' },
    { label: formatMsg(locale, 'chat.referenceFile'), keys: '@' },
    { label: formatMsg(locale, 'about.slashCommands'), keys: '/' },
    { label: formatMsg(locale, 'chat.steerRunningAgent'), keys: 'Enter' },
    { label: formatMsg(locale, 'about.commandPalette'), keys: '⌘ K' },
  ];

  const handleCopySha = () => {
    if (!displaySha) return;
    void navigator.clipboard?.writeText(displaySha);
    setCopiedSha(true);
    window.setTimeout(() => setCopiedSha(false), 1400);
  };

  return (
    <div
      data-testid="about-modal-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 px-4 backdrop-blur-[2px]"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={formatMsg(locale, 'about.title')}
        data-testid="about-modal"
        className="w-full max-w-[420px] overflow-hidden rounded-2xl border border-border bg-popover text-foreground shadow-2xl"
      >
        <div className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
          <div className="flex items-center gap-3.5">
            <NexLogo className="size-11" />
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <h2 data-testid="about-app-name" className="text-ui-lg font-semibold tracking-tight text-foreground">
                  {formatMsg(locale, 'about.appName')}
                </h2>
                {isDebug && (
                  <span
                    data-testid="about-debug-badge"
                    className="rounded-md border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 font-mono text-[11px] font-medium text-amber-600 dark:text-amber-400"
                  >
                    {formatMsg(locale, 'about.debugBadge')}
                  </span>
                )}
              </div>
              <p className="mt-0.5 text-ui-xs text-foreground-subtle">
                {formatMsg(locale, 'about.subtitle')}
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={formatMsg(locale, 'common.close')}
            className="flex size-7 shrink-0 items-center justify-center rounded-lg text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        </div>

        <div className="space-y-4 px-5 py-4">
          <div className="divide-y divide-border rounded-xl border border-border bg-surface/60">
            <div className="flex items-center justify-between gap-3 px-3.5 py-2.5 text-ui-sm">
              <span className="text-foreground-subtle">{formatMsg(locale, 'about.version')}</span>
              <span data-testid="about-version" className="font-mono font-medium text-foreground">
                {displayVersion}
              </span>
            </div>
            {isDebug && displaySha && (
              <div className="flex items-center justify-between gap-3 px-3.5 py-2.5 text-ui-sm">
                <span className="text-foreground-subtle">{formatMsg(locale, 'about.buildHash')}</span>
                <div className="flex items-center gap-1.5">
                  <code data-testid="about-build-sha" className="rounded bg-tag px-1.5 py-0.5 font-mono text-ui-xs text-foreground">
                    {displaySha}
                  </code>
                  <button
                    type="button"
                    onClick={handleCopySha}
                    title={copiedSha ? formatMsg(locale, 'about.copiedHash') : formatMsg(locale, 'about.copyHash')}
                    className="flex size-6 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                  >
                    {copiedSha ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
                  </button>
                </div>
              </div>
            )}
          </div>

          <div data-testid="about-shortcuts" className="space-y-2">
            <div className="flex items-center gap-1.5 text-ui-xs font-medium uppercase tracking-wider text-foreground-subtlest">
              <Keyboard className="size-3.5" />
              <span>{formatMsg(locale, 'about.shortcutsTitle')}</span>
            </div>
            <div className="divide-y divide-border/60 rounded-xl border border-border bg-card px-3.5 py-1">
              {shortcuts.map((sc, idx) => (
                <div key={idx} className="flex items-center justify-between gap-3 py-1.5 text-ui-sm">
                  <span className="text-foreground-subtle">{sc.label}</span>
                  <kbd className="rounded bg-tag px-1.5 py-0.5 font-mono text-ui-xs text-foreground">
                    {sc.keys}
                  </kbd>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
