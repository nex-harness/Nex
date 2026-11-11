import { useEffect, useRef, useState } from "react";
import type { IMemoryService } from "@nex/services";
import {
  TID_SETTINGS_MEMORY_FILE_PREVIEW,
  TID_SETTINGS_MEMORY_FILE_PREVIEW_RETRY,
  testId,
} from "@nex/shared/test-ids";
import { Button } from "@/components/ui/button.js";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog.js";
import { MessageResponse } from "@/components/ai-elements/message.js";
import { DEFAULT_CODE_PREVIEW_SETTINGS } from "@/lib/codePreviewSettings.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";
import { useNexStoreWithDefault } from "@/store/StoreProvider.js";
import {
  resolveMemoryPreviewErrorPresentation,
  type MemoryPreviewErrorPresentation,
} from "@/settings/memoryPreviewErrorPresentation.js";
import { formatMemoryUpdatedAt } from "@/settings/memoryUpdatedAt.js";
import type { ProjectMemoryFileSummary } from "@nex/services";
import type { Theme } from "@/useTheme.js";

type MemoryPreviewLoadState =
  | { phase: "loading" }
  | { phase: "ready"; content: string; updatedAt: number }
  | { phase: "error"; presentation: MemoryPreviewErrorPresentation };

/**
 * 设置页 Memory 文件预览弹窗（右侧滑出面板形态）。
 *
 * 触发入口与展示层都在设置页内，不跨层——预览不进 workspace side pane，
 * 因为设置页是全屏覆盖层，底层的 side pane 会被完全遮住。
 * 正文权威在 IMemoryService（本地 Host RPC）；每次打开按文件重读，请求按
 * 序号判旧：快速切换文件时晚到的旧响应直接丢弃，不能覆盖当前文件正文。
 */
export function MemoryPreviewDialog({
  memoryService,
  target,
  onOpenChange,
}: {
  memoryService: Pick<IMemoryService, "readProjectMemoryFile">;
  /** 当前预览目标；非空即打开。 */
  target: { workspaceId: string; workspaceLabel: string; file: ProjectMemoryFileSummary } | null;
  onOpenChange: (open: boolean) => void;
}) {
  const { intl, locale } = useNexIntl();
  const theme = useNexStoreWithDefault((state) => state.theme, "system" as Theme);
  const codePreviewSettings = useNexStoreWithDefault(
    (state) => state.codePreviewSettings,
    DEFAULT_CODE_PREVIEW_SETTINGS,
  );
  const [state, setState] = useState<MemoryPreviewLoadState>({ phase: "loading" });
  const [reloadSeq, setReloadSeq] = useState(0);
  const requestIdRef = useRef(0);

  useEffect(() => {
    if (!target) {
      setState({ phase: "loading" });
      return;
    }
    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;
    setState({ phase: "loading" });
    memoryService
      .readProjectMemoryFile({
        workspaceId: target.workspaceId,
        fileName: target.file.name,
      })
      .then((result) => {
        if (requestIdRef.current !== requestId) return;
        setState({ phase: "ready", content: result.content, updatedAt: result.updatedAt });
      })
      .catch((error: unknown) => {
        if (requestIdRef.current !== requestId) return;
        setState({ phase: "error", presentation: resolveMemoryPreviewErrorPresentation(error) });
      });
  }, [intl, memoryService, reloadSeq, target]);

  return (
    <Dialog open={target !== null} onOpenChange={onOpenChange}>
      <DialogContent
        showOverlay={false}
        className="inset-y-2 left-auto right-2 top-2 bottom-2 z-40 flex h-auto w-[min(42rem,calc(100%-1rem))] translate-x-0 translate-y-0 flex-col gap-0 overflow-hidden p-0 data-open:slide-in-from-right-4 data-closed:slide-out-to-right-4"
        aria-describedby={undefined}
      >
        {target ? (
          <>
            <DialogHeader className="shrink-0 border-b border-border px-4 py-3">
              <DialogTitle data-testid={testId(TID_SETTINGS_MEMORY_FILE_PREVIEW, target.file.name)}>
                {target.file.name}
              </DialogTitle>
              <DialogDescription className="mt-0.5">
                {target.workspaceLabel} ·{" "}
                {formatMemoryUpdatedAt({
                  formatMessage: intl.formatMessage,
                  locale,
                  now: Date.now(),
                  updatedAt: target.file.updatedAt,
                })}
              </DialogDescription>
            </DialogHeader>
            <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4">
              {state.phase === "loading" ? (
                <div className="flex h-full min-h-32 items-center justify-center text-ui-base text-foreground-subtle">
                  {intl.formatMessage({ id: "settings.memory.preview.loading" })}
                </div>
              ) : state.phase === "error" ? (
                <div className="flex h-full min-h-32 flex-col items-center justify-center gap-3 px-4 text-center">
                  <p className="text-ui-base text-foreground-subtle">
                    {state.presentation.messageKey
                      ? intl.formatMessage({ id: state.presentation.messageKey })
                      : state.presentation.rawMessage}
                  </p>
                  {state.presentation.oversized ? null : (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      data-testid={TID_SETTINGS_MEMORY_FILE_PREVIEW_RETRY}
                      onClick={() => setReloadSeq((seq) => seq + 1)}
                    >
                      {intl.formatMessage({ id: "settings.memory.viewer.refresh" })}
                    </Button>
                  )}
                </div>
              ) : (
                <MessageResponse
                  className="mx-auto w-full max-w-3xl min-w-0 break-words text-foreground"
                  theme={theme}
                  codePreviewSettings={codePreviewSettings}
                >
                  {state.content}
                </MessageResponse>
              )}
            </div>
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
