import { useEffect, useMemo, useState } from "react";
import { Loader2Icon, SearchIcon } from "lucide-react";
import { Button } from "@/components/ui/button.js";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog.js";
import { Checkbox } from "@/components/ui/checkbox.js";
import { Input } from "@/components/ui/input.js";
import { useNexIntl } from "@/i18n/IntlProvider.js";

/**
 * "从 /v1/models 获取"弹窗：用供应商地址和密钥拉取远端模型列表，
 * 搜索、多选后批量交给父级添加（模型信息由父级从 models.dev 填充）。
 */
export function ProviderRemoteModelPickerDialog({
  open,
  onOpenChange,
  onFetch,
  onConfirm,
  existingModelIds = [],
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 打开时拉取远端模型 ID 列表；抛错视为加载失败。 */
  onFetch: () => Promise<{ readonly ids: readonly string[] }>;
  /** 批量添加所选模型；抛错视为添加失败，弹窗保持打开。 */
  onConfirm: (modelIds: readonly string[]) => Promise<void>;
  /** 供应商已配置的模型，列表中禁用并标记已添加。 */
  existingModelIds?: readonly string[];
}) {
  const { intl } = useNexIntl();
  const [ids, setIds] = useState<readonly string[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [query, setQuery] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [confirmError, setConfirmError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setSelected(new Set());
    setQuery("");
    setConfirmError(null);
    setLoadError(null);
    setIds([]);
    let cancelled = false;
    setLoading(true);
    onFetch()
      .then((result) => {
        if (cancelled) return;
        setIds(result.ids);
      })
      .catch((error) => {
        if (cancelled) return;
        setLoadError(error instanceof Error ? error.message : String(error));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, onFetch]);

  const existing = useMemo(() => new Set(existingModelIds), [existingModelIds]);
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? ids.filter((id) => id.toLowerCase().includes(q)) : ids;
  }, [ids, query]);
  // 已添加的模型不可选：部分添加失败后重试时，已成功的那部分会出现在 existing 中，
  // 必须从待提交集合里剔除，否则会重复添加。
  const selectable = useMemo(
    () => filtered.filter((id) => !existing.has(id)),
    [filtered, existing],
  );
  const pending = useMemo(
    () => [...selected].filter((id) => !existing.has(id)),
    [selected, existing],
  );

  const toggle = (id: string, checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const handleConfirm = async () => {
    if (pending.length === 0 || confirming) return;
    setConfirming(true);
    setConfirmError(null);
    try {
      await onConfirm(pending);
      onOpenChange(false);
    } catch (error) {
      setConfirmError(error instanceof Error ? error.message : String(error));
    } finally {
      setConfirming(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] flex-col gap-4 sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {intl.formatMessage({ id: "settings.modelProvider.remotePicker.title" })}
          </DialogTitle>
          <DialogDescription>
            {intl.formatMessage({ id: "settings.modelProvider.remotePicker.description" })}
          </DialogDescription>
        </DialogHeader>
        {loading ? (
          <div className="flex min-h-40 items-center justify-center gap-2 text-foreground-subtle">
            <Loader2Icon className="size-4 animate-spin" aria-hidden="true" />
            {intl.formatMessage({ id: "common.loading" })}
          </div>
        ) : loadError ? (
          <div
            role="alert"
            className="min-h-20 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-ui-sm text-destructive"
          >
            {intl.formatMessage({ id: "settings.modelProvider.remotePicker.loadFailed" })}
            {`: ${loadError}`}
          </div>
        ) : (
          <>
            <div className="relative">
              <SearchIcon
                className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-foreground-subtlest"
                aria-hidden="true"
              />
              <Input
                type="text"
                size="lg"
                className="pl-9"
                placeholder={intl.formatMessage({
                  id: "settings.modelProvider.remotePicker.searchPlaceholder",
                })}
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
            </div>
            <div className="flex items-center justify-between gap-2 text-ui-sm">
              <div className="flex items-center gap-3">
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  disabled={selectable.length === 0}
                  onClick={() =>
                    setSelected((prev) => {
                      const next = new Set(prev);
                      for (const id of selectable) next.add(id);
                      return next;
                    })
                  }
                >
                  {intl.formatMessage({ id: "settings.modelProvider.remotePicker.selectAll" })}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="text-foreground-subtle"
                  disabled={pending.length === 0}
                  onClick={() => setSelected(new Set())}
                >
                  {intl.formatMessage({ id: "settings.modelProvider.remotePicker.clearSelection" })}
                </Button>
              </div>
              <span className="text-foreground-subtle">
                {intl.formatMessage(
                  { id: "settings.modelProvider.remotePicker.selectedCount" },
                  { count: pending.length },
                )}
              </span>
            </div>
            <div className="-mr-2 min-h-0 flex-1 space-y-2 overflow-y-auto pr-2">
              {filtered.length === 0 ? (
                <div className="py-8 text-center text-ui-sm text-foreground-subtle">
                  {intl.formatMessage({ id: "settings.modelProvider.remotePicker.empty" })}
                </div>
              ) : (
                filtered.map((id) => {
                  const added = existing.has(id);
                  return (
                    <label
                      key={id}
                      className={`flex items-center gap-3 rounded-lg border border-input-border bg-input px-3 py-2.5 text-ui-sm ${
                        added ? "opacity-60" : "cursor-pointer hover:bg-surface-hover"
                      }`}
                    >
                      <Checkbox
                        disabled={added}
                        checked={added || selected.has(id)}
                        onCheckedChange={(checked) => toggle(id, checked === true)}
                      />
                      <span className="min-w-0 truncate font-medium">{id}</span>
                      {added ? (
                        <span className="ml-auto shrink-0 text-ui-xs text-foreground-subtle">
                          {intl.formatMessage({
                            id: "settings.modelProvider.remotePicker.alreadyAdded",
                          })}
                        </span>
                      ) : null}
                    </label>
                  );
                })
              )}
            </div>
          </>
        )}
        {confirmError ? (
          <div
            role="alert"
            className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-ui-sm text-destructive"
          >
            {confirmError}
          </div>
        ) : null}
        <div className="flex items-center justify-end gap-2 pt-1">
          <Button
            type="button"
            variant="ghost"
            size="lg"
            disabled={confirming}
            onClick={() => onOpenChange(false)}
          >
            {intl.formatMessage({ id: "common.cancel" })}
          </Button>
          <Button
            type="button"
            variant="default"
            size="lg"
            disabled={loading || pending.length === 0 || confirming}
            onClick={() => void handleConfirm()}
          >
            {confirming ? (
              <Loader2Icon className="size-3.5 animate-spin" aria-hidden="true" />
            ) : null}
            {intl.formatMessage({ id: "settings.modelProvider.remotePicker.addSelected" })}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
