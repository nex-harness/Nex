import { Bell, X } from 'lucide-react';
import { useNexStore } from '../core/store';
import { formatMsg } from '../i18n/messages';

export function NotificationToastStack() {
  const toasts = useNexStore((s) => s.notificationToasts);
  const locale = useNexStore((s) => s.locale);
  const selectSession = useNexStore((s) => s.selectSession);
  const dismissNotificationToast = useNexStore((s) => s.dismissNotificationToast);

  if (toasts.length === 0) return null;

  return (
    <div
      data-testid="notification-toast-stack"
      className="pointer-events-none fixed right-4 bottom-4 z-50 flex w-[min(22rem,calc(100vw-2rem))] flex-col gap-2"
    >
      {toasts.map((toast) => (
        <div
          key={toast.id}
          data-testid="in-app-notification-toast"
          role="status"
          onClick={() => {
            dismissNotificationToast(toast.id);
            if (toast.sessionId) {
              useNexStore.setState({ mainView: 'workspace', serverConnectRequired: false });
              void selectSession(toast.sessionId);
            }
          }}
          className="pointer-events-auto flex cursor-pointer items-start gap-2.5 rounded-xl border border-border bg-card/95 p-3 shadow-lg backdrop-blur-md transition-colors hover:bg-hover"
        >
          <span className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-lg bg-surface text-foreground">
            <Bell className="size-3.5" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="truncate text-ui-sm font-semibold text-foreground">{toast.title}</div>
            {toast.body ? (
              <div className="mt-0.5 line-clamp-2 text-ui-xs text-foreground-subtle">{toast.body}</div>
            ) : null}
            {toast.sessionId ? (
              <div className="mt-1 text-ui-xs font-medium text-foreground">{formatMsg(locale, 'notify.openSession')}</div>
            ) : null}
          </div>
          <button
            type="button"
            aria-label={formatMsg(locale, 'notify.dismiss')}
            onClick={(e) => {
              e.stopPropagation();
              dismissNotificationToast(toast.id);
            }}
            className="flex size-6 shrink-0 items-center justify-center rounded-md text-foreground-subtlest transition-colors hover:bg-surface hover:text-foreground"
          >
            <X className="size-3.5" />
          </button>
        </div>
      ))}
    </div>
  );
}
