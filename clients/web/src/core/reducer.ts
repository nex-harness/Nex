import { ChatTurnItem, PendingApproval, PendingUIPrompt, TurnTelemetry } from './types';

export interface SessionStreamState {
  lastSeq: number;
  running: boolean;
  items: ChatTurnItem[];
  latestTelemetry: TurnTelemetry | null;
  uiBadges: Record<string, string>;
  pendingApproval: PendingApproval | null;
  pendingUIPrompt: PendingUIPrompt | null;
}

export function emptyStreamState(): SessionStreamState {
  return {
    lastSeq: 0,
    running: false,
    items: [],
    latestTelemetry: null,
    uiBadges: {},
    pendingApproval: null,
    pendingUIPrompt: null,
  };
}

export interface RawSessionEventRow {
  session_id: string;
  seq: number;
  turn_idx: number;
  event_type: string;
  payload: Record<string, unknown>;
}

export function applySessionEvent(
  prev: SessionStreamState,
  runnerId: string,
  ev: RawSessionEventRow,
): SessionStreamState {
  if (ev.seq > 0 && ev.seq <= prev.lastSeq) {
    return prev;
  }
  const next: SessionStreamState = {
    ...prev,
    lastSeq: Math.max(prev.lastSeq, ev.seq || 0),
    items: [...prev.items],
    uiBadges: { ...prev.uiBadges },
  };

  const p = ev.payload || {};

  const ensureStreamingAssistant = (): ChatTurnItem => {
    const last = next.items[next.items.length - 1];
    if (last && last.role === 'assistant' && last.streaming) {
      const cloned: ChatTurnItem = { ...last, toolCalls: [...last.toolCalls] };
      next.items[next.items.length - 1] = cloned;
      return cloned;
    }
    const created: ChatTurnItem = {
      id: `ast_${ev.seq}`,
      role: 'assistant',
      text: '',
      thinking: '',
      toolCalls: [],
      streaming: true,
    };
    next.items.push(created);
    return created;
  };

  switch (ev.event_type) {
    case 'agent_start':
      next.running = true;
      break;

    case 'agent_end':
      next.running = false;
      next.items = next.items.map((it) => (it.streaming ? { ...it, streaming: false } : it));
      break;

    case 'message_start':
      ensureStreamingAssistant();
      break;

    case 'message_update': {
      const target = ensureStreamingAssistant();
      const kind = String(p.delta_kind || '');
      const delta = String(p.delta || '');
      if (kind === 'text') target.text += delta;
      else if (kind === 'thinking') target.thinking += delta;
      break;
    }

    case 'message_end': {
      const msg = p.message as { role?: string; content?: Array<Record<string, unknown>> } | undefined;
      if (!msg) break;
      if (msg.role === 'user') {
        const text = (msg.content || [])
          .filter((b) => b.type === 'text')
          .map((b) => String(b.text || ''))
          .join('');
        if (text) {
          next.items.push({ id: `usr_${ev.seq}`, role: 'user', text, thinking: '', toolCalls: [] });
        }
      } else if (msg.role === 'assistant') {
        const target = ensureStreamingAssistant();
        const blocks = msg.content || [];
        const fullText = blocks.filter((b) => b.type === 'text').map((b) => String(b.text || '')).join('');
        const fullThink = blocks.filter((b) => b.type === 'thinking').map((b) => String(b.thinking || '')).join('');
        if (fullText) target.text = fullText;
        if (fullThink) target.thinking = fullThink;
        for (const b of blocks) {
          if (b.type === 'tool_call') {
            const callId = String(b.tool_call_id || '');
            if (!target.toolCalls.some((tc) => tc.id === callId)) {
              target.toolCalls.push({
                id: callId,
                name: String(b.tool_name || ''),
                argsText: typeof b.arguments === 'string' ? b.arguments : JSON.stringify(b.arguments || {}),
                status: 'running',
                streamOutput: '',
              });
            }
          }
        }
        target.streaming = false;
      }
      break;
    }

    case 'tool_execution_start': {
      const callId = String(p.tool_call_id || '');
      next.items = next.items.map((it) => ({
        ...it,
        toolCalls: it.toolCalls.map((tc) => (tc.id === callId ? { ...tc, status: 'running' } : tc)),
      }));
      break;
    }

    case 'tool_execution_update': {
      const callId = String(p.tool_call_id || '');
      const delta = String(p.delta || '');
      next.items = next.items.map((it) => ({
        ...it,
        toolCalls: it.toolCalls.map((tc) =>
          tc.id === callId ? { ...tc, streamOutput: tc.streamOutput + delta } : tc,
        ),
      }));
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
      };
      next.items = next.items.map((it) => ({
        ...it,
        toolCalls: it.toolCalls.map((tc) =>
          tc.id === callId
            ? {
                ...tc,
                status: tr.is_error ? 'error' : 'completed',
                finalOutput: tr.output || tc.streamOutput,
                diff: tr.diff,
                additions: tr.additions,
                deletions: tr.deletions,
              }
            : tc,
        ),
      }));
      break;
    }

    case 'approval_requested':
      next.pendingApproval = {
        approvalId: String(p.approval_id || ''),
        sessionId: ev.session_id,
        runnerId,
        toolCallId: String(p.tool_call_id || ''),
        toolName: String(p.tool_name || ''),
        argsJson: typeof p.tool_args === 'string' ? p.tool_args : JSON.stringify(p.tool_args || {}, null, 2),
      };
      break;

    case 'approval_resolved':
      next.pendingApproval = null;
      break;

    case 'ui_prompt_requested': {
      const uip = (p.ui_prompt || {}) as Record<string, unknown>;
      next.pendingUIPrompt = {
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
        if (badge) next.uiBadges[key] = badge;
        else delete next.uiBadges[key];
      }
      break;
    }

    case 'turn_end':
      if (p.telemetry) {
        next.latestTelemetry = p.telemetry as TurnTelemetry;
      }
      break;
  }

  return next;
}
