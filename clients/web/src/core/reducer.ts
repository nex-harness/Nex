import { ChatTurnItem, GoalEntry, PendingApproval, PendingUIPrompt, TodoItem, TurnTelemetry } from './types';

export interface SessionStreamState {
  lastSeq: number;
  running: boolean;
  items: ChatTurnItem[];
  latestTelemetry: TurnTelemetry | null;
  turnTelemetries: TurnTelemetry[];
  uiBadges: Record<string, string>;
  pendingApproval: PendingApproval | null;
  pendingUIPrompt: PendingUIPrompt | null;
  todos: TodoItem[];
  goals: GoalEntry[];
}

export function emptyStreamState(): SessionStreamState {
  return {
    lastSeq: 0,
    running: false,
    items: [],
    latestTelemetry: null,
    turnTelemetries: [],
    uiBadges: {},
    pendingApproval: null,
    pendingUIPrompt: null,
    todos: [],
    goals: [],
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

export interface RawSessionEventRow {
  session_id: string;
  seq: number;
  turn_idx: number;
  event_type: string;
  payload: Record<string, unknown>;
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
  let latestTelemetry = prev.latestTelemetry;
  let turnTelemetries = prev.turnTelemetries || [];
  let uiBadges = prev.uiBadges;
  let pendingApproval = prev.pendingApproval;
  let pendingUIPrompt = prev.pendingUIPrompt;
  let todos = prev.todos;
  let goals = prev.goals;

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

  switch (ev.event_type) {
    case 'agent_start':
      running = true;
      break;

    case 'agent_end': {
      running = false;
      const lastIdx = items.length - 1;
      if (lastIdx >= 0 && items[lastIdx].streaming) {
        items = items.slice();
        items[lastIdx] = { ...items[lastIdx], streaming: false };
      }
      if (goals.some((g) => g.status === 'running')) {
        const completedAt = Date.now();
        goals = goals.map((g) => (g.status === 'running' ? { ...g, status: 'completed', completedAt } : g));
      }
      break;
    }

    case 'message_start':
      cloneOrAppendTailAssistant();
      break;

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
        const images = (msg.content || [])
          .filter((b) => b.type === 'image')
          .map((b) => ({
            mime_type: String(b.mime_type || 'image/png'),
            data_b64: b.data_b64 ? String(b.data_b64) : undefined,
            image_url: b.image_url ? String(b.image_url) : undefined,
          }));
        if (text || images.length > 0) {
          items = [
            ...items,
            {
              id: `usr_${ev.seq || items.length}`,
              seq: ev.seq || undefined,
              role: 'user',
              text,
              thinking: '',
              images: images.length > 0 ? images : undefined,
              toolCalls: [],
            },
          ];
          const goalTitle = parseGoalTitle(text);
          if (goalTitle) {
            const lastGoal = goals[goals.length - 1];
            if (!lastGoal || lastGoal.title !== goalTitle) {
              if (lastGoal && lastGoal.status === 'running') {
                goals = goals.slice();
                goals[goals.length - 1] = { ...lastGoal, status: 'completed', completedAt: Date.now() };
              }
              const counts = countTodos(todos);
              goals = [
                ...goals,
                {
                  id: `goal_${ev.seq || goals.length + 1}`,
                  index: goals.length + 1,
                  title: goalTitle,
                  status: 'running',
                  startedAt: Date.now(),
                  completedTodos: counts.completed,
                  totalTodos: counts.total,
                },
              ];
            }
          }
        }
      } else if (msg.role === 'assistant') {
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
        diff?: string;
        additions?: number;
        deletions?: number;
        metadata?: Record<string, unknown>;
      };
      const endedToolName = String(p.tool_name || '') || findToolName(callId);
      updateToolCallFromTail(callId, (tc) => ({
        ...tc,
        status: tr.is_error ? 'error' : 'completed',
        finalOutput: tr.output || tc.streamOutput,
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
      pendingUIPrompt = {
        requestId: String(uip.request_id || ''),
        sessionId: ev.session_id,
        runnerId,
        kind: (uip.kind as 'select' | 'confirm' | 'input') || 'select',
        title: String(uip.title || ''),
        message: uip.message ? String(uip.message) : undefined,
        placeholder: uip.placeholder ? String(uip.placeholder) : undefined,
        options: Array.isArray(uip.options) ? uip.options.map(String) : undefined,
        defaultValue: uip.default_value ? String(uip.default_value) : undefined,
      };
      break;
    }

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
      uiBadges = {
        ...uiBadges,
        context: reason === 'manual' ? 'compacted' : 'auto-compacted',
      };
      break;
    }
  }

  return {
    lastSeq: ev.seq > 0 ? Math.max(prev.lastSeq, ev.seq) : prev.lastSeq,
    running,
    items,
    latestTelemetry,
    turnTelemetries,
    uiBadges,
    pendingApproval,
    pendingUIPrompt,
    todos,
    goals,
  };
}
