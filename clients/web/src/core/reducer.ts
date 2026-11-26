import {
  BackgroundTaskItem,
  ChatTurnItem,
  CompactionCard,
  ContextCategoryItem,
  GoalDividerCard,
  GoalEntry,
  GoalPhase,
  PendingApproval,
  PendingUIPrompt,
  TodoItem,
  TurnTelemetry,
  UIPromptOption,
  UIPromptQuestion,
} from './types';

export interface SessionStreamState {
  lastSeq: number;
  running: boolean;
  runStartedAt?: number;
  items: ChatTurnItem[];
  latestTelemetry: TurnTelemetry | null;
  turnTelemetries: TurnTelemetry[];
  uiBadges: Record<string, string>;
  pendingApproval: PendingApproval | null;
  pendingUIPrompt: PendingUIPrompt | null;
  todos: TodoItem[];
  goals: GoalEntry[];
  backgroundTasks?: BackgroundTaskItem[];
  lastError?: string;
}

export function emptyStreamState(): SessionStreamState {
  return {
    lastSeq: 0,
    running: false,
    runStartedAt: undefined,
    items: [],
    latestTelemetry: null,
    turnTelemetries: [],
    uiBadges: {},
    pendingApproval: null,
    pendingUIPrompt: null,
    todos: [],
    goals: [],
    backgroundTasks: [],
    lastError: undefined,
  };
}

/** todo_write is emitted by harnesses as `todo_write` and by Claude-style agents as `TodoWrite`. */
export function isTodoWriteTool(name: string): boolean {
  return name.trim().toLowerCase().replace(/[_-]/g, '') === 'todowrite';
}

function normalizeTodoStatus(raw: unknown): TodoItem['status'] {
  return raw === 'completed' || raw === 'in_progress' ? raw : 'pending';
}

/**
 * Normalizes a raw `todos` array (from `arguments.todos` or `tool_result.metadata.todos`) into
 * strictly typed TodoItem records. Returns null when the payload is not an array so callers can
 * keep the previous reference untouched.
 */
export function normalizeTodos(raw: unknown): TodoItem[] | null {
  if (!Array.isArray(raw)) return null;
  const out: TodoItem[] = [];
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object') continue;
    const rec = entry as Record<string, unknown>;
    const content = typeof rec.content === 'string' ? rec.content.trim() : '';
    if (!content) continue;
    const priority =
      rec.priority === 'high' || rec.priority === 'medium' || rec.priority === 'low' ? rec.priority : undefined;
    const item: TodoItem = {
      content,
      status: normalizeTodoStatus(rec.status),
    };
    if (priority) item.priority = priority;
    out.push(item);
  }
  return out;
}

function parseArgumentTodos(rawArgs: unknown): TodoItem[] | null {
  if (typeof rawArgs === 'string') {
    try {
      return parseArgumentTodos(JSON.parse(rawArgs));
    } catch {
      return null;
    }
  }
  if (!rawArgs || typeof rawArgs !== 'object') return null;
  return normalizeTodos((rawArgs as Record<string, unknown>).todos);
}

function countTodos(todos: TodoItem[]): { completed: number; total: number } {
  return {
    completed: todos.filter((t) => t.status === 'completed').length,
    total: todos.length,
  };
}

/** Extracts a concise goal title from a `/goal …` prompt or an `<untrusted_objective>` wrapper. */
export function parseGoalTitle(text: string): string | null {
  const trimmed = (text || '').trim();
  if (!trimmed) return null;
  const lower = trimmed.toLowerCase();
  const openIdx = lower.indexOf('<untrusted_objective>');
  if (openIdx >= 0) {
    const afterOpen = trimmed.slice(openIdx + '<untrusted_objective>'.length);
    const closeIdx = afterOpen.toLowerCase().indexOf('</untrusted_objective>');
    const body = (closeIdx >= 0 ? afterOpen.slice(0, closeIdx) : afterOpen).trim();
    return body || null;
  }
  if (/^\/goal(?:\s|$)/i.test(trimmed)) {
    const body = trimmed.replace(/^\/goal\s*/i, '').trim();
    return body || null;
  }
  return null;
}

