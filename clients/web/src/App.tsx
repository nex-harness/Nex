import { Plus, SquareTerminal, X } from 'lucide-react';
import { type PointerEvent as ReactPointerEvent, useEffect, useState } from 'react';
import { emptyStreamState } from './core/reducer';
import { useNexStore } from './core/store';
import { ApprovalModal, UIPromptModal } from './features/ControlModals';
import { ConversationPane } from './features/ConversationPane';
import { DesktopTopOverlay } from './features/DesktopTopOverlay';
import { GhosttyTerminal } from './features/GhosttyTerminal';
import { ProjectSidebar } from './features/ProjectSidebar';
import { SettingsPage } from './features/SettingsPage';
import { WorkbenchSidebar } from './features/WorkbenchSidebar';
import { formatMsg } from './i18n/messages';

export function App() {
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
  const [sidePaneWidth, setSidePaneWidth] = useState(460);
  const [terminalHeight, setTerminalHeight] = useState(220);

  useEffect(() => {
    void bootstrap();
  }, [bootstrap]);

  const startHorizontalDrag = (
    e: ReactPointerEvent<HTMLDivElement>,
    initialWidth: number,
    minW: number,
    maxW: number,
    invert: boolean,
    setter: (w: number) => void,
  ) => {
    e.preventDefault();
    const startX = e.clientX;
    const onMove = (ev: PointerEvent) => {
      const delta = invert ? startX - ev.clientX : ev.clientX - startX;
      setter(Math.max(minW, Math.min(maxW, Math.round(initialWidth + delta))));
    };
    const onUp = () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  };

  const startVerticalDrag = (e: ReactPointerEvent<HTMLDivElement>, initialH: number) => {
    e.preventDefault();
    const startY = e.clientY;
    const onMove = (ev: PointerEvent) => {
      const delta = startY - ev.clientY;
      setTerminalHeight(Math.max(140, Math.min(520, Math.round(initialH + delta))));
    };
    const onUp = () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  };

  const activeBottomTerm = bottomTerminals.find((t) => t.id === activeBottomTermId) || bottomTerminals[0];

  if (mainView === 'settings') {
    return (
      <>
        <SettingsPage />
        {pendingApproval && <ApprovalModal approval={pendingApproval} />}
        {pendingUIPrompt && <UIPromptModal prompt={pendingUIPrompt} />}
      </>
    );
  }

  return (
    <div className="relative flex h-dvh w-screen overflow-hidden bg-background text-foreground">
      {/* Top-Left Floating Controls when Left Sidebar is Collapsed */}
      <DesktopTopOverlay />

      {/* Seamless Full-Height Left Sidebar */}
      {sidebarVisible && (
        <div style={{ width: `${sidebarWidth}px` }} className="relative h-full shrink-0">
          <ProjectSidebar />
          <div
            role="separator"
            aria-label="Resize sidebar"
            onPointerDown={(e) => startHorizontalDrag(e, sidebarWidth, 220, 420, false, setSidebarWidth)}
            className="absolute top-0 -right-1 z-20 h-full w-2 cursor-col-resize hover:bg-resize-handle/40 transition-colors"
          />
        </div>
      )}

      {/* Seamless Full-Bleed Center Column: Conversation + Optional Bottom Terminal */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-background">
        <ConversationPane />

        {showBottomPanel && (
          <section
            style={{ height: `${terminalHeight}px` }}
            className="relative flex shrink-0 flex-col border-t border-border bg-panel"
          >
            <div
              role="separator"
              onPointerDown={(e) => startVerticalDrag(e, terminalHeight)}
              className="absolute -top-1 left-0 z-20 h-2 w-full cursor-row-resize hover:bg-resize-handle/40 transition-colors"
            />
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
                  className="flex size-6 items-center justify-center rounded-md text-foreground-subtle hover:bg-hover hover:text-foreground"
                >
                  <Plus className="size-3.5" />
                </button>
              </div>

              <button
                type="button"
                onClick={() => useNexStore.setState({ showBottomPanel: false })}
                className="flex size-6 items-center justify-center rounded-md text-foreground-subtlest hover:bg-hover hover:text-foreground"
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

      {/* Non-Persistent Right Side Pane (Closed by default; opened on demand) */}
      {sidePaneVisible && (
        <div style={{ width: `${sidePaneWidth}px` }} className="relative h-full shrink-0">
          <div
            role="separator"
            onPointerDown={(e) => startHorizontalDrag(e, sidePaneWidth, 320, 760, true, setSidePaneWidth)}
            className="absolute top-0 -left-1 z-20 h-full w-2 cursor-col-resize hover:bg-resize-handle/40 transition-colors"
          />
          <WorkbenchSidebar />
        </div>
      )}

      {/* Blocking Interaction Dialogs */}
      {pendingApproval && <ApprovalModal approval={pendingApproval} />}
      {pendingUIPrompt && <UIPromptModal prompt={pendingUIPrompt} />}
    </div>
  );
}
