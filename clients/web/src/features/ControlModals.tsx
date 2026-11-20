import { ShieldAlert, ShieldCheck, TriangleAlert, X } from 'lucide-react';
import { useState } from 'react';
import { useNexStore } from '../core/store';
import { PendingApproval, PendingUIPrompt } from '../core/types';
import { formatMsg } from '../i18n/messages';

export function ApprovalModal({ approval }: { approval: PendingApproval }) {
  const locale = useNexStore((s) => s.locale);
  const resolveApproval = useNexStore((s) => s.resolveApproval);
  const setPermissionMode = useNexStore((s) => s.setPermissionMode);

  const argsPreview = approval.argsJson || '{}';

  return (
    <div className="zcode-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4 backdrop-blur-[1px]">
      <div className="zcode-dialog-content w-full max-w-lg rounded-2xl border border-border bg-popover p-5 text-foreground shadow-lg">
        <div className="flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-warning-surface text-warning-surface-fg">
              <ShieldAlert className="size-4" />
            </span>
            <h3 className="truncate text-ui-base font-semibold text-foreground">
              {formatMsg(locale, 'chat.permission.title')}
            </h3>
            <span className="shrink-0 rounded-md border border-warning/30 bg-warning-surface px-1.5 py-0.5 text-ui-xs font-medium text-warning-surface-fg">
              {formatMsg(locale, 'chat.permission.awaiting')}
            </span>
          </div>
          <span className="shrink-0 font-mono text-ui-xs text-foreground-subtlest">{approval.runnerId}</span>
        </div>

        <div className="mt-3 flex items-start gap-2 rounded-xl border border-border bg-surface px-3 py-2.5">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" />
          <div className="min-w-0 text-ui-sm text-foreground-subtle">
            <span>{locale === 'en-US' ? 'The agent wants to run ' : 'Agent 希望执行工具 '}</span>
            <code className="rounded bg-tag px-1.5 py-0.5 font-mono text-ui-xs text-foreground">{approval.toolName}</code>
            <span>{locale === 'en-US' ? ' — review the arguments before allowing.' : '，请先确认调用参数。'}</span>
          </div>
        </div>

        <pre className="mt-3 max-h-56 overflow-auto rounded-xl border border-border bg-surface p-3 font-mono text-ui-sm whitespace-pre-wrap text-foreground-subtle">
          {argsPreview}
        </pre>

        <div className="mt-4 flex flex-wrap items-center justify-between gap-2">
          <button
            type="button"
            onClick={() => resolveApproval(approval.approvalId, false)}
            className="flex h-8 items-center gap-1.5 rounded-lg border border-border px-3.5 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            <X className="size-3.5" />
            <span>{formatMsg(locale, 'chat.permission.deny')}</span>
          </button>

          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => {
                resolveApproval(approval.approvalId, true);
                setPermissionMode('full_access');
              }}
              className="flex h-8 items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-ui-sm font-medium text-foreground transition-colors hover:bg-surface-hover"
            >
              <ShieldCheck className="size-3.5 text-success" />
              <span>
                {locale === 'en-US' ? 'Allow and switch to full access' : '允许，并切换为完全访问'}
              </span>
            </button>
            <button
              type="button"
              onClick={() => resolveApproval(approval.approvalId, true)}
              className="flex h-8 items-center gap-1.5 rounded-lg bg-primary px-4 text-ui-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
            >
              <span>{formatMsg(locale, 'chat.permission.approve')}</span>
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

export function UIPromptModal({ prompt }: { prompt: PendingUIPrompt }) {
  const locale = useNexStore((s) => s.locale);
  const resolveUIPrompt = useNexStore((s) => s.resolveUIPrompt);
  const [val, setVal] = useState(prompt.defaultValue || '');

  return (
    <div className="zcode-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4 backdrop-blur-[1px]">
      <div className="zcode-dialog-content w-full max-w-md rounded-2xl border border-border bg-popover p-5 text-foreground shadow-lg">
        <h3 className="text-ui-lg font-semibold text-foreground">{prompt.title}</h3>
        {prompt.message && <p className="mt-1.5 text-ui-sm text-foreground-subtle">{prompt.message}</p>}

        {prompt.kind === 'select' && (
          <div className="mt-3 space-y-1">
            {(prompt.options || []).map((opt) => (
              <button
                key={opt}
                type="button"
                onClick={() => resolveUIPrompt(prompt.requestId, true, opt)}
                className="w-full rounded-lg border border-border bg-surface px-3 py-2 text-left text-ui-base text-foreground transition-colors hover:border-border-hover hover:bg-surface-hover"
              >
                {opt}
              </button>
            ))}
          </div>
        )}

        {prompt.kind === 'input' && (
          <input
            value={val}
            onChange={(e) => setVal(e.target.value)}
            placeholder={prompt.placeholder || ''}
            className="mt-3 h-9 w-full rounded-lg border border-border bg-input px-3 text-ui-base text-foreground placeholder-foreground-subtlest"
          />
        )}

        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            onClick={() => resolveUIPrompt(prompt.requestId, false, '', true)}
            className="h-8 rounded-lg border border-border px-3 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            {locale === 'en-US' ? 'Cancel' : '取消'}
          </button>
          {prompt.kind !== 'select' && (
            <button
              type="button"
              onClick={() => resolveUIPrompt(prompt.requestId, true, val)}
              className="h-8 rounded-lg bg-primary px-4 text-ui-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
            >
              {locale === 'en-US' ? 'Confirm' : '确认'}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
