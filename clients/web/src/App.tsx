import { Plus, SquareTerminal, X } from 'lucide-react';
import { type PointerEvent as ReactPointerEvent, useEffect, useState } from 'react';
import { emptyStreamState } from './core/reducer';
import { readPersistedSessionId, useNexStore } from './core/store';
import { AuthGate } from './features/AuthGate';
import { CommandPaletteModal } from './features/CommandPaletteModal';
import { ApprovalModal, UIPromptModal } from './features/ControlModals';
import { ConversationPane } from './features/ConversationPane';
import { DesktopTopOverlay } from './features/DesktopTopOverlay';
import { GhosttyTerminal } from './features/GhosttyTerminal';
import { ImagePreviewModal } from './features/ImagePreviewModal';
import { NotificationToastStack } from './features/NotificationToastStack';
import { ProjectSidebar } from './features/ProjectSidebar';
import { ServerConnectGate } from './features/ServerConnectGate';
import { SettingsPage } from './features/SettingsPage';
import { WorkbenchSidebar } from './features/WorkbenchSidebar';
import { formatMsg } from './i18n/messages';

export function App() {
  const serverConnectRequired = useNexStore((s) => s.serverConnectRequired);
  const authEnabled = useNexStore((s) => s.authEnabled);
  const authenticated = useNexStore((s) => s.authenticated);
  const locale = useNexStore((s) => s.locale);
  const mainView = useNexStore((s) => s.mainView);
  const sidebarVisible = useNexStore((s) => s.sidebarVisible);
  const sidePaneVisible = useNexStore((s) => s.sidePaneVisible);
  const showBottomPanel = useNexStore((s) => s.showBottomPanel);
  const selectedRunnerId = useNexStore((s) => s.selectedRunnerId);
  const bottomTerminals = useNexStore((s) => s.bottomTerminals);
  const activeBottomTermId = useNexStore((s) => s.activeBottomTermId);
  const bootstrap = useNexStore((s) => s.bootstrap);
  const spawnBottomTerminal = useNexStore((s) => s.spawnBottomTerminal);
  const closeBottomTerminal = useNexStore((s) => s.closeBottomTerminal);

  // Subscribe ONLY to blocking modal states so App shell NEVER re-renders on text/tool streaming frames!
  const pendingApproval = useNexStore((s) =>
    s.activeSessionId ? s.streams[s.activeSessionId]?.pendingApproval || null : null,
  );
  const pendingUIPrompt = useNexStore((s) =>
    s.activeSessionId ? s.streams[s.activeSessionId]?.pendingUIPrompt || null : null,
  );

  const [sidebarWidth, setSidebarWidth] = useState(260);
  const [sidePaneWidth, setSidePaneWidth] = useState(440);
  const [terminalHeight, setTerminalHeight] = useState(220);
  const [viewportWidth, setViewportWidth] = useState(() =>
    typeof window !== 'undefined' ? window.innerWidth : 1440,
  );
  const [draggingPanel, setDraggingPanel] = useState<'sidebar' | 'sidepane' | 'terminal' | null>(null);
  const [hasOpenedSidePane, setHasOpenedSidePane] = useState(sidePaneVisible);
  const [hasOpenedTerminal, setHasOpenedTerminal] = useState(showBottomPanel);

  useEffect(() => {
    void bootstrap();
  }, [bootstrap]);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    const onResize = () => setViewportWidth(window.innerWidth);
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.metaKey && !e.ctrlKey && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        const st = useNexStore.getState();
        st.setQuickSearchOpen(!st.quickSearchOpen);
      }
    };
    const onPopState = () => {
      const sid = readPersistedSessionId();
      const st = useNexStore.getState();
      if (sid && sid !== st.activeSessionId) {
        void st.selectSession(sid);
      } else if (!sid && st.activeSessionId) {
        st.startNewSession();
      }
    };
    window.addEventListener('resize', onResize);
    window.addEventListener('keydown', onKeyDown);
    window.addEventListener('popstate', onPopState);
    return () => {
      window.removeEventListener('resize', onResize);
      window.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('popstate', onPopState);
    };
  }, []);

  const effectiveSidePaneWidth = Math.min(
    sidePaneWidth,
    Math.max(280, Math.floor(viewportWidth * 0.36)),
  );

  useEffect(() => {
    if (sidePaneVisible) setHasOpenedSidePane(true);
  }, [sidePaneVisible]);

  useEffect(() => {
    if (showBottomPanel) setHasOpenedTerminal(true);
  }, [showBottomPanel]);

  const startHorizontalDrag = (
    e: ReactPointerEvent<HTMLDivElement>,
    kind: 'sidebar' | 'sidepane',
    initialWidth: number,
    minW: number,
    maxW: number,
    invert: boolean,
    setter: (w: number) => void,
  ) => {
    e.preventDefault();
    setDraggingPanel(kind);
    const startX = e.clientX;
    const onMove = (ev: PointerEvent) => {
      const delta = invert ? startX - ev.clientX : ev.clientX - startX;
      setter(Math.max(minW, Math.min(maxW, Math.round(initialWidth + delta))));
    };
    const onUp = () => {
      setDraggingPanel(null);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  };

  const startVerticalDrag = (e: ReactPointerEvent<HTMLDivElement>, initialH: number) => {
    e.preventDefault();
    setDraggingPanel('terminal');
    const startY = e.clientY;
    const onMove = (ev: PointerEvent) => {
      const delta = startY - ev.clientY;
      setTerminalHeight(Math.max(140, Math.min(520, Math.round(initialH + delta))));
    };
    const onUp = () => {
      setDraggingPanel(null);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  };

  const shouldMountSidePane = sidePaneVisible || hasOpenedSidePane;
  const shouldMountTerminal = showBottomPanel || hasOpenedTerminal;
  const isCompact = viewportWidth < 768;

  const activeBottomTerm = bottomTerminals.find((t) => t.id === activeBottomTermId) || bottomTerminals[0];

  if (serverConnectRequired) {
    return <ServerConnectGate />;
  }

  if (authEnabled && !authenticated) {
    return <AuthGate />;
  }

  if (mainView === 'settings') {
    return (
      <div className="zcode-view-enter nex-safe-top nex-safe-bottom h-dvh w-screen overflow-hidden">
        <SettingsPage />
        <CommandPaletteModal />
        <ImagePreviewModal />
        <NotificationToastStack />
        {pendingApproval && <ApprovalModal approval={pendingApproval} />}
        {pendingUIPrompt && <UIPromptModal prompt={pendingUIPrompt} />}
      </div>
    );
  }

  return (
    <div className="nex-safe-top nex-safe-bottom relative flex h-dvh w-screen overflow-hidden bg-background text-foreground">
      {/* Top-Left Floating Controls when Left Sidebar is Collapsed */}
      <DesktopTopOverlay />

      {isCompact && sidebarVisible && (
        <div
          data-testid="mobile-sidebar-backdrop"
          onClick={() => useNexStore.getState().toggleSidebar()}
          className="fixed inset-0 z-30 bg-black/40 backdrop-blur-[1px]"
        />
      )}

      {/* Smooth ZCode Animated Full-Height Left Sidebar (Drawer on Mobile) */}
      <div
        data-testid="sidebar-panel"
        data-state={sidebarVisible ? 'open' : 'closed'}
        aria-hidden={!sidebarVisible}
        style={isCompact ? undefined : { width: sidebarVisible ? `${sidebarWidth}px` : '0px' }}
        className={
          isCompact
            ? `nex-safe-top nex-safe-bottom fixed inset-y-0 left-0 z-40 w-[85vw] max-w-[320px] overflow-hidden shadow-2xl transition-transform duration-200 ease-out ${
                sidebarVisible ? 'translate-x-0 opacity-100' : 'pointer-events-none -translate-x-full opacity-0'
              }`
            : `relative h-full shrink-0 overflow-hidden ${
                draggingPanel === 'sidebar' ? '' : 'transition-[width,opacity] duration-200 ease-out'
              } ${sidebarVisible ? 'opacity-100' : 'pointer-events-none opacity-0'}`
        }
      >
        <div style={isCompact ? undefined : { width: `${sidebarWidth}px` }} className="h-full w-full">
          <ProjectSidebar />
        </div>
        {!isCompact && sidebarVisible && (
          <div
            role="separator"
            aria-label="Resize sidebar"
            onPointerDown={(e) => startHorizontalDrag(e, 'sidebar', sidebarWidth, 220, 420, false, setSidebarWidth)}
            className="absolute top-0 right-0 z-20 h-full w-1.5 cursor-col-resize transition-colors hover:bg-resize-handle/40"
          />
        )}
      </div>

      {/* Seamless Full-Bleed Center Column: Conversation + Smooth Animated Bottom Terminal */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-background">
        <ConversationPane />

        {shouldMountTerminal && (
          <section
            data-testid="bottom-terminal-panel"
            data-state={showBottomPanel ? 'open' : 'closed'}
            aria-hidden={!showBottomPanel}
            style={{ height: showBottomPanel ? `${terminalHeight}px` : '0px' }}
            className={`relative flex shrink-0 flex-col overflow-hidden bg-panel ${
              draggingPanel === 'terminal' ? '' : 'transition-[height,opacity] duration-200 ease-out'
            } ${showBottomPanel ? 'border-t border-border opacity-100' : 'pointer-events-none border-t-0 opacity-0'}`}
          >
            {showBottomPanel && (
              <div
                role="separator"
                onPointerDown={(e) => startVerticalDrag(e, terminalHeight)}
                className="absolute top-0 left-0 z-20 h-1.5 w-full cursor-row-resize transition-colors hover:bg-resize-handle/40"
              />
            )}
            <div className="flex h-9 shrink-0 items-center justify-between border-b border-border bg-background px-3">
              <div className="flex items-center gap-1 overflow-x-auto">
                <span className="mr-1.5 flex items-center gap-1.5 text-ui-sm font-medium text-foreground-subtle">
                  <SquareTerminal className="size-3.5" />
                  <span>{formatMsg(locale, 'terminal.title')}</span>
                </span>
                {bottomTerminals.map((bt) => (
                  <div
                    key={bt.id}
                    onClick={() => useNexStore.setState({ activeBottomTermId: bt.id })}
                    className={`flex h-6 cursor-pointer items-center gap-1.5 rounded-md px-2 text-ui-xs transition-colors ${
                      bt.id === activeBottomTerm?.id
                        ? 'bg-selected font-medium text-foreground'
                        : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
                    }`}
                  >
                    <span>{bt.title}</span>
                    <button
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        closeBottomTerminal(bt.id);
                      }}
                      className="text-foreground-subtlest hover:text-foreground"
                    >
                      <X className="size-3" />
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  onClick={() => void spawnBottomTerminal()}
                  title="New remote libghostty terminal"
                  className="flex size-6 items-center justify-center rounded-md text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
                >
                  <Plus className="size-3.5" />
                </button>
              </div>

              <button
                type="button"
                onClick={() => useNexStore.setState({ showBottomPanel: false })}
                className="flex size-6 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground"
              >
                <X className="size-3.5" />
              </button>
            </div>

            <div className="min-h-0 flex-1 overflow-hidden">
              <GhosttyTerminal ptyId={activeBottomTerm?.ptyId || ''} runnerId={selectedRunnerId} />
            </div>
          </section>
        )}
      </div>

      {/* Smooth ZCode Animated Right Side Pane (Full-screen overlay sheet on Mobile) */}
      {shouldMountSidePane && (
        <div
          data-testid="side-pane-container"
          data-state={sidePaneVisible ? 'open' : 'closed'}
          aria-hidden={!sidePaneVisible}
          style={isCompact ? undefined : { width: sidePaneVisible ? `${effectiveSidePaneWidth}px` : '0px' }}
          className={
            isCompact
              ? `nex-safe-top nex-safe-bottom fixed inset-0 z-40 bg-background transition-opacity duration-200 ease-out ${
                  sidePaneVisible ? 'opacity-100' : 'pointer-events-none opacity-0'
                }`
              : `relative h-full shrink-0 overflow-hidden ${
                  draggingPanel === 'sidepane' ? '' : 'transition-[width,opacity] duration-200 ease-out'
                } ${sidePaneVisible ? 'opacity-100' : 'pointer-events-none opacity-0'}`
          }
        >
          {!isCompact && sidePaneVisible && (
            <div
              role="separator"
              onPointerDown={(e) =>
                startHorizontalDrag(e, 'sidepane', effectiveSidePaneWidth, 280, 760, true, setSidePaneWidth)
              }
              className="absolute top-0 left-0 z-20 h-full w-1.5 cursor-col-resize transition-colors hover:bg-resize-handle/40"
            />
          )}
          <div style={isCompact ? undefined : { width: `${effectiveSidePaneWidth}px` }} className="h-full w-full">
            <WorkbenchSidebar />
          </div>
        </div>
      )}

      {/* Command Palette, Image Preview & Blocking Interaction Dialogs */}
      <CommandPaletteModal />
      <ImagePreviewModal />
      <NotificationToastStack />
      {pendingApproval && <ApprovalModal approval={pendingApproval} />}
    </div>
  );
}
