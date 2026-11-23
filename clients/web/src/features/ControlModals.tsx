import {
  Check,
  ChevronLeft,
  ChevronRight,
  Info,
  ShieldAlert,
  ShieldCheck,
  TriangleAlert,
  X,
} from 'lucide-react';
import { type KeyboardEvent as ReactKeyboardEvent, useEffect, useMemo, useRef, useState } from 'react';
import { useNexStore } from '../core/store';
import { PendingApproval, PendingUIPrompt, UIPromptOption } from '../core/types';
import { formatMsg, type LocaleCode } from '../i18n/messages';

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
            <span>{formatMsg(locale, 'modal.theAgentWantsToRun')}</span>
            <code className="rounded bg-tag px-1.5 py-0.5 font-mono text-ui-xs text-foreground">{approval.toolName}</code>
            <span>{formatMsg(locale, 'modal.reviewTheArgumentsBeforeAllowing')}</span>
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
                {formatMsg(locale, 'modal.allowAndSwitchToFull')}
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

interface NormalizedQuestion {
  header?: string;
  question: string;
  message?: string;
  kind: 'select' | 'confirm' | 'input';
  options: UIPromptOption[];
  multiSelect: boolean;
  allowCustom: boolean;
  placeholder?: string;
  defaultValue?: string;
}

interface QuestionAnswerDraft {
  selectedIndices: number[];
  customText: string;
  useCustom: boolean;
}

function splitLegacyOption(raw: string): UIPromptOption {
  const trimmed = raw.trim();
  const emIdx = trimmed.indexOf(' — ');
  if (emIdx > 0) {
    return {
      label: trimmed.slice(0, emIdx).trim(),
      description: trimmed.slice(emIdx + 3).trim() || undefined,
    };
  }
  return { label: trimmed };
}

function normalizePromptQuestions(prompt: PendingUIPrompt, locale: LocaleCode): NormalizedQuestion[] {
  const toQuestion = (input: {
    header?: string;
    question: string;
    message?: string;
    kind?: 'select' | 'confirm' | 'input';
    options?: UIPromptOption[];
    legacyOptions?: string[];
    multiSelect?: boolean;
    allowCustom?: boolean;
    placeholder?: string;
    defaultValue?: string;
  }): NormalizedQuestion => {
    let options: UIPromptOption[] = [];
    if (input.options && input.options.length > 0) {
      options = input.options;
    } else if (input.legacyOptions && input.legacyOptions.length > 0) {
      options = input.legacyOptions.map(splitLegacyOption).filter((o) => o.label.length > 0);
    }

    const kind = input.kind || (options.length > 0 ? 'select' : 'input');
    if (kind === 'confirm' && options.length === 0) {
      options = [
        { label: formatMsg(locale, 'questionCard.yes') },
        { label: formatMsg(locale, 'questionCard.no') },
      ];
    }

    const allowCustom =
      kind === 'input'
        ? true
        : kind === 'confirm'
          ? input.allowCustom === true
          : input.allowCustom !== false;

    return {
      header: input.header?.trim() || undefined,
      question: input.question,
      message: input.message?.trim() || undefined,
      kind,
      options,
      multiSelect: Boolean(input.multiSelect),
      allowCustom,
      placeholder: input.placeholder?.trim() || undefined,
      defaultValue: input.defaultValue,
    };
  };

  if (prompt.questions && prompt.questions.length > 0) {
    return prompt.questions.map((q, idx) =>
      toQuestion({
        header: q.header || (idx === 0 ? prompt.header : undefined),
        question: q.question || prompt.title,
        message: q.message,
        kind: q.kind,
        options: q.options,
        multiSelect: q.multiSelect,
        allowCustom: q.allowCustom,
        placeholder: q.placeholder,
        defaultValue: q.defaultValue,
      }),
    );
  }

  return [
    toQuestion({
      header: prompt.header,
      question: prompt.title,
      message: prompt.message,
      kind: prompt.kind,
      options: prompt.optionItems,
      legacyOptions: prompt.options,
      multiSelect: prompt.multiSelect,
      allowCustom: prompt.allowCustom,
      placeholder: prompt.placeholder,
      defaultValue: prompt.defaultValue,
    }),
  ];
}

function initialDraftForQuestion(q: NormalizedQuestion): QuestionAnswerDraft {
  const def = (q.defaultValue || '').trim();
  if (def) {
    const matchIdx = q.options.findIndex((o) => o.label === def);
    if (matchIdx >= 0) {
      return { selectedIndices: [matchIdx], customText: '', useCustom: false };
    }
    if (q.kind === 'confirm') {
      const lower = def.toLowerCase();
      if (lower === 'yes' || lower === 'true' || lower === 'y') {
        return { selectedIndices: [0], customText: '', useCustom: false };
      }
      if (lower === 'no' || lower === 'false' || lower === 'n') {
        return { selectedIndices: [1], customText: '', useCustom: false };
      }
    }
    return { selectedIndices: [], customText: q.defaultValue || '', useCustom: true };
  }
  return { selectedIndices: [], customText: '', useCustom: false };
}

