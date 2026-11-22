import {
  ArrowLeft,
  ArrowRight,
  Globe,
  Hand,
  Monitor,
  Plus,
  RotateCw,
  Smartphone,
} from 'lucide-react';
import { type KeyboardEvent, type MouseEvent, type WheelEvent, useEffect, useMemo, useRef, useState } from 'react';
import { useNexStore } from '../core/store';
import { wsClient } from '../core/wsClient';
import { formatMsg } from '../i18n/messages';

export function BrowserTabPanel() {
  const locale = useNexStore((s) => s.locale);
  const activeSessionId = useNexStore((s) => s.activeSessionId);
  const selectedRunnerId = useNexStore((s) => s.selectedRunnerId);
  const wsConnected = useNexStore((s) => s.wsConnected);
  const browserStates = useNexStore((s) => s.browserStates);
  const sendBrowserControl = useNexStore((s) => s.sendBrowserControl);
  const sendBrowserInput = useNexStore((s) => s.sendBrowserInput);
  const resolveBrowserHandoff = useNexStore((s) => s.resolveBrowserHandoff);

  const sessionKey = activeSessionId || 'default';
  const sessionState = browserStates[sessionKey];
  const tabs = sessionState?.tabs || [];
  const activeTab = useMemo(
    () => tabs.find((t) => t.tab_id === sessionState?.active_tab_id) || tabs[0] || null,
    [tabs, sessionState?.active_tab_id],
  );
  const activeTabId = activeTab?.tab_id || '';
  const handoff = sessionState?.handoff;
  const humanControl = Boolean(activeTab?.human_control || sessionState?.human_control || handoff?.state === 'awaiting_human');

  const [urlDraft, setUrlDraft] = useState(activeTab?.url || '');
  const [hasFrame, setHasFrame] = useState(false);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const lastMoveSentRef = useRef(0);

  useEffect(() => {
    setUrlDraft(activeTab?.url || '');
  }, [activeTab?.tab_id, activeTab?.url]);

  useEffect(() => {
    setHasFrame(false);
    if (!activeTabId || !selectedRunnerId || !wsConnected) return;

    const vw = activeTab?.viewport?.width || 1280;
    const vh = activeTab?.viewport?.height || 720;
    void sendBrowserControl({
      session_id: sessionKey,
      tab_id: activeTabId,
      action: 'stream_start',
      max_width: vw,
      max_height: vh,
      quality: 72,
    });

    const off = wsClient.onBrowserFrame(activeTabId, (jpegBytes) => {
      setHasFrame(true);
      const canvas = canvasRef.current;
      if (!canvas) return;
      const copy = new Uint8Array(jpegBytes.length);
      copy.set(jpegBytes);
      const blob = new Blob([copy], { type: 'image/jpeg' });

      if (typeof createImageBitmap === 'function') {
        void createImageBitmap(blob)
          .then((bmp) => {
            const c = canvasRef.current;
            if (!c) {
              bmp.close();
              return;
            }
            if (c.width !== bmp.width || c.height !== bmp.height) {
              c.width = bmp.width;
              c.height = bmp.height;
            }
            const ctx = c.getContext('2d');
            if (ctx) ctx.drawImage(bmp, 0, 0);
            bmp.close();
          })
          .catch(() => {
            // ignore corrupted/stale frame
          });
      } else {
        const imgUrl = URL.createObjectURL(blob);
        const img = new Image();
        img.onload = () => {
          const c = canvasRef.current;
          if (c) {
            if (c.width !== img.naturalWidth || c.height !== img.naturalHeight) {
              c.width = img.naturalWidth;
              c.height = img.naturalHeight;
            }
            const ctx = c.getContext('2d');
            ctx?.drawImage(img, 0, 0);
          }
          URL.revokeObjectURL(imgUrl);
        };
        img.src = imgUrl;
      }
    });

    return () => {
      off();
      void sendBrowserControl({
        session_id: sessionKey,
        tab_id: activeTabId,
        action: 'stream_stop',
      });
    };
  }, [activeTabId, selectedRunnerId, wsConnected, sessionKey, activeTab?.viewport?.width, activeTab?.viewport?.height, sendBrowserControl]);

  const toViewportCoords = (clientX: number, clientY: number) => {
    const canvas = canvasRef.current;
    if (!canvas) return { x: 0, y: 0 };
    const rect = canvas.getBoundingClientRect();
    const vw = activeTab?.viewport?.width || 1280;
    const vh = activeTab?.viewport?.height || 720;
    const scale = Math.min(rect.width / vw, rect.height / vh);
    const drawW = vw * scale;
    const drawH = vh * scale;
    const offX = rect.left + (rect.width - drawW) / 2;
    const offY = rect.top + (rect.height - drawH) / 2;
    const x = Math.max(0, Math.min(vw, Math.round((clientX - offX) / (scale || 1))));
    const y = Math.max(0, Math.min(vh, Math.round((clientY - offY) / (scale || 1))));
    return { x, y };
  };

  const mapMouseBtn = (btn: number) => (btn === 2 ? 'right' : btn === 1 ? 'middle' : 'left');

  const handleMouseMove = (e: MouseEvent<HTMLCanvasElement>) => {
    if (!activeTabId) return;
    const now = performance.now();
    if (now - lastMoveSentRef.current < 33) return;
    lastMoveSentRef.current = now;
    const pt = toViewportCoords(e.clientX, e.clientY);
    sendBrowserInput({
      session_id: sessionKey,
      tab_id: activeTabId,
      kind: 'mouse',
      type: 'mouseMoved',
      x: pt.x,
      y: pt.y,
      button: 'none',
    });
  };

  const handleMouseDown = (e: MouseEvent<HTMLCanvasElement>) => {
    if (!activeTabId) return;
    e.currentTarget.focus();
    const pt = toViewportCoords(e.clientX, e.clientY);
    sendBrowserInput({
      session_id: sessionKey,
      tab_id: activeTabId,
      kind: 'mouse',
      type: 'mousePressed',
      x: pt.x,
      y: pt.y,
      button: mapMouseBtn(e.button),
      click_count: 1,
    });
  };

  const handleMouseUp = (e: MouseEvent<HTMLCanvasElement>) => {
    if (!activeTabId) return;
    const pt = toViewportCoords(e.clientX, e.clientY);
    sendBrowserInput({
      session_id: sessionKey,
      tab_id: activeTabId,
      kind: 'mouse',
      type: 'mouseReleased',
      x: pt.x,
      y: pt.y,
      button: mapMouseBtn(e.button),
      click_count: 1,
    });
  };

  const handleWheel = (e: WheelEvent<HTMLCanvasElement>) => {
    if (!activeTabId) return;
    const pt = toViewportCoords(e.clientX, e.clientY);
    sendBrowserInput({
      session_id: sessionKey,
      tab_id: activeTabId,
      kind: 'wheel',
      x: pt.x,
      y: pt.y,
      delta_x: e.deltaX,
      delta_y: e.deltaY,
    });
  };

  const handleKey = (kind: 'keyDown' | 'keyUp') => (e: KeyboardEvent<HTMLCanvasElement>) => {
    if (!activeTabId) return;
    e.preventDefault();
    const modifiers =
      (e.altKey ? 1 : 0) |
      (e.ctrlKey ? 2 : 0) |
      (e.metaKey ? 4 : 0) |
      (e.shiftKey ? 8 : 0);
    const printable = kind === 'keyDown' && e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey ? e.key : undefined;
    sendBrowserInput({
      session_id: sessionKey,
      tab_id: activeTabId,
      kind: 'key',
      type: kind,
      key: e.key,
      code: e.code,
      text: printable,
      modifiers,
    });
  };

  const submitURL = (e: React.FormEvent) => {
    e.preventDefault();
    const raw = urlDraft.trim();
    if (!raw) return;
    const normalized = /^[a-zA-Z]+:/.test(raw) ? raw : `https://${raw}`;
    void sendBrowserControl({
      session_id: sessionKey,
      tab_id: activeTabId || undefined,
      action: 'navigate',
      url: normalized,
    });
  };

  const isMobileViewport = Boolean(activeTab && activeTab.viewport.width <= 500);

  return (
    <div data-testid="browser-tab-panel" className="flex h-full min-h-0 flex-col bg-background">
      {/* Top Browser Toolbar */}
      <div className="flex flex-wrap items-center gap-1.5 border-b border-border bg-surface px-2.5 py-2">
        <div className="flex items-center gap-0.5">
          <button
            type="button"
            data-testid="browser-back-btn"
            disabled={!activeTabId}
            onClick={() => void sendBrowserControl({ session_id: sessionKey, tab_id: activeTabId, action: 'back' })}
            title={formatMsg(locale, 'browser.back')}
            className="flex size-7 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
          >
            <ArrowLeft className="size-3.5" />
          </button>
          <button
            type="button"
            data-testid="browser-forward-btn"
            disabled={!activeTabId}
            onClick={() => void sendBrowserControl({ session_id: sessionKey, tab_id: activeTabId, action: 'forward' })}
            title={formatMsg(locale, 'browser.forward')}
            className="flex size-7 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
          >
            <ArrowRight className="size-3.5" />
          </button>
          <button
            type="button"
            data-testid="browser-reload-btn"
            disabled={!activeTabId}
            onClick={() => void sendBrowserControl({ session_id: sessionKey, tab_id: activeTabId, action: 'reload' })}
            title={formatMsg(locale, 'browser.reload')}
            className="flex size-7 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
          >
            <RotateCw className="size-3.5" />
          </button>
        </div>

        <form onSubmit={submitURL} className="flex min-w-0 flex-1 items-center">
          <div className="flex h-7 w-full items-center gap-1.5 rounded-lg border border-border bg-input px-2">
            <Globe className="size-3 shrink-0 text-foreground-subtlest" />
            <input
              data-testid="browser-url-input"
              value={urlDraft}
              onChange={(e) => setUrlDraft(e.target.value)}
              placeholder={formatMsg(locale, 'browser.urlPlaceholder')}
              className="min-w-0 flex-1 bg-transparent font-mono text-ui-xs text-foreground placeholder-foreground-subtlest focus:outline-none"
            />
          </div>
        </form>

        {tabs.length > 1 && (
          <select
            data-testid="browser-tab-select"
            value={activeTabId}
            onChange={(e) =>
              void sendBrowserControl({ session_id: sessionKey, tab_id: e.target.value, action: 'switch_tab' })
            }
            className="h-7 max-w-[140px] rounded-md border border-border bg-input px-1.5 text-ui-xs text-foreground"
          >
            {tabs.map((t, idx) => (
              <option key={t.tab_id} value={t.tab_id}>
                {t.title || t.url || `Tab ${idx + 1}`}
              </option>
            ))}
          </select>
        )}

        <button
          type="button"
          data-testid="browser-newtab-btn"
          onClick={() => void sendBrowserControl({ session_id: sessionKey, action: 'new_tab', url: 'about:blank' })}
          title={formatMsg(locale, 'sidePane.newTab')}
          className="flex size-7 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
        >
          <Plus className="size-3.5" />
        </button>

        <button
          type="button"
          data-testid="browser-viewport-toggle"
          disabled={!activeTabId}
          onClick={() =>
            void sendBrowserControl({
              session_id: sessionKey,
              tab_id: activeTabId,
              action: 'set_viewport',
              width: isMobileViewport ? 1280 : 393,
              height: isMobileViewport ? 720 : 852,
            })
          }
          title={formatMsg(locale, isMobileViewport ? 'browser.desktopViewport' : 'browser.mobileViewport')}
          className="flex size-7 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
        >
          {isMobileViewport ? <Smartphone className="size-3.5" /> : <Monitor className="size-3.5" />}
        </button>

        {activeTabId && (
          <button
            type="button"
            data-testid="browser-takeover-btn"
            onClick={() =>
              void sendBrowserControl({
                session_id: sessionKey,
                tab_id: activeTabId,
                action: humanControl ? 'handback' : 'takeover',
              })
            }
            className={`inline-flex h-7 items-center gap-1 rounded-md border px-2 text-ui-xs font-medium transition-colors ${
              humanControl
                ? 'border-amber-500/40 bg-amber-500/12 text-amber-700 dark:text-amber-300'
                : 'border-border bg-card text-foreground-subtle hover:bg-hover hover:text-foreground'
            }`}
          >
            <Hand className="size-3" />
            <span>{formatMsg(locale, humanControl ? 'browser.handback' : 'browser.takeover')}</span>
          </button>
        )}
      </div>

      {/* Human Handoff Banner */}
      {handoff?.state === 'awaiting_human' && (
        <div
          data-testid="browser-handoff-banner"
          className="flex flex-wrap items-center justify-between gap-2 border-b border-amber-500/30 bg-amber-500/10 px-3 py-2 text-ui-xs"
        >
          <div className="min-w-0 flex-1">
            <div className="font-semibold text-amber-800 dark:text-amber-300">
              {formatMsg(locale, 'browser.handoffTitle')}
            </div>
            <div className="truncate text-foreground-subtle">
              {handoff.reason}
              {handoff.message ? ` — ${handoff.message}` : ''}
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            <button
              type="button"
              data-testid="browser-handoff-resume"
              onClick={() => resolveBrowserHandoff(sessionKey, handoff.tab_id || activeTabId, true)}
              className="h-7 rounded-md bg-primary px-2.5 text-ui-xs font-medium text-primary-foreground transition-opacity hover:opacity-90"
            >
              {formatMsg(locale, 'browser.handoffResume')}
            </button>
            <button
              type="button"
              data-testid="browser-handoff-cancel"
              onClick={() => resolveBrowserHandoff(sessionKey, handoff.tab_id || activeTabId, false)}
              className="h-7 rounded-md border border-border bg-card px-2.5 text-ui-xs text-foreground-subtle hover:bg-hover hover:text-foreground"
            >
              {formatMsg(locale, 'browser.handoffCancel')}
            </button>
          </div>
        </div>
      )}

      {/* Screencast Canvas Viewport */}
      <div className="relative flex min-h-0 flex-1 items-center justify-center overflow-hidden bg-black/5 p-2 dark:bg-black/30">
        {!activeTabId ? (
          <div className="max-w-xs text-center">
            <Globe className="mx-auto mb-2 size-8 text-foreground-subtlest" />
            <div className="text-ui-base font-medium text-foreground">{formatMsg(locale, 'browser.emptyTitle')}</div>
            <p className="mt-1 text-ui-xs leading-relaxed text-foreground-subtle">
              {formatMsg(locale, 'browser.emptyHint')}
            </p>
          </div>
        ) : (
          <>
            <canvas
              ref={canvasRef}
              data-testid="browser-canvas"
              tabIndex={0}
              onMouseMove={handleMouseMove}
              onMouseDown={handleMouseDown}
              onMouseUp={handleMouseUp}
              onWheel={handleWheel}
              onKeyDown={handleKey('keyDown')}
              onKeyUp={handleKey('keyUp')}
              onContextMenu={(e) => e.preventDefault()}
              className="max-h-full max-w-full cursor-pointer rounded-lg border border-border bg-white shadow-sm outline-none"
              style={{
                aspectRatio: `${activeTab?.viewport?.width || 1280} / ${activeTab?.viewport?.height || 720}`,
              }}
            />
            {!hasFrame && (
              <div className="pointer-events-none absolute inset-0 flex items-center justify-center text-ui-xs text-foreground-subtlest">
                {formatMsg(locale, 'browser.connectingStream')}
              </div>
            )}
          </>
        )}
      </div>

      {/* Bottom Status Strip */}
      {activeTab && (
        <div className="flex items-center justify-between border-t border-border bg-surface px-3 py-1 text-ui-xs text-foreground-subtlest">
          <span className="truncate font-mono">
            {activeTab.viewport.width}x{activeTab.viewport.height} ·{' '}
            {formatMsg(locale, humanControl ? 'browser.humanActive' : 'browser.agentActive')}
          </span>
          <span className="shrink-0">{formatMsg(locale, 'browser.tabsCount', { count: tabs.length })}</span>
        </div>
      )}
    </div>
  );
}
