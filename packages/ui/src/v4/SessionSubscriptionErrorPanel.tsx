import { NEX_GITHUB_ISSUES_NEW_URL } from "@nex/shared";
import { usePlatform } from "@/hooks/usePlatform.js";
import { useCallback } from "react";
import { TID_V4_RETRY_SUBSCRIBE } from "@nex/shared/test-ids";
import { Button } from "@/components/ui/button.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";

export interface SessionSubscriptionErrorPanelProps {
  error: string;
  onReconnect: () => void;
  reconnectLabel?: string;
}

export function SessionSubscriptionErrorPanel({
  error,
  onReconnect,
  reconnectLabel,
}: SessionSubscriptionErrorPanelProps) {
  const { intl } = useNexIntl();
  const platform = usePlatform();
  const handleOpenFeedback = useCallback(() => {
    // 产品决定：不再内置工单系统，反馈入口打开 GitHub Issues 新建页。
    platform.openExternal(NEX_GITHUB_ISSUES_NEW_URL);
  }, [platform]);

  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-4 text-ui-base">
      <p className="max-w-full break-words text-center font-mono text-destructive">{error}</p>
      <div className="flex flex-wrap items-center justify-center gap-2">
        <Button type="button" variant="outline" onClick={handleOpenFeedback}>
          {intl.formatMessage({ id: "chat.error.feedback" })}
        </Button>
        <Button type="button" data-testid={TID_V4_RETRY_SUBSCRIBE} onClick={onReconnect}>
          {reconnectLabel ?? intl.formatMessage({ id: "workspaceSidebar.reconnect" })}
        </Button>
      </div>
    </div>
  );
}