function resolveQuestionAnswer(q: NormalizedQuestion, draft: QuestionAnswerDraft): string {
  const chosen: string[] = [];
  for (const idx of draft.selectedIndices) {
    const opt = q.options[idx];
    if (opt) chosen.push(opt.label);
  }
  const custom = draft.customText.trim();
  if (custom && (draft.useCustom || q.multiSelect || chosen.length === 0)) {
    chosen.push(custom);
  }
  return chosen.join(', ');
}

export function InlineQuestionCard({ prompt }: { prompt: PendingUIPrompt }) {
  const locale = useNexStore((s) => s.locale);
  const resolveUIPrompt = useNexStore((s) => s.resolveUIPrompt);

  const questions = useMemo(() => normalizePromptQuestions(prompt, locale), [prompt, locale]);
  const [pageIdx, setPageIdx] = useState(0);
  const [drafts, setDrafts] = useState<QuestionAnswerDraft[]>(() => questions.map(initialDraftForQuestion));
  const [focusedRow, setFocusedRow] = useState<number>(0);

  const cardRef = useRef<HTMLDivElement | null>(null);
  const customInputRef = useRef<HTMLInputElement | null>(null);
  const rowBtnRefs = useRef<Array<HTMLButtonElement | null>>([]);

  useEffect(() => {
    setPageIdx(0);
    setDrafts(questions.map(initialDraftForQuestion));
    setFocusedRow(0);
  }, [prompt.requestId, questions]);

  const currentQ = questions[pageIdx] || questions[0];
  const currentDraft = drafts[pageIdx] || { selectedIndices: [], customText: '', useCustom: false };
  const rowCount = currentQ.options.length + (currentQ.allowCustom ? 1 : 0);
  const customRowIdx = currentQ.allowCustom ? currentQ.options.length : -1;

  useEffect(() => {
    if (currentQ.options.length === 0 && currentQ.allowCustom) {
      setFocusedRow(0);
      customInputRef.current?.focus();
    } else {
      setFocusedRow(0);
      cardRef.current?.focus();
    }
  }, [prompt.requestId, pageIdx, currentQ.options.length, currentQ.allowCustom]);

  const allAnswered = questions.every((q, idx) => resolveQuestionAnswer(q, drafts[idx] || { selectedIndices: [], customText: '', useCustom: false }).length > 0);

  const updateDraft = (idx: number, updater: (prev: QuestionAnswerDraft) => QuestionAnswerDraft) => {
    setDrafts((prev) => {
      const next = prev.slice();
      const base = next[idx] || { selectedIndices: [], customText: '', useCustom: false };
      next[idx] = updater(base);
      return next;
    });
  };

  const handleSubmit = () => {
    if (!allAnswered) return;
    if (questions.length === 1) {
      const q = questions[0];
      const d = drafts[0] || { selectedIndices: [], customText: '', useCustom: false };
      if (q.kind === 'confirm') {
        const confirmed = d.selectedIndices.includes(0);
        resolveUIPrompt(prompt.requestId, confirmed, confirmed ? 'yes' : 'no');
        return;
      }
      resolveUIPrompt(prompt.requestId, true, resolveQuestionAnswer(q, d));
      return;
    }

    const answers = questions.map((q, idx) => ({
      question: q.question,
      answer: resolveQuestionAnswer(q, drafts[idx] || { selectedIndices: [], customText: '', useCustom: false }),
    }));
    resolveUIPrompt(prompt.requestId, true, JSON.stringify({ answers }));
  };

  const selectOptionRow = (optIdx: number) => {
    setFocusedRow(optIdx);
    updateDraft(pageIdx, (prev) => {
      if (currentQ.multiSelect) {
        const exists = prev.selectedIndices.includes(optIdx);
        return {
          ...prev,
          selectedIndices: exists
            ? prev.selectedIndices.filter((i) => i !== optIdx)
            : [...prev.selectedIndices, optIdx].sort((a, b) => a - b),
        };
      }
      return {
        ...prev,
        selectedIndices: [optIdx],
        useCustom: false,
      };
    });
  };

  const focusRowElement = (rowIdx: number) => {
    setFocusedRow(rowIdx);
    if (rowIdx === customRowIdx) {
      customInputRef.current?.focus();
    } else {
      rowBtnRefs.current[rowIdx]?.focus();
    }
  };

  const handleCardKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.nativeEvent.isComposing) return;
    const target = e.target as HTMLElement | null;
    const inCustomInput = target === customInputRef.current;

    if (e.key === 'Escape') {
      e.preventDefault();
      resolveUIPrompt(prompt.requestId, false, '', true);
      return;
    }

    if (rowCount === 0) return;

    if (e.key === 'ArrowDown' || (e.key === 'Tab' && !e.shiftKey)) {
      e.preventDefault();
      const next = (focusedRow + 1) % rowCount;
      focusRowElement(next);
      return;
    }

    if (e.key === 'ArrowUp' || (e.key === 'Tab' && e.shiftKey)) {
      e.preventDefault();
      const prev = (focusedRow - 1 + rowCount) % rowCount;
      focusRowElement(prev);
      return;
    }

    if (inCustomInput) {
      if (e.key === 'Enter' && currentDraft.customText.trim()) {
        e.preventDefault();
        if (questions.length > 1 && pageIdx < questions.length - 1 && !allAnswered) {
          setPageIdx((p) => Math.min(questions.length - 1, p + 1));
        } else {
          handleSubmit();
        }
      }
      return;
    }

    if (e.key === ' ' || e.key === 'Enter') {
      if (focusedRow === customRowIdx) {
        e.preventDefault();
        customInputRef.current?.focus();
        return;
      }
      if (focusedRow >= 0 && focusedRow < currentQ.options.length) {
        e.preventDefault();
        const alreadySelected = !currentQ.multiSelect && currentDraft.selectedIndices.includes(focusedRow);
        if (e.key === 'Enter' && alreadySelected) {
          if (questions.length > 1 && pageIdx < questions.length - 1 && !allAnswered) {
            setPageIdx((p) => Math.min(questions.length - 1, p + 1));
          } else if (allAnswered) {
            handleSubmit();
          }
          return;
        }
        selectOptionRow(focusedRow);
      }
    }
  };

  return (
    <div
      ref={cardRef}
      tabIndex={-1}
      data-testid="inline-question-card"
      onKeyDown={handleCardKeyDown}
      className="zcode-bottom-dock mx-auto w-full max-w-4xl rounded-2xl border border-border bg-card px-5 py-4 text-foreground outline-none composer-shadow"
    >
      {/* Header row: optional tag pill + question + < 1 / N > pager */}
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2.5">
            {currentQ.header && (
              <span
                data-testid="question-card-header"
                className="inline-flex shrink-0 items-center rounded-full border border-border bg-surface px-2.5 py-0.5 text-ui-xs font-medium text-foreground-subtle"
              >
                {currentQ.header}
              </span>
            )}
            <h3 data-testid="question-card-title" className="text-[15px] font-semibold leading-snug text-foreground">
              {currentQ.question}
            </h3>
            {currentQ.multiSelect && (
              <span className="text-ui-xs text-foreground-subtlest">
                {formatMsg(locale, 'questionCard.multiSelectHint')}
              </span>
            )}
          </div>
          {currentQ.message && (
            <p className="mt-1.5 text-ui-sm leading-relaxed text-foreground-subtle">{currentQ.message}</p>
          )}
        </div>

        <div className="flex shrink-0 items-center gap-1 text-ui-sm tabular-nums text-foreground-subtlest">
          <button
            type="button"
            data-testid="question-pager-prev"
            aria-label={formatMsg(locale, 'questionCard.prevQuestion')}
            disabled={pageIdx === 0}
            onClick={() => setPageIdx((p) => Math.max(0, p - 1))}
            className="flex size-6 items-center justify-center rounded-md transition-colors hover:bg-hover hover:text-foreground disabled:opacity-35 disabled:hover:bg-transparent"
          >
            <ChevronLeft className="size-3.5" />
          </button>
          <span data-testid="question-pager-indicator" className="px-1">
            {pageIdx + 1} / {questions.length}
          </span>
          <button
            type="button"
            data-testid="question-pager-next"
            aria-label={formatMsg(locale, 'questionCard.nextQuestion')}
            disabled={pageIdx >= questions.length - 1}
            onClick={() => setPageIdx((p) => Math.min(questions.length - 1, p + 1))}
            className="flex size-6 items-center justify-center rounded-md transition-colors hover:bg-hover hover:text-foreground disabled:opacity-35 disabled:hover:bg-transparent"
          >
            <ChevronRight className="size-3.5" />
          </button>
        </div>
      </div>

      {/* Numbered Options List + Inline Custom Input */}
      <div
        role="listbox"
        aria-multiselectable={currentQ.multiSelect}
        className="mt-3.5 space-y-0.5"
      >
        {currentQ.options.map((opt, idx) => {
          const isSelected = !currentDraft.useCustom && currentDraft.selectedIndices.includes(idx);
          const isFocused = focusedRow === idx;
          return (
            <button
              key={`${pageIdx}_${idx}_${opt.label}`}
              ref={(el) => {
                rowBtnRefs.current[idx] = el;
              }}
              type="button"
              role="option"
              aria-selected={isSelected}
              data-testid={`question-option-${idx}`}
              onClick={() => selectOptionRow(idx)}
              onFocus={() => setFocusedRow(idx)}
              className={`flex w-full items-center gap-3 rounded-xl px-2.5 py-2 text-left transition-colors outline-none ${
                isSelected
                  ? 'bg-selected'
                  : isFocused
                    ? 'bg-hover/70'
                    : 'hover:bg-hover/60'
              } ${isFocused ? 'ring-1 ring-border-hover' : ''}`}
            >
              <span className="w-5 shrink-0 text-right text-ui-base tabular-nums text-foreground-subtlest">
                {idx + 1}.
              </span>
              <span className="min-w-0 flex-1 flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
                <span className="text-ui-base font-semibold text-foreground">{opt.label}</span>
                {opt.description && (
                  <span className="text-ui-base text-foreground-subtle">{opt.description}</span>
                )}
              </span>
              {isSelected && <Check className="size-4 shrink-0 text-foreground" />}
            </button>
          );
        })}

        {currentQ.allowCustom && (
          <div
            onClick={() => {
              setFocusedRow(customRowIdx);
              customInputRef.current?.focus();
            }}
            className={`flex items-center gap-3 rounded-xl px-2.5 py-2 transition-colors ${
              currentDraft.useCustom && currentDraft.customText.trim()
                ? 'bg-selected'
                : focusedRow === customRowIdx
                  ? 'bg-hover/50'
                  : 'hover:bg-hover/40'
            } ${focusedRow === customRowIdx ? 'ring-1 ring-border-hover' : ''}`}
          >
            <span className="w-5 shrink-0 text-right text-ui-base tabular-nums text-foreground-subtlest">
              {currentQ.options.length + 1}.
            </span>
            <input
              ref={customInputRef}
              type="text"
              data-testid="question-custom-input"
              value={currentDraft.customText}
              placeholder={currentQ.placeholder || formatMsg(locale, 'questionCard.customPlaceholder')}
              onFocus={() => setFocusedRow(customRowIdx)}
              onChange={(e) => {
                const nextVal = e.target.value;
                updateDraft(pageIdx, (prev) => ({
                  selectedIndices: currentQ.multiSelect ? prev.selectedIndices : nextVal.trim() ? [] : prev.selectedIndices,
                  customText: nextVal,
                  useCustom: Boolean(nextVal.trim()),
                }));
              }}
              className="min-w-0 flex-1 bg-transparent text-ui-base text-foreground placeholder-foreground-subtlest outline-none"
            />
          </div>
        )}
      </div>

      {/* Footer: Keyboard Hint + Dismiss & Submit */}
      <div className="mt-4 flex flex-wrap items-center justify-between gap-3 pt-0.5">
        <div className="flex min-w-0 items-center gap-2 text-ui-sm text-foreground-subtlest">
          <Info className="size-4 shrink-0" />
          <span className="leading-snug">{formatMsg(locale, 'questionCard.keyboardHint')}</span>
        </div>

        <div className="flex shrink-0 items-center gap-2">
          <button
            type="button"
            data-testid="question-dismiss-btn"
            onClick={() => resolveUIPrompt(prompt.requestId, false, '', true)}
            className="h-8 rounded-lg border border-border bg-background px-3.5 text-ui-sm font-medium text-foreground transition-colors hover:bg-hover"
          >
            {formatMsg(locale, 'questionCard.dismiss')}
          </button>
          <button
            type="button"
            data-testid="question-submit-btn"
            disabled={!allAnswered}
            onClick={handleSubmit}
            className="h-8 rounded-lg bg-foreground px-3.5 text-ui-sm font-medium text-background transition-opacity hover:opacity-90 disabled:opacity-35"
          >
            {formatMsg(locale, 'questionCard.submit')}
          </button>
        </div>
      </div>
    </div>
  );
}

export function UIPromptModal({ prompt }: { prompt: PendingUIPrompt }) {
  return (
    <div className="zcode-dialog-overlay fixed inset-0 z-50 flex items-end justify-center bg-black/45 p-4 backdrop-blur-[1px] sm:items-center">
      <div className="w-full max-w-3xl">
        <InlineQuestionCard prompt={prompt} />
      </div>
    </div>
  );
}