/** Seals any trailing streaming/optimistic turns and settles dangling running tool calls. */
export function sealRunningItems(items: ChatTurnItem[]): ChatTurnItem[] {
  const needsSeal = items.some(
    (it) =>
      it.streaming ||
      it.optimistic ||
      it.compaction?.status === 'compacting' ||
      it.toolCalls.some((tc) => tc.status === 'running'),
  );
  if (!needsSeal) return items;
  return items.map((it) => {
    const hasRunningTool = it.toolCalls.some((tc) => tc.status === 'running');
    const hasRunningCompaction = it.compaction?.status === 'compacting';
    if (!it.streaming && !it.optimistic && !hasRunningTool && !hasRunningCompaction) return it;
    return {
      ...it,
      streaming: false,
      optimistic: false,
      ...(hasRunningCompaction && it.compaction
        ? { compaction: { ...it.compaction, status: 'failed' as const } }
        : {}),
      toolCalls: hasRunningTool
        ? it.toolCalls.map((tc) =>
            tc.status === 'running'
              ? { ...tc, status: 'error' as const, finalOutput: tc.finalOutput || tc.streamOutput }
              : tc,
          )
        : it.toolCalls,
    };
  });
}

export function mergeCompactedTelemetry(
  baseTelemetry: TurnTelemetry | null,
  afterTokens: number,
  options: {
    id: string;
    sessionId: string;
    turnIdx: number;
    runnerId: string;
    recordedAtSec: number;
    inlineMessages?: string;
  },
): TurnTelemetry {
  const prevWin = baseTelemetry?.context_window || 200000;
  const baseBreakdown = baseTelemetry?.breakdown || [];
  const nonMsgItems = baseBreakdown.filter((b) => b.category !== 'messages');
  const nonMsgTokens = nonMsgItems.reduce((acc, b) => acc + (b.tokens || 0), 0);
  const sectionTokens = nonMsgItems
    .filter((b) => b.category === 'system_prompt' || b.category === 'skills' || b.category === 'meta_context')
    .reduce((acc, b) => acc + (b.tokens || 0), 0);

  const messagesTokens =
    baseBreakdown.length > 0 && afterTokens > sectionTokens ? afterTokens - sectionTokens : afterTokens;
  const totalTokens = messagesTokens + nonMsgTokens;
  const pct = prevWin > 0 ? Math.min(100, Number(((totalTokens * 100) / prevWin).toFixed(1))) : 0;
  const pctFor = (tokens: number): number =>
    totalTokens > 0 ? Number(((tokens * 100) / totalTokens).toFixed(1)) : 0;

  const nextBreakdown: ContextCategoryItem[] =
    baseBreakdown.length > 0
      ? baseBreakdown.map((b) =>
          b.category === 'messages'
            ? {
                ...b,
                tokens: messagesTokens,
                pct: pctFor(messagesTokens),
                blob_hash: '',
                inline_content: options.inlineMessages ?? b.inline_content,
              }
            : { ...b, pct: pctFor(b.tokens) },
        )
      : [
          {
            category: 'messages' as const,
            bytes: 0,
            tokens: messagesTokens,
            pct: 100,
            blob_hash: '',
            inline_content: options.inlineMessages,
          },
        ];

  return baseTelemetry
    ? {
        ...baseTelemetry,
        id: options.id,
        total_context_tokens: totalTokens,
        window_utilization_pct: pct,
        input_tokens: totalTokens,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        output_tokens: 0,
        raw_trace_blob_hash: '',
        breakdown: nextBreakdown,
        recorded_at: options.recordedAtSec,
      }
    : {
        id: options.id,
        session_id: options.sessionId,
        turn_idx: options.turnIdx,
        runner_id: options.runnerId,
        provider_id: '',
        model_id: '',
        trigger_source: 'chat',
        context_window: prevWin,
        total_context_tokens: totalTokens,
        window_utilization_pct: pct,
        input_tokens: totalTokens,
        output_tokens: 0,
        reasoning_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cache_hit_rate_pct: 0,
        estimated_cost_usd: 0,
        cache_saved_usd: 0,
        breakdown: nextBreakdown,
        system_prompt_blob_hash: '',
        raw_trace_blob_hash: '',
        ttft_ms: 0,
        duration_ms: 0,
        recorded_at: options.recordedAtSec,
      };
}

export interface RawSessionEventRow {
  session_id: string;
  seq: number;
  turn_idx: number;
  event_type: string;
  payload: Record<string, unknown>;
  created_at?: number;
}

/**
 * O(tail) reducer preserving exact object references for all earlier historical ChatTurnItems
 * so memoized turn rows NEVER re-render during high-frequency streaming!
 */
