import { ArrowLeft, ArrowRight, CirclePlus, PanelLeft } from 'lucide-react';
import { useNexStore } from '../core/store';
import { formatMsg } from '../i18n/messages';

/**
 * ZCode DesktopTopOverlay: floating controls shown at the top-left of the window
 * whenever the left project sidebar is collapsed. The outer layer is
 * pointer-events-none so the conversation canvas stays draggable/clickable,
 * while the compact control row re-enables pointer events.
 */
export function DesktopTopOverlay() {
  const sidebarVisible = useNexStore((s) => s.sidebarVisible);
  const toggleSidebar = useNexStore((s) => s.toggleSidebar);
  const startNewSession = useNexStore((s) => s.startNewSession);
  const locale = useNexStore((s) => s.locale);

  if (sidebarVisible) return null;

  return (
    <div className="zcode-view-enter pointer-events-none absolute left-3 top-2.5 z-30 flex items-center">
      <div className="pointer-events-auto flex items-center gap-0.5">
        <button
          type="button"
          data-testid="top-overlay-sidebar-open"
          onClick={toggleSidebar}
          title={formatMsg(locale, 'sidePane.expand')}
          className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
        >
          <PanelLeft className="size-4" />
        </button>
        <button
          type="button"
          onClick={() => window.history.back()}
          className="flex size-6 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground-subtle"
        >
          <ArrowLeft className="size-3.5" />
        </button>
        <button
          type="button"
          onClick={() => window.history.forward()}
          className="flex size-6 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-hover hover:text-foreground-subtle"
        >
          <ArrowRight className="size-3.5" />
        </button>
        <button
          type="button"
          data-testid="top-overlay-new-task"
          onClick={() => startNewSession()}
          title={formatMsg(locale, 'sidebar.newTask')}
          className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
        >
          <CirclePlus className="size-4" />
        </button>
      </div>
    </div>
  );
}
