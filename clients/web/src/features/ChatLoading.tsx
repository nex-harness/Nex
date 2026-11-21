import { useEffect, useState } from 'react';
import { formatMsg, formatRunningWorkDuration, type LocaleCode } from '../i18n/messages';

const ORBIT_ORDER = [0, 1, 2, 5, 8, 7, 6, 3];

/** 3x3 orbiting pixel indicator matching ZCode's ChatLoadingPixels. */
export function ChatLoadingPixels() {
  return (
    <span
      aria-hidden="true"
      className="grid size-[13px] shrink-0 grid-cols-3 grid-rows-3 gap-[1.3px] text-foreground"
    >
      {Array.from({ length: 9 }, (_, index) => (
        <span
          key={index}
          className="zcode-loading-pixel size-full rounded-[1px] bg-current"
          style={{
            opacity: index === 4 ? 0.07 : 0.15,
            animationName: index === 4 ? 'none' : undefined,
            animationDuration: '950ms',
            animationDelay: `${Math.max(0, ORBIT_ORDER.indexOf(index)) * 110}ms`,
          }}
        />
      ))}
    </span>
  );
}

export interface ChatLoadingProps {
  loading: boolean;
  startedAt?: number;
  locale: LocaleCode;
  className?: string;
}

/**
 * ZCode-aligned turn-tail working indicator: 3x3 pixel spinner, shimmering label,
 * and self-ticking 100ms elapsed timer (keeps history turn rows from re-rendering).
 */
export function ChatLoading({ loading, startedAt, locale, className = '' }: ChatLoadingProps) {
  const [elapsedMs, setElapsedMs] = useState(() =>
    startedAt ? Math.max(0, Date.now() - startedAt) : 0,
  );

  useEffect(() => {
    if (!loading) {
      setElapsedMs(0);
      return;
    }
    const baseStarted = startedAt || Date.now();
    const update = () => setElapsedMs(Math.max(0, Date.now() - baseStarted));
    update();
    const timer = window.setInterval(update, 100);
    return () => window.clearInterval(timer);
  }, [loading, startedAt]);

  if (!loading) return null;

  return (
    <div
      role="status"
      data-testid="chat-loading"
      aria-label={formatMsg(locale, 'chat.loading.label')}
      className={`zcode-stream-in inline-flex w-fit items-center gap-2.5 ${className}`.trim()}
    >
      <ChatLoadingPixels />
      <span className="zcode-loading-label text-ui-base font-medium">
        {formatMsg(locale, 'chat.loading.working')}
      </span>
      <span
        aria-hidden="true"
        className="font-mono text-ui-xs tabular-nums text-foreground-subtle"
      >
        {formatRunningWorkDuration(elapsedMs, locale)}
      </span>
    </div>
  );
}