export function applySessionEvent(
  prev: SessionStreamState,
  runnerId: string,
  ev: RawSessionEventRow,
): SessionStreamState {
  if (ev.seq > 0 && ev.seq <= prev.lastSeq) {
    return prev;
  }

  let items = prev.items;
  let running = prev.running;
  let runStartedAt = prev.runStartedAt;
  let latestTelemetry = prev.latestTelemetry;
  let turnTelemetries = prev.turnTelemetries || [];
  let uiBadges = prev.uiBadges;
  let pendingApproval = prev.pendingApproval;
  let pendingUIPrompt = prev.pendingUIPrompt;
  let todos = prev.todos;
  let goals = prev.goals;
  let backgroundTasks = prev.backgroundTasks || [];
  let lastError = prev.lastError;

  const p = ev.payload || {};

  const sameTodos = (a: TodoItem[], b: TodoItem[]): boolean =>
    a === b ||
    (a.length === b.length &&
      a.every(
        (it, i) =>
          it.content === b[i].content && it.status === b[i].status && (it.priority || '') === (b[i].priority || ''),
      ));

  // Syncs the latest completed/total counters onto the active goal iteration (if any).
  const applyTodos = (next: TodoItem[] | null) => {
    if (!next) return;
    if (sameTodos(todos, next)) return;
    todos = next;
    if (goals.length === 0) return;
    const counts = countTodos(next);
    let targetIdx = -1;
    for (let i = goals.length - 1; i >= 0; i--) {
      if (goals[i].status === 'running') {
        targetIdx = i;
        break;
      }
    }
    if (targetIdx < 0) targetIdx = goals.length - 1;
    const target = goals[targetIdx];
    if (target.completedTodos === counts.completed && target.totalTodos === counts.total) return;
    const nextGoals = goals.slice();
    nextGoals[targetIdx] = { ...target, completedTodos: counts.completed, totalTodos: counts.total };
    goals = nextGoals;
  };

  const findToolName = (callId: string): string => {
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      if (it.role !== 'assistant') continue;
      const tc = it.toolCalls.find((t) => t.id === callId);
      if (tc) return tc.name;
    }
    return '';
  };

  // Clones ONLY the tail assistant item (or appends a new one if the last item was a user message),
  // keeping items[0 .. len-2] reference-identical (`prev.items[i] === next.items[i]`)!
  const cloneOrAppendTailAssistant = (): ChatTurnItem => {
    const lastIdx = items.length - 1;
    if (lastIdx >= 0 && items[lastIdx].role === 'assistant') {
      const prevTail = items[lastIdx];
      const cloned: ChatTurnItem = {
        ...prevTail,
        toolCalls: prevTail.toolCalls,
        streaming: true,
      };
      items = items.slice();
      items[lastIdx] = cloned;
      return cloned;
    }
    const created: ChatTurnItem = {
      id: `ast_${ev.seq || Date.now()}`,
      startSeq: ev.seq > 0 ? ev.seq : undefined,
      role: 'assistant',
      text: '',
      thinking: '',
      toolCalls: [],
      streaming: true,
    };
    items = [...items, created];
    return created;
  };

  // Mutates ONLY the single item containing callId (scanning backwards from tail in O(tail) time!).
  const updateToolCallFromTail = (
    callId: string,
    updater: (tc: ChatTurnItem['toolCalls'][number]) => ChatTurnItem['toolCalls'][number],
  ) => {
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      if (it.role !== 'assistant') continue;
      const tcIdx = it.toolCalls.findIndex((tc) => tc.id === callId);
      if (tcIdx >= 0) {
        const nextTools = it.toolCalls.slice();
        nextTools[tcIdx] = updater(nextTools[tcIdx]);
        const nextItems = items.slice();
        nextItems[i] = { ...it, toolCalls: nextTools };
        items = nextItems;
        return;
      }
    }
  };

  const eventTimestamp = (fallbackNow = Date.now()): number => {
    if (typeof ev.created_at === 'number' && Number.isFinite(ev.created_at) && ev.created_at > 0) {
      return ev.created_at < 1e12 ? ev.created_at * 1000 : ev.created_at;
    }
    const msgObj = p.message as { timestamp?: unknown } | undefined;
    if (msgObj && typeof msgObj.timestamp === 'number' && Number.isFinite(msgObj.timestamp) && msgObj.timestamp > 0) {
      return msgObj.timestamp < 1e12 ? msgObj.timestamp * 1000 : msgObj.timestamp;
    }
    return fallbackNow;
  };

  switch (ev.event_type) {
    case 'agent_start':
      running = true;
      lastError = undefined;
      if (!runStartedAt) runStartedAt = eventTimestamp();
      break;

    case 'agent_end': {
      running = false;
      runStartedAt = undefined;
      pendingUIPrompt = null;
      pendingApproval = null;
      items = sealRunningItems(items);
      if (typeof p.error === 'string' && p.error.trim()) {
        lastError = p.error.trim();
      }
      if (goals.some((g) => g.status === 'running')) {
        const completedAt = eventTimestamp();
        const reason = String(p.reason || '').trim().toLowerCase();
        const errText = typeof p.error === 'string' && p.error.trim() ? p.error.trim() : undefined;
        const terminalStatus: GoalEntry['status'] =
          reason === 'aborted' ? 'aborted' : errText || reason === 'error' ? 'failed' : 'completed';
        goals = goals.map((g) =>
          g.status === 'running'
            ? {
                ...g,
                status: terminalStatus,
                phase: terminalStatus,
                ...(errText ? { error: errText } : {}),
                completedAt,
              }
            : g,
        );
      }
      break;
    }

    case 'goal_lifecycle': {
      const g = p.goal && typeof p.goal === 'object' ? (p.goal as Record<string, unknown>) : null;
      if (!g) break;
      const goalId = String(g.goal_id || `goal_${ev.seq || goals.length + 1}`);
      const phase = String(g.phase || 'started') as GoalPhase;
      const objective = String(g.objective || '').trim() || undefined;
      const round = typeof g.round === 'number' && g.round > 0 ? g.round : undefined;
      const maxRounds = typeof g.max_rounds === 'number' && g.max_rounds > 0 ? g.max_rounds : undefined;
      const achieved = typeof g.achieved === 'boolean' ? g.achieved : undefined;
      const verdict = String(g.verdict || '').trim() || undefined;
      const missingItems = Array.isArray(g.missing_items)
        ? g.missing_items.map((x) => String(x || '').trim()).filter(Boolean)
        : undefined;
      const nextStep = String(g.next_step || '').trim() || undefined;
      const errMsg = String(g.error || p.error || '').trim() || undefined;

      const mappedStatus: GoalEntry['status'] =
        phase === 'completed'
          ? 'completed'
          : phase === 'failed' || phase === 'max_rounds'
            ? 'failed'
            : phase === 'aborted'
              ? 'aborted'
              : phase === 'blocked'
                ? 'blocked'
                : 'running';

      const counts = countTodos(todos);
      const existingIdx = goals.findIndex((it) => it.id === goalId);
      const ts = eventTimestamp();
      if (existingIdx >= 0) {
        const nextGoals = goals.slice();
        const prevGoal = nextGoals[existingIdx];
        nextGoals[existingIdx] = {
          ...prevGoal,
          title: objective || prevGoal.title,
          status: mappedStatus,
          phase,
          round: round ?? prevGoal.round,
          maxRounds: maxRounds ?? prevGoal.maxRounds,
          lastVerdict: verdict ?? prevGoal.lastVerdict,
          missingItems: missingItems ?? (achieved ? undefined : prevGoal.missingItems),
          error: errMsg ?? prevGoal.error,
          completedTodos: counts.completed,
          totalTodos: counts.total,
          completedAt: mappedStatus !== 'running' ? ts : prevGoal.completedAt,
        };
        goals = nextGoals;
      } else {
        let nextGoals = goals;
        const lastGoal = nextGoals[nextGoals.length - 1];
        if (lastGoal && lastGoal.status === 'running') {
          nextGoals = nextGoals.slice();
          nextGoals[nextGoals.length - 1] = { ...lastGoal, status: 'completed', completedAt: ts };
        }
        goals = [
          ...nextGoals,
          {
            id: goalId,
            index: nextGoals.length + 1,
            title: objective || 'Goal',
            status: mappedStatus,
            phase,
            round,
            maxRounds,
            lastVerdict: verdict,
            missingItems,
            error: errMsg,
            startedAt: ts,
            completedAt: mappedStatus !== 'running' ? ts : undefined,
            completedTodos: counts.completed,
            totalTodos: counts.total,
          },
        ];
      }

      if (phase !== 'started') {
        const card: GoalDividerCard = {
          goalId,
          phase,
          round,
          maxRounds,
          achieved,
          verdict,
          missingItems,
          nextStep,
          error: errMsg,
          objective,
        };
        const nextItems = items.slice();
        const tail = nextItems[nextItems.length - 1];
        const canUpdateTail =
          tail &&
          tail.role === 'goal' &&
          tail.goalDivider?.goalId === goalId &&
          ((phase === 'round_verified' &&
            tail.goalDivider.phase === 'verifying' &&
            tail.goalDivider.round === round) ||
            ((phase === 'completed' ||
              phase === 'max_rounds' ||
              phase === 'failed' ||
              phase === 'aborted' ||
              phase === 'blocked') &&
              (tail.goalDivider.phase === 'round_verified' || tail.goalDivider.phase === 'verifying') &&
              (!round || tail.goalDivider.round === round)));

        if (canUpdateTail) {
          nextItems[nextItems.length - 1] = {
            ...tail,
            seq: ev.seq > 0 ? ev.seq : tail.seq,
            goalDivider: {
              ...tail.goalDivider,
              ...card,
            },
            streaming: false,
          };
        } else {
          nextItems.push({
            id: `goal_div_${ev.seq || Date.now()}_${nextItems.length}`,
            seq: ev.seq > 0 ? ev.seq : undefined,
            role: 'goal',
            text: '',
            thinking: '',
            toolCalls: [],
            goalDivider: card,
            streaming: phase === 'verifying',
          });
        }
        items = nextItems;
      }
      break;
    }

    case 'message_start': {
      const lastIdx = items.length - 1;
      const tail = lastIdx >= 0 ? items[lastIdx] : undefined;
      if (tail && tail.role === 'assistant' && tail.text.trim().length > 0) {
        const nextItems = items.slice();
        nextItems[lastIdx] = { ...tail, streaming: false };
        nextItems.push({
          id: `ast_${ev.seq || Date.now()}_${nextItems.length}`,
          startSeq: ev.seq > 0 ? ev.seq : undefined,
          role: 'assistant',
          text: '',
          thinking: '',
          toolCalls: [],
          streaming: true,
        });
        items = nextItems;
      } else {
        cloneOrAppendTailAssistant();
      }
      break;
    }

    case 'message_update': {
      const target = cloneOrAppendTailAssistant();
      const kind = String(p.delta_kind || '');
      const delta = String(p.delta || '');
      if (kind === 'text') {
        target.text += delta;
      } else if (kind === 'thinking') {
        target.thinking += delta;
      }
      break;
    }

    case 'message_end': {
      const msg = p.message as { role?: string; content?: Array<Record<string, unknown>> } | undefined;
      if (!msg) break;
      if (msg.role === 'user') {
        // Seal previous assistant turn if still marked streaming
        const lastIdx = items.length - 1;
        if (lastIdx >= 0 && items[lastIdx].streaming) {
          items = items.slice();
          items[lastIdx] = { ...items[lastIdx], streaming: false };
        }
        const text = (msg.content || [])
          .filter((b) => b.type === 'text')
          .map((b) => String(b.text || ''))
          .join('');
        if (/^<goal-verification-feedback\b/i.test(text.trim())) {
          break;
        }
        const images = (msg.content || [])
          .filter((b) => b.type === 'image')
          .map((b) => ({
            mime_type: String(b.mime_type || 'image/png'),
            data_b64: b.data_b64 ? String(b.data_b64) : undefined,
            image_url: b.image_url ? String(b.image_url) : undefined,
          }));
        if (text || images.length > 0) {
          const authUserItem: ChatTurnItem = {
            id: `usr_${ev.seq || items.length}`,
            seq: ev.seq || undefined,
            role: 'user',
            text,
            thinking: '',
            images: images.length > 0 ? images : undefined,
            toolCalls: [],
          };
          let replacedIdx = -1;
          for (let i = items.length - 1; i >= 0; i--) {
            const candidate = items[i];
            if (candidate.role === 'user' && candidate.optimistic) {
              replacedIdx = i;
              break;
            }
            if (
              candidate.role === 'assistant' &&
              (candidate.seq || candidate.text || candidate.thinking || candidate.toolCalls.length > 0)
            ) {
              break;
            }
          }
          if (replacedIdx >= 0) {
            items = items.slice();
            items[replacedIdx] = authUserItem;
          } else {
            items = [...items, authUserItem];
          }
          const goalTitle = parseGoalTitle(text);
          if (goalTitle) {
            const lastGoal = goals[goals.length - 1];
            const alreadyActive =
              lastGoal &&
              (lastGoal.title === goalTitle ||
                (lastGoal.status === 'running' && lastGoal.id.startsWith('goal_')));
            if (!alreadyActive) {
              if (lastGoal && lastGoal.status === 'running') {
                goals = goals.slice();
                goals[goals.length - 1] = { ...lastGoal, status: 'completed', completedAt: eventTimestamp() };
              }
              const counts = countTodos(todos);
              goals = [
                ...goals,
                {
                  id: `goal_${ev.seq || goals.length + 1}`,
                  index: goals.length + 1,
                  title: goalTitle,
                  status: 'running',
                  startedAt: eventTimestamp(),
                  completedTodos: counts.completed,
                  totalTodos: counts.total,
                },
              ];
            }
          }
        }
      } else if (msg.role === 'assistant') {
        const lastIdx = items.length - 1;
        const rawTail = lastIdx >= 0 ? items[lastIdx] : undefined;
        if (
          rawTail &&
          rawTail.role === 'assistant' &&
          Boolean(rawTail.seq) &&
          rawTail.seq !== ev.seq &&
          rawTail.text.trim().length > 0
        ) {
          const nextItems = items.slice();
          nextItems[lastIdx] = { ...rawTail, streaming: false };
          nextItems.push({
            id: `ast_${ev.seq || Date.now()}_${nextItems.length}`,
            startSeq: ev.seq > 0 ? ev.seq : undefined,
            role: 'assistant',
            text: '',
            thinking: '',
            toolCalls: [],
            streaming: true,
          });
          items = nextItems;
        }
        const target = cloneOrAppendTailAssistant();
        if (ev.seq) {
          target.seq = ev.seq;
        }
        const blocks = msg.content || [];
        const stepText = blocks
          .filter((b) => b.type === 'text')
          .map((b) => String(b.text || ''))
          .join('');
        const stepThink = blocks
          .filter((b) => b.type === 'thinking')
          .map((b) => String(b.thinking || ''))
          .join('');

        // Replace live delta buffer of current step cleanly without duplicating multi-step text
        if (stepText) {
          target.text = stepText;
        }
        if (stepThink) {
          target.thinking = target.thinking && !target.thinking.includes(stepThink)
            ? `${target.thinking}\n${stepThink}`.trim()
            : stepThink;
        }

        let nextTools = target.toolCalls;
        for (const b of blocks) {
          if (b.type === 'tool_call') {
            const callId = String(b.tool_call_id || '');
            const toolName = String(b.tool_name || '');
            if (isTodoWriteTool(toolName)) {
              applyTodos(parseArgumentTodos(b.arguments));
            }
            if (!nextTools.some((tc) => tc.id === callId)) {
              if (nextTools === target.toolCalls) nextTools = target.toolCalls.slice();
              nextTools.push({
                id: callId,
                name: toolName,
                argsText: typeof b.arguments === 'string' ? b.arguments : JSON.stringify(b.arguments || {}),
                status: 'running',
                streamOutput: '',
              });
            }
          }
        }
        target.toolCalls = nextTools;
        target.streaming = running;
      }
      break;
    }

    case 'tool_execution_start': {
      const callId = String(p.tool_call_id || '');
      const toolName = String(p.tool_name || '') || findToolName(callId);
      updateToolCallFromTail(callId, (tc) => ({ ...tc, status: 'running' }));
      if (isTodoWriteTool(toolName)) {
        applyTodos(parseArgumentTodos(p.tool_args ?? p.arguments));
      }
      break;
    }

    case 'tool_execution_update': {
      const callId = String(p.tool_call_id || '');
      const delta = String(p.delta || '');
      updateToolCallFromTail(callId, (tc) => {
        const combined = tc.streamOutput + delta;
        // Cap live stream preview buffer in UI state to 32KB to prevent DOM bloat
        const capped = combined.length > 32768 ? combined.slice(-32768) : combined;
        return { ...tc, streamOutput: capped };
      });
      break;
    }

    case 'tool_execution_end': {
      const callId = String(p.tool_call_id || '');
      const tr = (p.tool_result || {}) as {
        output?: string;
        is_error?: boolean;
        extra_blocks?: Array<{
          type?: string;
          mime_type?: string;
          data_b64?: string;
          image_url?: string;
        }>;
        diff?: string;
        additions?: number;
        deletions?: number;
        metadata?: Record<string, unknown>;
      };
      const resultImages = (tr.extra_blocks || [])
        .filter((b) => b.type === 'image' && (b.data_b64 || b.image_url))
        .map((b) => ({
          mime_type: b.mime_type || 'image/jpeg',
          data_b64: b.data_b64,
          image_url: b.image_url,
        }));
      const endedToolName = String(p.tool_name || '') || findToolName(callId);
      updateToolCallFromTail(callId, (tc) => ({
        ...tc,
        status: tr.is_error ? 'error' : 'completed',
        finalOutput: tr.output || tc.streamOutput,
        images: resultImages.length > 0 ? resultImages : tc.images,
        diff: tr.diff,
        additions: tr.additions,
        deletions: tr.deletions,
      }));
      if (isTodoWriteTool(endedToolName)) {
        applyTodos(normalizeTodos(tr.metadata?.todos) ?? parseArgumentTodos(p.tool_args ?? p.arguments));
      }
      break;
    }

    case 'approval_requested':
      pendingApproval = {
        approvalId: String(p.approval_id || ''),
        sessionId: ev.session_id,
        runnerId,
        toolCallId: String(p.tool_call_id || ''),
        toolName: String(p.tool_name || ''),
        argsJson: typeof p.tool_args === 'string' ? p.tool_args : JSON.stringify(p.tool_args || {}, null, 2),
      };
      break;

    case 'approval_resolved':
      pendingApproval = null;
      break;

    case 'ui_prompt_requested': {
      const uip = (p.ui_prompt || {}) as Record<string, unknown>;
      const parseOptionItems = (raw: unknown): UIPromptOption[] | undefined => {
        if (!Array.isArray(raw)) return undefined;
        const parsed: UIPromptOption[] = [];
        for (const item of raw) {
          if (typeof item === 'string') {
            const label = item.trim();
            if (label) parsed.push({ label });
          } else if (item && typeof item === 'object') {
            const rec = item as Record<string, unknown>;
            const label = String(rec.label || rec.title || '').trim();
            const desc = rec.description ? String(rec.description).trim() : undefined;
            if (label) parsed.push({ label, description: desc || undefined });
          }
        }
        return parsed.length > 0 ? parsed : undefined;
      };
      const parseQuestions = (raw: unknown): UIPromptQuestion[] | undefined => {
        if (!Array.isArray(raw) || raw.length === 0) return undefined;
        const list: UIPromptQuestion[] = [];
        for (const q of raw) {
          if (!q || typeof q !== 'object') continue;
          const rec = q as Record<string, unknown>;
          const question = String(rec.question || '').trim();
          if (!question) continue;
          list.push({
            id: rec.id ? String(rec.id) : undefined,
            header: rec.header ? String(rec.header) : undefined,
            question,
            message: rec.message ? String(rec.message) : undefined,
            kind: rec.kind ? (rec.kind as 'select' | 'confirm' | 'input') : undefined,
            options: parseOptionItems(rec.options),
            multiSelect: typeof rec.multi_select === 'boolean' ? rec.multi_select : undefined,
            allowCustom: typeof rec.allow_custom === 'boolean' ? rec.allow_custom : undefined,
            placeholder: rec.placeholder ? String(rec.placeholder) : undefined,
            defaultValue: rec.default_value ? String(rec.default_value) : undefined,
          });
        }
        return list.length > 0 ? list : undefined;
      };
      pendingUIPrompt = {
        requestId: String(uip.request_id || ''),
        sessionId: ev.session_id,
        runnerId,
        kind: (uip.kind as 'select' | 'confirm' | 'input') || 'select',
        header: uip.header ? String(uip.header) : undefined,
        title: String(uip.title || ''),
        message: uip.message ? String(uip.message) : undefined,
        placeholder: uip.placeholder ? String(uip.placeholder) : undefined,
        options: Array.isArray(uip.options) ? uip.options.map(String) : undefined,
        optionItems: parseOptionItems(uip.option_items),
        multiSelect: typeof uip.multi_select === 'boolean' ? uip.multi_select : undefined,
        allowCustom: typeof uip.allow_custom === 'boolean' ? uip.allow_custom : undefined,
        questions: parseQuestions(uip.questions),
        defaultValue: uip.default_value ? String(uip.default_value) : undefined,
      };
      break;
    }

    case 'ui_prompt_resolved':
      pendingUIPrompt = null;
      break;

    case 'ui_status_updated': {
      const key = String(p.key || '');
      const badge = String(p.badge || '');
      if (key) {
        uiBadges = { ...uiBadges };
        if (badge) uiBadges[key] = badge;
        else delete uiBadges[key];
      }
      break;
    }

    case 'turn_end':
      if (typeof p.error === 'string' && p.error.trim()) {
        lastError = p.error.trim();
      } else if (p.telemetry) {
        lastError = undefined;
      }
      if (p.telemetry) {
        const tel = p.telemetry as TurnTelemetry;
        latestTelemetry = tel;
        const idx = turnTelemetries.findIndex(
          (t) =>
            (tel.raw_trace_blob_hash && t.raw_trace_blob_hash === tel.raw_trace_blob_hash && t.turn_idx === tel.turn_idx) ||
            (tel.recorded_at > 0 && t.recorded_at === tel.recorded_at && t.turn_idx === tel.turn_idx),
        );
        if (idx >= 0) {
          turnTelemetries = turnTelemetries.slice();
          turnTelemetries[idx] = tel;
        } else {
          turnTelemetries = [...turnTelemetries, tel];
        }
      }
      break;

    case 'compaction': {
      const reason = String(p.reason || 'compacted');
      const rawCompaction =
        p.compaction && typeof p.compaction === 'object' ? (p.compaction as Record<string, unknown>) : null;
      const rawStatus = String(rawCompaction?.status || 'completed');
      const status: CompactionCard['status'] =
        rawStatus === 'compacting' || rawStatus === 'skipped' || rawStatus === 'failed'
          ? rawStatus
          : 'completed';
      const beforeMessages =
        typeof rawCompaction?.before_messages === 'number' ? rawCompaction.before_messages : undefined;
      const afterMessages =
        typeof rawCompaction?.after_messages === 'number' ? rawCompaction.after_messages : undefined;
      const beforeTokens =
        typeof rawCompaction?.before_tokens === 'number' ? rawCompaction.before_tokens : undefined;
      const afterTokens =
        typeof rawCompaction?.after_tokens === 'number' ? rawCompaction.after_tokens : undefined;
      const summary = String(rawCompaction?.summary || p.delta || '').trim() || undefined;
      const errMsg = String(rawCompaction?.error || p.error || '').trim() || undefined;

      // Silent background microcompaction updates telemetry/badge without cluttering the chat timeline.
      if (reason !== 'microcompact') {
        const card: CompactionCard = {
          status,
          reason,
          beforeMessages,
          afterMessages,
          beforeTokens,
          afterTokens,
          summary,
          error: errMsg,
        };

        const nextItems = items.slice();
        const tail = nextItems[nextItems.length - 1];
        if (tail && tail.role === 'user' && tail.optimistic && /^\/compact(?:\s|$)/i.test(tail.text.trim())) {
          nextItems.pop();
        }
        const afterStripTail = nextItems[nextItems.length - 1];
        if (
          afterStripTail &&
          afterStripTail.role === 'compaction' &&
          afterStripTail.compaction?.status === 'compacting'
        ) {
          nextItems[nextItems.length - 1] = {
            ...afterStripTail,
            seq: ev.seq > 0 ? ev.seq : afterStripTail.seq,
            compaction: card,
            streaming: status === 'compacting',
            optimistic: false,
          };
        } else {
          nextItems.push({
            id: `cmp_${ev.seq || Date.now()}`,
            seq: ev.seq > 0 ? ev.seq : undefined,
            role: 'compaction',
            text: '',
            thinking: '',
            toolCalls: [],
            compaction: card,
            streaming: status === 'compacting',
          });
        }
        items = nextItems;
      }

      uiBadges = {
        ...uiBadges,
        context: reason === 'manual' ? 'compacted' : 'auto-compacted',
      };

      if (status === 'completed' && typeof afterTokens === 'number' && afterTokens > 0) {
        latestTelemetry = mergeCompactedTelemetry(latestTelemetry, afterTokens, {
          id: `cmp_${ev.seq || Date.now()}`,
          sessionId: ev.session_id,
          turnIdx: ev.turn_idx,
          runnerId,
          recordedAtSec: Math.floor(eventTimestamp() / 1000),
          inlineMessages: summary,
        });
      }
      break;
    }

    case 'background_tasks_updated': {
      const rawTasks = Array.isArray(p.tasks) ? p.tasks : [];
      const nextTasks: BackgroundTaskItem[] = [];
      for (const raw of rawTasks) {
        if (!raw || typeof raw !== 'object') continue;
        const rec = raw as Record<string, unknown>;
        const taskId = String(rec.task_id || '').trim();
        const status = String(rec.status || 'running').trim();
        if (!taskId || status !== 'running') continue;
        nextTasks.push({
          task_id: taskId,
          kind: String(rec.kind || 'local_bash'),
          title: String(rec.title || taskId),
          status,
          started_at:
            typeof rec.started_at === 'number' && Number.isFinite(rec.started_at) ? rec.started_at : Date.now(),
          exit_code: typeof rec.exit_code === 'number' ? rec.exit_code : undefined,
          stopped_by: rec.stopped_by ? String(rec.stopped_by) : undefined,
        });
      }
      backgroundTasks = nextTasks;
      break;
    }
  }

  return {
    lastSeq: ev.seq > 0 ? Math.max(prev.lastSeq, ev.seq) : prev.lastSeq,
    running,
    runStartedAt,
    items,
    latestTelemetry,
    turnTelemetries,
    uiBadges,
    pendingApproval,
    pendingUIPrompt,
    todos,
    goals,
    backgroundTasks,
    lastError,
  };
}
