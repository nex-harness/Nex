import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { App } from '../App';
import { decodePTYBinaryFrame, encodePTYBinaryFrame, PTY_BINARY_STDOUT } from '../core/protocol';
import { applySessionEvent, emptyStreamState } from '../core/reducer';

describe('Nex-v3 Web Client Core & Workbench UI', () => {
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
      payload: { delta_kind: 'text', delta: 'Hello Nex!' },
    });
    expect(st.items[0].text).toBe('Hello Nex!');

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

  it('renders Workbench layout, Context windows popover, and + tab menu', () => {
    render(<App />);
    const ringBtn = screen.getByTestId('context-ring-btn');
    fireEvent.click(ringBtn);
    expect(screen.getByTestId('context-popover')).toBeTruthy();
    expect(screen.getByText('Show system prompt')).toBeTruthy();

    const plusBtn = screen.getByTestId('workbench-plus-btn');
    fireEvent.click(plusBtn);
    expect(screen.getByText('Side conversation')).toBeTruthy();
    expect(screen.getByText('Review')).toBeTruthy();
  });
});
