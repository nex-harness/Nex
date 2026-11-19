import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { App } from '../App';
import { decodePTYBinaryFrame, encodePTYBinaryFrame, PTY_BINARY_STDOUT } from '../core/protocol';
import { applySessionEvent, emptyStreamState } from '../core/reducer';

describe('Nex-v3 ZCode-Aligned Layout, Blocks, i18n & On-Demand Side Pane', () => {
  it('encodes and decodes binary PTY frames matching Go protocol', () => {
    const ptyId = 'b9777673-535b-4d67-a726-b390c39ed46d';
    const frame = encodePTYBinaryFrame(PTY_BINARY_STDOUT, ptyId, 'hello ghostty');
    const decoded = decodePTYBinaryFrame(frame);
    expect(decoded).not.toBeNull();
    expect(decoded?.op).toBe(PTY_BINARY_STDOUT);
    expect(decoded?.ptyId).toBe(ptyId);
    expect(new TextDecoder().decode(decoded!.data)).toBe('hello ghostty');
  });

  it('reduces streaming deltas, tool execution, approval, and 6-category telemetry', () => {
    let st = emptyStreamState();
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 1,
      turn_idx: 1,
      event_type: 'agent_start',
      payload: {},
    });
    expect(st.running).toBe(true);

    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 2,
      turn_idx: 1,
      event_type: 'message_update',
      payload: { delta_kind: 'text', delta: '## Heading\n|A|B|\n|---|---|\n|1|2|' },
    });
    expect(st.items[0].text).toContain('## Heading');

    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 3,
      turn_idx: 1,
      event_type: 'turn_end',
      payload: {
        telemetry: {
          id: 'tel_1',
          session_id: 's1',
          turn_idx: 1,
          runner_id: 'run_1',
          provider_id: 'new-provider',
          model_id: 'group/gemini-4-carbon',
          trigger_source: 'chat',
          context_window: 1000000,
          total_context_tokens: 67600,
          window_utilization_pct: 6.8,
          input_tokens: 7600,
          output_tokens: 200,
          reasoning_tokens: 0,
          cache_read_tokens: 60000,
          cache_write_tokens: 0,
          cache_hit_rate_pct: 88.8,
          estimated_cost_usd: 0.025,
          cache_saved_usd: 0.15,
          breakdown: [
            { category: 'messages', bytes: 1000, tokens: 35600, pct: 52.7 },
            { category: 'system_tools', bytes: 800, tokens: 26600, pct: 39.4 },
            { category: 'system_prompt', bytes: 200, tokens: 2700, pct: 4.0, blob_hash: 'abc123' },
            { category: 'mcp_tools', bytes: 90, tokens: 1200, pct: 1.8 },
            { category: 'skills', bytes: 90, tokens: 1200, pct: 1.8 },
            { category: 'meta_context', bytes: 20, tokens: 300, pct: 0.3 },
          ],
          system_prompt_blob_hash: 'abc123',
          ttft_ms: 120,
          duration_ms: 450,
          recorded_at: 1763452800,
        },
      },
    });
    expect(st.latestTelemetry?.window_utilization_pct).toBe(6.8);
    expect(st.latestTelemetry?.breakdown).toHaveLength(6);
  });

  it('starts with Side Pane closed by default, opens New Tab launcher on demand, and navigates ZCode Settings', () => {
    render(<App />);
    expect(screen.getByTestId('project-sidebar')).toBeTruthy();

    // Right Side Pane is NOT persistent by default!
    expect(screen.queryByTestId('workbench-plus-btn')).toBeNull();

    // Click header Side Pane toggle -> reveals New Tab launcher & moves close button into Side Pane tab bar
    fireEvent.click(screen.getByTestId('side-pane-toggle'));
    expect(screen.getByTestId('workbench-plus-btn')).toBeTruthy();
    expect(screen.queryByTestId('side-pane-toggle')).toBeNull();

    // Close Side Pane again via its tab-strip close button
    fireEvent.click(screen.getByTestId('side-pane-close'));
    expect(screen.queryByTestId('workbench-plus-btn')).toBeNull();

    // Open localized Context Windows popover (`上下文容量`)
    fireEvent.click(screen.getByTestId('context-ring-btn'));
    expect(screen.getByTestId('context-popover')).toBeTruthy();
    expect(screen.getByText('上下文容量')).toBeTruthy();

    // Navigate to full-page Settings Center and switch ZCode-named sections
    fireEvent.click(screen.getByTestId('sidebar-settings-btn'));
    expect(screen.getByTestId('settings-page')).toBeTruthy();

    fireEvent.click(screen.getByTestId('settings-nav-appearance'));
    expect(screen.getAllByText('外观').length).toBeGreaterThan(0);

    fireEvent.click(screen.getByTestId('settings-nav-usage'));
    expect(screen.getAllByText('使用统计').length).toBeGreaterThan(0);

    fireEvent.click(screen.getByTestId('settings-nav-plugins'));
    expect(screen.getAllByText('插件').length).toBeGreaterThan(0);

    // Return back to Workspace
    fireEvent.click(screen.getByTestId('settings-back-btn'));
    expect(screen.getByTestId('chat-stream')).toBeTruthy();
  });

  it('preserves frozen history item object references across high-frequency streaming deltas (ZCode O(tail) rule)', () => {
    let st = emptyStreamState();
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 1,
      turn_idx: 1,
      event_type: 'message_end',
      payload: { message: { role: 'user', content: [{ type: 'text', text: 'hello' }] } },
    });
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 2,
      turn_idx: 1,
      event_type: 'message_end',
      payload: { message: { role: 'assistant', content: [{ type: 'text', text: 'first reply' }] } },
    });
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 3,
      turn_idx: 2,
      event_type: 'message_end',
      payload: { message: { role: 'user', content: [{ type: 'text', text: '再执行一遍' }] } },
    });

    const frozenTurn0 = st.items[0];
    const frozenTurn1 = st.items[1];
    const frozenTurn2 = st.items[2];

    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 4,
      turn_idx: 2,
      event_type: 'agent_start',
      payload: {},
    });

    for (let i = 0; i < 100; i++) {
      st = applySessionEvent(st, 'run_1', {
        session_id: 's1',
        seq: 0, // transient live delta
        turn_idx: 2,
        event_type: 'message_update',
        payload: { delta_kind: 'text', delta: `chunk_${i} ` },
      });
    }

    // All historical turns MUST retain identical object references so React.memo skips them in O(1)!
    expect(st.items[0]).toBe(frozenTurn0);
    expect(st.items[1]).toBe(frozenTurn1);
    expect(st.items[2]).toBe(frozenTurn2);
    expect(st.items[3].text).toContain('chunk_99');
  });

  it('populates state.todos strictly from todo_write tool calls (and ignores read/bash noise)', () => {
    let st = emptyStreamState();

    // Non-todo tool activity must NEVER leak into Progress.
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 1,
      turn_idx: 1,
      event_type: 'tool_execution_end',
      payload: {
        tool_call_id: 'call_read',
        tool_name: 'read',
        tool_result: { output: 'a/b/c.ts', is_error: false },
      },
    });
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 2,
      turn_idx: 1,
      event_type: 'tool_execution_end',
      payload: {
        tool_call_id: 'call_bash',
        tool_name: 'bash',
        tool_result: { output: 'ok', is_error: false },
      },
    });
    expect(st.todos).toHaveLength(0);

    // todo_write replaces the whole list from arguments.todos.
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 3,
      turn_idx: 1,
      event_type: 'tool_execution_start',
      payload: {
        tool_call_id: 'call_todo_1',
        tool_name: 'todo_write',
        tool_args: {
          todos: [
            { content: 'Explore layout', status: 'completed', priority: 'high' },
            { content: 'Implement panel', status: 'in_progress', priority: 'medium' },
            { content: 'Run tests', status: 'pending', priority: 'low' },
          ],
        },
      },
    });
    expect(st.todos).toHaveLength(3);
    expect(st.todos[1]).toEqual({ content: 'Implement panel', status: 'in_progress', priority: 'medium' });

    // CamelCase TodoWrite arrives through message_end tool_call blocks.
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 4,
      turn_idx: 1,
      event_type: 'message_end',
      payload: {
        message: {
          role: 'assistant',
          content: [
            {
              type: 'tool_call',
              tool_call_id: 'call_todo_2',
              tool_name: 'TodoWrite',
              arguments: {
                todos: [
                  { content: 'Explore layout', status: 'completed' },
                  { content: 'Implement panel', status: 'completed' },
                  { content: 'Run tests', status: 'completed' },
                ],
              },
            },
          ],
        },
      },
    });
    expect(st.todos.filter((t) => t.status === 'completed')).toHaveLength(3);

    // tool_result.metadata.todos wins on tool_execution_end.
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 5,
      turn_idx: 1,
      event_type: 'tool_execution_end',
      payload: {
        tool_call_id: 'call_todo_2',
        tool_name: 'TodoWrite',
        tool_result: {
          output: 'Updated todo list',
          metadata: { todos: [{ content: 'Only one left', status: 'pending' }] },
        },
      },
    });
    expect(st.todos).toHaveLength(1);
    expect(st.todos[0].content).toBe('Only one left');
  });

  it('populates state.goals from /goal user messages and completes them on agent_end', () => {
    let st = emptyStreamState();

    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 1,
      turn_idx: 1,
      event_type: 'message_end',
      payload: { message: { role: 'user', content: [{ type: 'text', text: '普通消息，不应产生 Goal' }] } },
    });
    expect(st.goals).toHaveLength(0);

    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 2,
      turn_idx: 1,
      event_type: 'message_end',
      payload: { message: { role: 'user', content: [{ type: 'text', text: '/goal 完成 SettingsPage 对齐' }] } },
    });
    expect(st.goals).toHaveLength(1);
    expect(st.goals[0].index).toBe(1);
    expect(st.goals[0].title).toBe('完成 SettingsPage 对齐');
    expect(st.goals[0].status).toBe('running');
    expect(st.goals[0].startedAt).toBeGreaterThan(0);

    // todo_write progress syncs onto the active goal iteration.
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 3,
      turn_idx: 1,
      event_type: 'tool_execution_end',
      payload: {
        tool_call_id: 'call_todo',
        tool_name: 'todo_write',
        tool_result: {
          output: 'ok',
          metadata: {
            todos: [
              { content: 'a', status: 'completed' },
              { content: 'b', status: 'pending' },
            ],
          },
        },
      },
    });
    expect(st.goals[0].completedTodos).toBe(1);
    expect(st.goals[0].totalTodos).toBe(2);

    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 4,
      turn_idx: 1,
      event_type: 'agent_end',
      payload: {},
    });
    expect(st.goals[0].status).toBe('completed');
    expect(st.goals[0].completedAt).toBeGreaterThan(0);

    // A second objective appends iteration 2 wrapped in <untrusted_objective>.
    st = applySessionEvent(st, 'run_1', {
      session_id: 's1',
      seq: 5,
      turn_idx: 2,
      event_type: 'message_end',
      payload: {
        message: {
          role: 'user',
          content: [{ type: 'text', text: '<untrusted_objective>Iteration 4</untrusted_objective>' }],
        },
      },
    });
    expect(st.goals).toHaveLength(2);
    expect(st.goals[1].index).toBe(2);
    expect(st.goals[1].title).toBe('Iteration 4');
    expect(st.goals[1].status).toBe('running');
  });
});
