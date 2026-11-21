import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { App } from '../App';
import { MarkdownRenderer } from '../features/MarkdownRenderer';
import { decodePTYBinaryFrame, encodePTYBinaryFrame, PTY_BINARY_STDOUT } from '../core/protocol';
import { applySessionEvent, emptyStreamState } from '../core/reducer';
import { persistActiveSessionId, readPersistedSessionId, useNexStore } from '../core/store';
import { wsClient } from '../core/wsClient';
import { parsePastedSSHTarget } from '../features/SettingsRunners';

let originalBootstrap: (() => Promise<void>) | null = null;

beforeAll(() => {
  originalBootstrap = useNexStore.getState().bootstrap;
});

beforeEach(() => {
  useNexStore.setState({ bootstrap: vi.fn().mockResolvedValue(undefined) });
});

afterEach(() => {
  cleanup();
});

afterAll(() => {
  if (originalBootstrap) {
    useNexStore.setState({ bootstrap: originalBootstrap });
  }
});

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
    useNexStore.setState({ locale: 'zh-CN' });
    render(<App />);
    expect(screen.getByTestId('project-sidebar')).toBeTruthy();

    // Right Side Pane is NOT persistent by default!
    expect(screen.queryByTestId('workbench-plus-btn')).toBeNull();

    // Click header Side Pane toggle -> reveals New Tab launcher & moves close button into Side Pane tab bar
    fireEvent.click(screen.getByTestId('side-pane-toggle'));
    expect(screen.getByTestId('workbench-plus-btn')).toBeTruthy();
    expect(screen.queryByTestId('side-pane-toggle')).toBeNull();

    // Close Side Pane again via its tab-strip close button (smooth ZCode animated collapse keeps container mounted with data-state="closed")
    fireEvent.click(screen.getByTestId('side-pane-close'));
    expect(screen.getByTestId('side-pane-container').getAttribute('data-state')).toBe('closed');
    expect(screen.getByTestId('side-pane-toggle')).toBeTruthy();

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

  it('selects provider via submenu without jumping to Settings, filters models, and only opens Settings via footer', () => {
    useNexStore.setState({
      mainView: 'workspace',
      activeSessionId: '',
      locale: 'zh-CN',
      providers: [
        { id: 'anthropic', name: 'Anthropic', driver: 'anthropic', base_url: 'https://api.anthropic.com', api_key: '', proxy_mode: 'direct', enabled: true },
        { id: 'openai', name: 'OpenAI', driver: 'openai_responses', base_url: 'https://api.openai.com/v1', api_key: '', proxy_mode: 'direct', enabled: true },
      ],
      models: [
        {
          id: 'claude-sonnet-4-5',
          provider_id: 'anthropic',
          display_name: 'Claude Sonnet 4.5',
          source: 'models.dev',
          context_window: 200000,
          max_output_tokens: 16384,
          supports_vision: true,
          supports_pdf: true,
          supports_tools: true,
          supports_reasoning: true,
          supports_caching: true,
          reasoning_levels: ['off', 'low', 'medium', 'high', 'max'],
          pricing: { input_per_1m: 3, output_per_1m: 15, cache_read_per_1m: 0.3, cache_write_per_1m: 3.75 },
        },
        {
          id: 'gpt-5.2',
          provider_id: 'openai',
          display_name: 'GPT-5.2',
          source: 'models.dev',
          context_window: 200000,
          max_output_tokens: 16384,
          supports_vision: true,
          supports_pdf: false,
          supports_tools: true,
          supports_reasoning: true,
          supports_caching: true,
          reasoning_levels: ['off', 'low', 'medium', 'high', 'xhigh'],
          pricing: { input_per_1m: 2, output_per_1m: 8, cache_read_per_1m: 0.2, cache_write_per_1m: 0 },
        },
      ],
      selectedProviderId: 'anthropic',
      selectedModelId: 'claude-sonnet-4-5',
      hiddenModelKeys: {},
      thinkingEffort: 'medium',
    });

    render(<App />);
    fireEvent.click(screen.getByTestId('model-effort-btn'));

    // Initially displays Anthropic's model only
    expect(screen.getByTestId('model-option-claude-sonnet-4-5')).toBeTruthy();
    expect(screen.queryByTestId('model-option-gpt-5.2')).toBeNull();

    // Clicking provider header MUST open provider submenu and NOT jump to Settings!
    fireEvent.click(screen.getByTestId('composer-provider-trigger'));
    expect(useNexStore.getState().mainView).toBe('workspace');
    expect(screen.getByTestId('composer-provider-submenu')).toBeTruthy();

    // Clicking OpenAI selects OpenAI provider and updates models list!
    fireEvent.click(screen.getAllByTestId('provider-option-openai')[0]);
    expect(useNexStore.getState().mainView).toBe('workspace');
    expect(useNexStore.getState().selectedProviderId).toBe('openai');
    expect(useNexStore.getState().selectedModelId).toBe('gpt-5.2');
    expect(screen.getByTestId('model-option-gpt-5.2')).toBeTruthy();

    // Only clicking Manage Providers footer jumps to Settings -> Providers
    fireEvent.click(screen.getByTestId('composer-provider-trigger'));
    fireEvent.click(screen.getAllByTestId('manage-providers-btn')[0]);
    expect(useNexStore.getState().mainView).toBe('settings');
    expect(useNexStore.getState().settingsSection).toBe('providers');
  });

  it('orders Effort ascending (off -> low -> medium -> high -> max), renders max bubbles, and cycles on Ctrl+T', () => {
    useNexStore.setState({
      mainView: 'workspace',
      activeSessionId: '',
      locale: 'zh-CN',
      providers: [
        { id: 'anthropic', name: 'Anthropic', driver: 'anthropic', base_url: 'https://api.anthropic.com', api_key: '', proxy_mode: 'direct', enabled: true },
      ],
      models: [
        {
          id: 'claude-opus-4-5',
          provider_id: 'anthropic',
          display_name: 'Claude Opus 4.5',
          source: 'models.dev',
          context_window: 200000,
          max_output_tokens: 64000,
          supports_vision: true,
          supports_pdf: true,
          supports_tools: true,
          supports_reasoning: true,
          supports_caching: true,
          reasoning_levels: ['off', 'low', 'medium', 'high', 'max'],
          pricing: { input_per_1m: 5, output_per_1m: 25, cache_read_per_1m: 0.5, cache_write_per_1m: 6.25 },
        },
      ],
      selectedProviderId: 'anthropic',
      selectedModelId: 'claude-opus-4-5',
      thinkingEffort: 'medium',
    });

    render(<App />);
    fireEvent.click(screen.getByTestId('model-effort-btn'));

    const slider = screen.getByTestId('composer-thought-slider') as HTMLInputElement;
    // Index 0=off, 1=low, 2=medium, 3=high, 4=max
    expect(slider.min).toBe('0');
    expect(slider.max).toBe('4');
    expect(slider.value).toBe('2');

    // Dragging to far right (max) MUST set 'max' (NOT 'off' / 关闭!) and display animated bubbles!
    fireEvent.change(slider, { target: { value: '4' } });
    expect(useNexStore.getState().thinkingEffort).toBe('max');
    expect(screen.getByTestId('composer-thought-bubbles')).toBeTruthy();

    // Dragging to far left (0) sets '' (off / 关闭) and hides bubbles
    fireEvent.change(slider, { target: { value: '0' } });
    expect(useNexStore.getState().thinkingEffort).toBe('');
    expect(screen.queryByTestId('composer-thought-bubbles')).toBeNull();

    // Pressing Ctrl+T cycles effort from off -> low
    fireEvent.keyDown(window, { key: 't', ctrlKey: true });
    expect(useNexStore.getState().thinkingEffort).toBe('low');
  });

  it('opens DirectoryBrowserModal when choosing a workspace folder in draft mode (no manual path input)', async () => {
    const browseMock = vi.fn().mockResolvedValue({
      current_path: '/Users/dev/Projects',
      parent_path: '/Users/dev',
      home_path: '/Users/dev',
      entries: [
        { name: 'Nex-v3', path: '/Users/dev/Projects/Nex-v3' },
        { name: 'Zcode', path: '/Users/dev/Projects/Zcode' },
      ],
    });

    useNexStore.setState({
      mainView: 'workspace',
      activeSessionId: '',
      locale: 'zh-CN',
      runners: [
        {
          id: 'run_local',
          name: 'MacBook',
          hostname: 'macbook',
          os: 'darwin',
          arch: 'arm64',
          shell: '/bin/zsh',
          version: '0.3.0',
          tags: [],
          status: 'online',
          last_seen_at: Date.now(),
          synced_config_rev: 1,
          workspace_default: '/Users/dev/Projects',
        },
      ],
      selectedRunnerId: 'run_local',
      selectedWorkspacePath: '/Users/dev/Projects',
      browseRunnerDirs: browseMock,
    });

    render(<App />);
    fireEvent.click(screen.getByTestId('project-select-btn'));
    expect(screen.getByTestId('composer-project-menu')).toBeTruthy();
    // Should NOT render raw "/path/to/project" text input
    expect(screen.queryByPlaceholderText('/path/to/project')).toBeNull();

    // Open DirectoryBrowserModal via "选择目录…"
    fireEvent.click(screen.getByTestId('choose-folder-btn'));
    expect(screen.getByTestId('directory-browser-modal')).toBeTruthy();

    await waitFor(() => {
      expect(screen.getByTestId('dir-entry-Zcode')).toBeTruthy();
    });

    // Quick-select Zcode directory
    fireEvent.click(screen.getByTestId('dir-pick-Zcode'));
    expect(useNexStore.getState().selectedWorkspacePath).toBe('/Users/dev/Projects/Zcode');
    expect(screen.queryByTestId('directory-browser-modal')).toBeNull();
  });

  it('opens Side Pane when inspecting a Subagent via openSubagentTab', () => {
    useNexStore.setState({
      mainView: 'workspace',
      sidePaneVisible: false,
      rightTabs: [],
      activeRightTabId: '',
      locale: 'zh-CN',
    });

    render(<App />);
    act(() => {
      useNexStore.getState().openSubagentTab({
        toolCallId: 'call_sub_1',
        title: 'Explore codebase',
        agentName: 'explore',
        status: 'done',
        prompt: 'Inspect architecture',
        resultText: 'Found 3 layers.',
      });
    });

    expect(useNexStore.getState().sidePaneVisible).toBe(true);
    expect(screen.getByTestId('side-pane-container').getAttribute('data-state')).toBe('open');
    expect(screen.getByText('任务提示词')).toBeTruthy();
    expect(screen.getByText('Inspect architecture')).toBeTruthy();
  });

  it('supports / slash command autocomplete, @ file mention autocomplete, and image attachment strip', async () => {
    const origBootstrap = useNexStore.getState().bootstrap;
    try {
      useNexStore.setState({
        bootstrap: vi.fn().mockResolvedValue(undefined),
        authEnabled: false,
        authenticated: true,
        hiddenModelKeys: {},
        mainView: 'workspace',
        activeSessionId: '',
        locale: 'zh-CN',
        selectedRunnerId: 'run_1',
        selectedProviderId: 'p1',
        selectedModelId: 'm1',
        draftProviderId: 'p1',
        draftModelId: 'm1',
        models: [
          {
            id: 'm1',
            provider_id: 'p1',
            display_name: 'Model One',
            source: 'custom',
            context_window: 128000,
            max_output_tokens: 4096,
            supports_vision: true,
            supports_pdf: false,
            supports_tools: true,
            supports_reasoning: false,
            supports_caching: false,
            pricing: { input_per_1m: 0, output_per_1m: 0, cache_read_per_1m: 0, cache_write_per_1m: 0 },
          },
        ],
        selectedWorkspacePath: '/repo',
        composerText: '',
        composerAttachments: [],
        plugins: [
          {
            id: 'pl_1',
            name: 'deploy-helper',
            source_ts: 'export default function(nex){ nex.registerCommand("deploy-check", { description: "Run preflight" }); }',
            scope: 'global',
            target_runners: [],
            enabled: true,
          },
        ],
        searchWorkspaceFiles: vi.fn(async (q: string) =>
          ['pkg/agent/loop.go', 'pkg/ai/openai_chat.go', 'README.md'].filter((f) =>
            f.toLowerCase().includes(q.toLowerCase()),
          ),
        ),
      });

      render(<App />);
      const input = screen.getByTestId('composer-input') as HTMLTextAreaElement;

      // 1. Typing `/` opens slash command autocomplete with built-ins + plugin commands
      fireEvent.change(input, { target: { value: '/', selectionStart: 1 } });
      expect(screen.getByTestId('composer-slash-menu')).toBeTruthy();
      expect(screen.getByTestId('slash-option-goal')).toBeTruthy();
      expect(screen.getByTestId('slash-option-compact')).toBeTruthy();
      expect(screen.getByTestId('slash-option-deploy-check')).toBeTruthy();

      // Selecting `/compact` via mouseDown replaces text cleanly
      fireEvent.mouseDown(screen.getByTestId('slash-option-compact'));
      expect(useNexStore.getState().composerText).toBe('/compact ');

      // 2. Typing `@loop` opens workspace file mention autocomplete and inserts `@pkg/agent/loop.go `
      fireEvent.change(input, { target: { value: 'inspect @loop', selectionStart: 13 } });
      await waitFor(() => {
        expect(screen.getByTestId('composer-mention-menu')).toBeTruthy();
        expect(screen.getByTestId('mention-option-0')).toBeTruthy();
      });
      fireEvent.keyDown(input, { key: 'Enter' });
      expect(useNexStore.getState().composerText).toBe('inspect @pkg/agent/loop.go ');

      // 3. Adding an image attachment renders thumbnail strip and enables send even with empty text
      act(() => {
        useNexStore.getState().setComposerText('');
        useNexStore.getState().addComposerAttachment({
          id: 'img_1',
          name: 'screenshot.png',
          mimeType: 'image/png',
          dataB64: 'iVBORw0KGgo=',
          previewUrl: 'data:image/png;base64,iVBORw0KGgo=',
          size: 2048,
        });
      });
      expect(screen.getByTestId('composer-attachments')).toBeTruthy();
      expect((screen.getByTestId('send-btn') as HTMLButtonElement).disabled).toBe(false);
    } finally {
      useNexStore.setState({ bootstrap: origBootstrap });
    }
  });

  it('exposes Branch & Edit / Fork actions on turns and Stage / Commit controls in Review tab', async () => {
    const forkSpy = vi.fn(async () => 'sess_forked_1');
    const stageSpy = vi.fn(async () => {});
    const commitSpy = vi.fn(async () => ({ ok: true, commit: 'abc1234' }));

    useNexStore.setState({
      mainView: 'workspace',
      activeSessionId: 'sess_active',
      selectedRunnerId: 'run_1',
      sidePaneVisible: true,
      locale: 'zh-CN',
      rightTabs: [{ id: 'tab_rev', kind: 'review', title: '审查', closable: true }],
      activeRightTabId: 'tab_rev',
      gitSummary: {
        is_git_repo: true,
        branch: 'feat/nex-v3',
        changed_files: 1,
        additions: 5,
        deletions: 1,
        files: [
          { rel_path: 'pkg/agent/loop.go', status: 'M', staged: true, unstaged: true, additions: 5, deletions: 1, diff: '+ok' },
        ],
      },
      streams: {
        sess_active: {
          ...emptyStreamState(),
          lastSeq: 2,
          items: [
            { id: 'usr_1', seq: 1, role: 'user', text: 'First question', thinking: '', toolCalls: [] },
            { id: 'ast_2', seq: 2, role: 'assistant', text: 'First answer', thinking: '', toolCalls: [] },
          ],
        },
      },
      forkSession: forkSpy,
      gitStage: stageSpy,
      gitCommit: commitSpy,
      refreshGitSummary: vi.fn(async () => null),
    });

    render(<App />);

    // Branch & Edit before user turn 1
    fireEvent.click(screen.getByTestId('fork-before-1'));
    expect(forkSpy).toHaveBeenCalledWith('sess_active', { beforeSeq: 1, prefillText: 'First question' });

    // Fork after assistant turn 2
    fireEvent.click(screen.getByTestId('fork-after-2'));
    expect(forkSpy).toHaveBeenCalledWith('sess_active', { upToSeq: 2 });

    // Commit staged file in Git Review tab
    const commitInput = screen.getByTestId('git-commit-input') as HTMLInputElement;
    fireEvent.change(commitInput, { target: { value: 'feat: test commit' } });
    fireEvent.click(screen.getByTestId('git-commit-btn'));
    await waitFor(() => {
      expect(commitSpy).toHaveBeenCalledWith('feat: test commit');
    });
  });

  it('handles custom effort level names, canonical clamping across models, and hides slider for non-reasoning models', () => {
    useNexStore.setState({
      mainView: 'workspace',
      activeSessionId: '',
      locale: 'zh-CN',
      providers: [
        { id: 'custom', name: 'CustomGW', driver: 'openai_chat', base_url: 'https://gw.example/v1', api_key: '', proxy_mode: 'direct', enabled: true },
      ],
      models: [
        {
          id: 'my-custom-thinker',
          provider_id: 'custom',
          display_name: 'Custom Thinker',
          source: 'custom',
          context_window: 128000,
          max_output_tokens: 8192,
          supports_vision: false,
          supports_pdf: false,
          supports_tools: true,
          supports_reasoning: true,
          supports_caching: false,
          reasoning_levels: ['off', 'turbo', 'ultra'],
          pricing: { input_per_1m: 0, output_per_1m: 0, cache_read_per_1m: 0, cache_write_per_1m: 0 },
        },
        {
          id: 'gpt-4o-mini',
          provider_id: 'custom',
          display_name: 'GPT-4o Mini',
          source: 'models.dev',
          context_window: 128000,
          max_output_tokens: 4096,
          supports_vision: true,
          supports_pdf: false,
          supports_tools: true,
          supports_reasoning: false,
          supports_caching: false,
          pricing: { input_per_1m: 0.15, output_per_1m: 0.6, cache_read_per_1m: 0, cache_write_per_1m: 0 },
        },
      ],
      selectedProviderId: 'custom',
      selectedModelId: 'my-custom-thinker',
      thinkingEffort: 'max', // from a previous Claude session -> should clamp to highest custom level ("Ultra"), NOT off!
      hiddenModelKeys: {},
    });

    render(<App />);
    fireEvent.click(screen.getByTestId('model-effort-btn'));

    // 1. Custom words are preserved and "max" clamps cleanly to index 2 ("Ultra")
    const slider = screen.getByTestId('composer-thought-slider') as HTMLInputElement;
    expect(slider.max).toBe('2');
    expect(slider.value).toBe('2');
    expect(slider.getAttribute('aria-valuetext')).toBe('Ultra');

    // 2. Switching to a non-reasoning model hides the slider and shows unsupported notice
    fireEvent.click(screen.getByTestId('model-option-gpt-4o-mini'));
    fireEvent.click(screen.getByTestId('model-effort-btn'));
    expect(screen.queryByTestId('composer-thought-slider')).toBeNull();
    expect(screen.getByTestId('composer-thought-unsupported')).toBeTruthy();
  });

  it('closes ContextPopover on outside click/Escape, merges system prompt inspection, and supports # session references', () => {
    useNexStore.setState({
      locale: 'zh-CN',
      activeSessionId: 'sess_active_1',
      sessions: [
        {
          id: 'sess_active_1',
          runner_id: 'run_1',
          depth: 0,
          title: 'Current Task',
          workspace_path: '/tmp/ws',
          agent_profile_slug: 'main',
          model_id: 'group/grok-4.7',
          provider_id: 'openai',
          permission_mode: 'full_access',
          status: 'idle',
          latest_context_tokens: 15400,
          context_window_limit: 500000,
          created_at: 1000,
          updated_at: 2000,
          last_active_at: 2000,
        },
        {
          id: 'sess_weather_ref',
          runner_id: 'run_1',
          depth: 0,
          title: '北京明天天气查询',
          workspace_path: '/tmp/ws',
          agent_profile_slug: 'main',
          model_id: 'group/grok-4.7',
          provider_id: 'openai',
          permission_mode: 'full_access',
          status: 'idle',
          latest_context_tokens: 4000,
          context_window_limit: 500000,
          created_at: 1000,
          updated_at: 1900,
          last_active_at: 1900,
        },
      ],
      streams: {
        sess_active_1: {
          lastSeq: 2,
          running: false,
          items: [
            { id: 'usr_1', seq: 1, role: 'user', text: 'Hi', thinking: '', toolCalls: [] },
            { id: 'ast_2', seq: 2, role: 'assistant', text: 'Hello!', thinking: '', toolCalls: [] },
          ],
          latestTelemetry: {
            id: 'tel_1',
            session_id: 'sess_active_1',
            turn_idx: 1,
            runner_id: 'run_1',
            provider_id: 'openai',
            model_id: 'group/grok-4.7',
            trigger_source: 'chat',
            context_window: 500000,
            total_context_tokens: 15400,
            window_utilization_pct: 3.1,
            input_tokens: 14000,
            output_tokens: 1400,
            reasoning_tokens: 120,
            cache_read_tokens: 1000,
            cache_write_tokens: 0,
            cache_hit_rate_pct: 7.5,
            estimated_cost_usd: 0.0322,
            cache_saved_usd: 0.001,
            breakdown: [
              { category: 'messages', bytes: 1000, tokens: 10000, pct: 69.7, blob_hash: 'hash_msg' },
              { category: 'system_tools', bytes: 500, tokens: 3500, pct: 23.4, blob_hash: 'hash_tools' },
              { category: 'system_prompt', bytes: 200, tokens: 600, pct: 3.7, blob_hash: 'hash_sys' },
            ],
            system_prompt_blob_hash: 'hash_sys',
            raw_trace_blob_hash: 'hash_trace',
            ttft_ms: 400,
            duration_ms: 1200,
            recorded_at: 1700000000,
          },
          turnTelemetries: [],
          uiBadges: {},
          pendingApproval: null,
          pendingUIPrompt: null,
          todos: [],
          goals: [],
        },
      },
      composerText: '',
    });

    render(<App />);

    // 1. Open ContextPopover -> no redundant "查看系统提示词" button; trajectory button exists; clicking outside closes it
    fireEvent.click(screen.getByTestId('context-ring-btn'));
    expect(screen.getByTestId('context-popover')).toBeTruthy();
    expect(screen.queryByText('查看系统提示词')).toBeNull();
    expect(screen.getByTestId('open-trajectory-btn')).toBeTruthy();
    expect(screen.getByTestId('context-cat-system_prompt')).toBeTruthy();

    fireEvent.mouseDown(document.body);
    expect(screen.queryByTestId('context-popover')).toBeNull();

    // Re-open and close with Escape
    fireEvent.click(screen.getByTestId('context-ring-btn'));
    expect(screen.getByTestId('context-popover')).toBeTruthy();
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(screen.queryByTestId('context-popover')).toBeNull();

    // 2. Typing `#` opens session reference autocomplete and inserts `#sess_weather_ref `
    const input = screen.getByTestId('composer-input') as HTMLTextAreaElement;
    fireEvent.change(input, { target: { value: '参考一下 #天气', selectionStart: 8 } });
    expect(screen.getByTestId('composer-session-menu')).toBeTruthy();
    expect(screen.getByTestId('session-ref-option-0').textContent).toContain('北京明天天气查询');

    fireEvent.mouseDown(screen.getByTestId('session-ref-option-0'));
    expect(useNexStore.getState().composerText).toBe('参考一下 #sess_weather_ref ');
    expect(screen.getByTestId('composer-session-refs')).toBeTruthy();
  });

  it('supports turn tick scroll, collapsed-sidebar header padding, right-aligned workbench + menu, and settings syntax highlighting', () => {
    useNexStore.setState({
      locale: 'zh-CN',
      mainView: 'workspace',
      sidebarVisible: false,
      sidePaneVisible: true,
      activeSessionId: 'sess_nav_1',
      sessions: [
        {
          id: 'sess_nav_1',
          runner_id: 'run_1',
          depth: 0,
          title: '查一下明天北京的天气',
          workspace_path: '/tmp/ws',
          agent_profile_slug: 'main',
          model_id: 'group/grok-4.7',
          provider_id: 'openai',
          permission_mode: 'full_access',
          status: 'idle',
          latest_context_tokens: 1000,
          context_window_limit: 500000,
          created_at: 1000,
          updated_at: 2000,
          last_active_at: 2000,
        },
      ],
      streams: {
        sess_nav_1: {
          lastSeq: 2,
          running: false,
          items: [
            { id: 'usr_1', seq: 1, role: 'user', text: 'First turn', thinking: '', toolCalls: [] },
            { id: 'ast_2', seq: 2, role: 'assistant', text: 'Second turn', thinking: '', toolCalls: [] },
          ],
          latestTelemetry: null,
          turnTelemetries: [],
          uiBadges: {},
          pendingApproval: null,
          pendingUIPrompt: null,
          todos: [],
          goals: [],
        },
      },
    });

    const { rerender } = render(<App />);

    // 1. Collapsed sidebar header has left clearance (`pl-[132px]`) so DesktopTopOverlay does not overlap title
    const header = screen.getByTestId('conversation-header');
    expect(header.className).toContain('pl-[132px]');

    // 2. Turn tick rail exists and clicking tick 0 triggers smooth scroll
    const scrollEl = screen.getByTestId('chat-stream');
    const scrollToSpy = vi.fn();
    scrollEl.scrollTo = scrollToSpy;
    fireEvent.click(screen.getByTestId('turn-tick-0'));
    expect(scrollToSpy).toHaveBeenCalledTimes(1);

    // 3. Right workbench '+' menu is anchored to right-0 (never clipped off-screen)
    fireEvent.click(screen.getByTestId('workbench-plus-btn'));
    const plusMenu = screen.getByTestId('workbench-plus-menu');
    expect(plusMenu.className).toContain('right-0');
    expect(plusMenu.className).not.toContain('left-0');

    // 4. Settings Appearance / Plugins / Skills render syntax-highlighted code
    useNexStore.setState({ mainView: 'settings', settingsSection: 'appearance' });
    rerender(<App />);
    expect(document.querySelector('[data-highlighted-lang="ts"]')).toBeTruthy();

    useNexStore.setState({ settingsSection: 'plugins' });
    rerender(<App />);
    expect(screen.getByTestId('plugin-ts-editor')).toBeTruthy();
    expect(document.querySelector('[data-highlighted-lang="ts"]')).toBeTruthy();

    useNexStore.setState({ settingsSection: 'skills' });
    rerender(<App />);
    // Skills page is now a ZCode-style discovery & workspace-visibility manager (no inline skill creation editor)
    expect(screen.getByTestId('skills-manager-section')).toBeTruthy();
    expect(screen.getByTestId('skills-workspace-select')).toBeTruthy();
    expect(screen.queryByTestId('skill-md-editor')).toBeNull();

    // 5. MCP section exposes ZCode/Pi install modal (Presets/Form/JSON), exposure policy, and workspace visibility
    useNexStore.setState({
      settingsSection: 'mcp',
      mcpServers: [
        {
          name: 'github',
          description: 'GitHub repos and PRs',
          host_mode: 'server_hosted',
          transport: 'http',
          command: '',
          args: [],
          env: {},
          url: 'https://api.githubcopilot.com/mcp/',
          exposure: 'deferred',
          scope: 'global',
          target_runners: [],
          idle_ttl_sec: 300,
          enabled: true,
          last_status: 'connected',
          catalog_cache: [
            { namespace: 'github', name: 'search_code', full_name: 'github.search_code', read_only: true },
          ],
        },
      ],
    });
    rerender(<App />);
    expect(screen.getByTestId('mcp-manager-section')).toBeTruthy();
    expect(screen.getByTestId('mcp-row-github')).toBeTruthy();
    expect((screen.getByTestId('mcp-exposure-github') as HTMLSelectElement).value).toBe('deferred');
    fireEvent.click(screen.getByTestId('mcp-install-btn'));
    expect(screen.getByTestId('mcp-install-modal')).toBeTruthy();
    expect(screen.getByTestId('mcp-preset-playwright')).toBeTruthy();
    fireEvent.click(screen.getByTestId('mcp-preset-playwright'));
    expect((screen.getByTestId('mcp-form-name') as HTMLInputElement).value).toBe('playwright');
    fireEvent.click(screen.getByTestId('mcp-tab-json'));
    expect(screen.getByTestId('mcp-json-textarea')).toBeTruthy();
  });

  it('supports clickable file_path:line_number links and follow-up message queueing while running', () => {
    const openFileSpy = vi.fn().mockResolvedValue(undefined);
    useNexStore.setState({
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: 'sess_b1',
      selectedRunnerId: 'run_1',
      selectedProviderId: 'openai',
      selectedModelId: 'group/grok-4.7',
      models: [
        {
          id: 'group/grok-4.7',
          provider_id: 'openai',
          display_name: 'Grok 4.7',
          source: 'custom',
          context_window: 128000,
          max_output_tokens: 4096,
          supports_vision: false,
          supports_pdf: false,
          supports_tools: true,
          supports_reasoning: false,
          supports_caching: false,
          pricing: { input_per_1m: 0, output_per_1m: 0, cache_read_per_1m: 0, cache_write_per_1m: 0 },
        },
      ],
      openFileInPreviewTab: openFileSpy,
      followUpQueues: {},
      sessions: [
        {
          id: 'sess_b1',
          runner_id: 'run_1',
          depth: 0,
          title: 'Batch 1 Check',
          workspace_path: '/tmp/ws',
          agent_profile_slug: 'main',
          model_id: 'group/grok-4.7',
          provider_id: 'openai',
          permission_mode: 'full_access',
          status: 'running',
          latest_context_tokens: 1000,
          context_window_limit: 500000,
          created_at: 1000,
          updated_at: 2000,
          last_active_at: 2000,
        },
      ],
      streams: {
        sess_b1: {
          lastSeq: 2,
          running: true,
          items: [
            { id: 'usr_1', seq: 1, role: 'user', text: 'Check server', thinking: '', toolCalls: [] },
            {
              id: 'ast_2',
              seq: 2,
              role: 'assistant',
              text: 'See `pkg/server/ws.go:162` for the event router.',
              thinking: '',
              toolCalls: [],
            },
          ],
          latestTelemetry: null,
          turnTelemetries: [],
          uiBadges: {},
          pendingApproval: null,
          pendingUIPrompt: null,
          todos: [],
          goals: [],
        },
      },
      composerText: 'Next, run all tests',
    });

    render(<App />);

    // 1. Clickable `file_path:line_number` pill opens preview at line 162
    const fileBtn = document.querySelector('[data-file-ref="pkg/server/ws.go:162"]') as HTMLButtonElement;
    expect(fileBtn).toBeTruthy();
    fireEvent.click(fileBtn);
    expect(openFileSpy).toHaveBeenCalledWith('pkg/server/ws.go', 162);

    // 2. While running, clicking Send queues a follow-up (does not interrupt) and renders the queue strip
    expect(screen.getByTestId('steer-now-btn')).toBeTruthy();
    fireEvent.click(screen.getByTestId('send-btn'));
    expect(useNexStore.getState().followUpQueues['sess_b1']?.length).toBe(1);
    expect(screen.getByTestId('followup-queue')).toBeTruthy();

    // Removing the queued follow-up clears the queue
    fireEvent.click(screen.getByTestId('followup-remove-0'));
    expect(useNexStore.getState().followUpQueues['sess_b1']?.length).toBe(0);
  });

  it('renders Plan Mode approval bar and Memory settings management section', async () => {
    useNexStore.setState({
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: 'sess_plan_1',
      selectedRunnerId: 'run_1',
      permissionMode: 'plan',
      memories: [
        {
          id: 'mem_1',
          scope: 'global',
          category: 'preference',
          title: '纯 Go 静态构建',
          content: '始终使用 CGO_ENABLED=0 构建二进制',
          created_at: 1000,
          updated_at: 2000,
        },
      ],
      sessions: [
        {
          id: 'sess_plan_1',
          runner_id: 'run_1',
          depth: 0,
          title: 'Plan Test',
          workspace_path: '/tmp/ws',
          agent_profile_slug: 'main',
          model_id: 'group/grok-4.7',
          provider_id: 'openai',
          permission_mode: 'plan',
          status: 'idle',
          latest_context_tokens: 1000,
          context_window_limit: 500000,
          created_at: 1000,
          updated_at: 2000,
          last_active_at: 2000,
        },
      ],
      streams: {
        sess_plan_1: {
          lastSeq: 2,
          running: false,
          items: [
            { id: 'usr_1', seq: 1, role: 'user', text: 'Plan refactor', thinking: '', toolCalls: [] },
            { id: 'ast_2', seq: 2, role: 'assistant', text: '## 实施计划\n1. 拆分模块', thinking: '', toolCalls: [] },
          ],
          latestTelemetry: null,
          turnTelemetries: [],
          uiBadges: {},
          pendingApproval: null,
          pendingUIPrompt: null,
          todos: [],
          goals: [],
        },
      },
    });

    const { rerender } = render(<App />);

    // 1. Plan approval bar appears and Approve switches permissionMode to full_access
    expect(screen.getByTestId('plan-approval-bar')).toBeTruthy();
    fireEvent.click(screen.getByTestId('approve-plan-btn'));
    expect(useNexStore.getState().permissionMode).toBe('full_access');

    // 2. Settings -> Memory section renders saved memories, engine settings, and consolidate action
    const consolidateSpy = vi.fn().mockResolvedValue({
      before_count: 2,
      after_count: 1,
      summary: 'Merged duplicate Go build preferences',
      memories: [
        {
          id: 'mem_1',
          scope: 'global',
          category: 'preference',
          title: '纯 Go 静态构建',
          content: '始终使用 CGO_ENABLED=0 构建二进制',
          created_at: 1000,
          updated_at: 2000,
        },
      ],
    });
    useNexStore.setState({
      mainView: 'settings',
      settingsSection: 'memories',
      consolidateMemories: consolidateSpy,
      memories: [
        ...useNexStore.getState().memories,
        {
          id: 'mem_2',
          scope: 'global',
          category: 'preference',
          title: '静态编译',
          content: '禁用 CGO',
          created_at: 1100,
          updated_at: 2100,
        },
      ],
    });
    rerender(<App />);
    expect(screen.getByTestId('memory-title-input')).toBeTruthy();
    expect(screen.getByTestId('memory-auto-extract-toggle')).toBeTruthy();
    expect(screen.getByTestId('memory-model-select')).toBeTruthy();
    expect(screen.getByText('纯 Go 静态构建')).toBeTruthy();
    fireEvent.click(screen.getByTestId('consolidate-memories-btn'));
    await waitFor(() => expect(consolidateSpy).toHaveBeenCalledTimes(1));
  });

  it('supports Cmd+K command palette, Markdown export, and turn-level file rewind', async () => {
    const rewindSpy = vi.fn().mockResolvedValue({
      session_id: 'sess_b3',
      before_seq: 10,
      restored_files: ['/tmp/ws/main.go'],
      deleted_files: [],
    });
    const exportSpy = vi.fn().mockResolvedValue({
      filename: 'Batch-3-Test.md',
      markdown: '# Batch 3 Test',
    });

    useNexStore.setState({
      mainView: 'workspace',
      locale: 'zh-CN',
      activeSessionId: 'sess_b3',
      quickSearchOpen: false,
      rewindSessionFiles: rewindSpy,
      exportSessionMarkdown: exportSpy,
      sessions: [
        {
          id: 'sess_b3',
          runner_id: 'run_1',
          depth: 0,
          title: 'Batch 3 Test',
          workspace_path: '/tmp/ws',
          agent_profile_slug: 'main',
          model_id: 'group/grok-4.7',
          provider_id: 'openai',
          permission_mode: 'full_access',
          status: 'idle',
          latest_context_tokens: 1000,
          context_window_limit: 500000,
          created_at: 1000,
          updated_at: 2000,
          last_active_at: 2000,
        },
      ],
      streams: {
        sess_b3: {
          lastSeq: 12,
          running: false,
          items: [
            { id: 'usr_9', seq: 9, role: 'user', text: 'Update main.go', thinking: '', toolCalls: [] },
            {
              id: 'ast_10',
              startSeq: 10,
              seq: 12,
              role: 'assistant',
              text: 'Updated main.go.',
              thinking: '',
              toolCalls: [
                {
                  id: 'tc_edit_1',
                  name: 'edit',
                  argsText: JSON.stringify({ path: 'main.go', old_text: 'a', new_text: 'b' }),
                  status: 'completed',
                  streamOutput: '',
                  finalOutput: 'edited main.go',
                },
              ],
            },
          ],
          latestTelemetry: null,
          turnTelemetries: [],
          uiBadges: {},
          pendingApproval: null,
          pendingUIPrompt: null,
          todos: [],
          goals: [],
        },
      },
    });

    render(<App />);

    // 1. Cmd+K opens CommandPaletteModal and can trigger Export Markdown
    fireEvent.keyDown(window, { key: 'k', metaKey: true });
    expect(screen.getByTestId('command-palette-modal')).toBeTruthy();
    fireEvent.click(screen.getByTestId('palette-item-cmd-export-md'));
    expect(exportSpy).toHaveBeenCalledWith('sess_b3');
    expect(screen.queryByTestId('command-palette-modal')).toBeNull();

    // 2. Session header menu exposes Export Markdown button
    fireEvent.click(screen.getByTestId('session-menu-btn'));
    expect(screen.getByTestId('export-markdown-btn')).toBeTruthy();
    fireEvent.click(screen.getByTestId('export-markdown-btn'));
    expect(exportSpy).toHaveBeenCalledTimes(2);

    // 3. Two-step file rewind confirmation triggers rewindSessionFiles(sessionId, startSeq)
    const rewindBtn = screen.getByTestId('rewind-turn-10');
    fireEvent.click(rewindBtn);
    expect(rewindBtn.textContent).toContain('确认回滚');
    fireEvent.click(rewindBtn);
    await waitFor(() => expect(rewindSpy).toHaveBeenCalledWith('sess_b3', 10));
    expect(screen.getByTestId('rewind-notice').textContent).toContain('已回滚 1 个文件');
  });

  it('persists active session across reload, enforces AuthGate password lock, and renders SSH server manager', async () => {
    // 1. Session persistence round-trip via URL query + storage
    persistActiveSessionId('sess_reload_keep');
    expect(window.location.search).toContain('session=sess_reload_keep');
    expect(readPersistedSessionId()).toBe('sess_reload_keep');
    persistActiveSessionId('');
    expect(window.location.search).not.toContain('session=');

    // 2. Smart SSH paste parser (ZCode RDUX-27)
    expect(parsePastedSSHTarget('ssh tylinux@192.168.123.42 -p 2222')).toEqual({
      host: '192.168.123.42',
      username: 'tylinux',
      port: '2222',
    });
    expect(parsePastedSSHTarget('root@10.0.0.5:2200')).toEqual({
      host: '10.0.0.5',
      username: 'root',
      port: '2200',
    });

    // 3. AuthGate blocks workspace when authEnabled && !authenticated
    const origBootstrap = useNexStore.getState().bootstrap;
    const loginSpy = vi.fn().mockResolvedValue({ ok: true });
    useNexStore.setState({
      locale: 'zh-CN',
      authChecked: true,
      authEnabled: true,
      authenticated: false,
      bootstrap: vi.fn().mockResolvedValue(undefined),
      loginWithPassword: loginSpy,
    });
    try {
      const { rerender } = render(<App />);
      expect(screen.getByTestId('auth-gate')).toBeTruthy();
      fireEvent.change(screen.getByTestId('auth-password-input'), { target: { value: 'mysecret' } });
      fireEvent.click(screen.getByTestId('auth-submit-btn'));
      await waitFor(() => expect(loginSpy).toHaveBeenCalledWith('mysecret'));

      // 4. General Settings exposes Web Access Password controls
      useNexStore.setState({
        authEnabled: true,
        authenticated: true,
        mainView: 'settings',
        settingsSection: 'general',
      });
      rerender(<App />);
      expect(screen.getByTestId('web-auth-status-badge')).toBeTruthy();
      expect(screen.getByTestId('web-password-new')).toBeTruthy();
      expect(screen.getByTestId('web-logout-btn')).toBeTruthy();

      // 5. Remote Devices (Runners) Settings exposes SSH targets + ZCode-style Add SSH modal (SCP & Remote Install)
      useNexStore.setState({
        settingsSection: 'runners',
        sshTargets: [
          {
            id: 'ssh_rb1',
            name: 'RemoteBuilder',
            ssh_alias: 'remotebuilder',
            host: '192.168.123.42',
            port: 22,
            username: 'tylinux',
            auth_method: 'key',
            install_mode: 'scp',
            last_status: 'idle',
          },
        ],
      });
      rerender(<App />);
      expect(screen.getByTestId('ssh-target-ssh_rb1')).toBeTruthy();
      fireEvent.click(screen.getByTestId('add-ssh-server-btn'));
      const modal = screen.getByTestId('ssh-server-modal');
      expect(modal).toBeTruthy();
      expect((screen.getByTestId('ssh-username-input') as HTMLInputElement).placeholder).toBe('root');
      expect(modal.querySelector('select')).toBeNull();
      fireEvent.click(screen.getByTestId('ssh-advanced-toggle'));
      expect((screen.getByTestId('ssh-workspace-input') as HTMLInputElement).placeholder).toBe('$HOME');
      expect(screen.getByTestId('ssh-import-config-btn')).toBeTruthy();
      fireEvent.click(screen.getByTestId('ssh-mode-remote_install'));
      expect(screen.getByTestId('ssh-download-url-input')).toBeTruthy();
    } finally {
      useNexStore.setState({ bootstrap: origBootstrap, authEnabled: false, authenticated: true });
    }
  });

  it('does not hang or OOM-crash MarkdownRenderer on partial streaming headings (##, #, #tag, #####)', () => {
    const samples = ['##', '#', '###', '#hashtag', '##### Level 5 Heading', 'line 1\n##\nline 2'];
    for (const s of samples) {
      const { unmount } = render(<MarkdownRenderer content={s} streaming />);
      unmount();
    }
    render(<MarkdownRenderer content={'##### Deep Title\n\n#tag-line'} />);
    expect(screen.getByText('Deep Title')).toBeTruthy();
    expect(screen.getByText('#tag-line')).toBeTruthy();
  });

  it('suppresses ConversationStatusPanel on clean git repos and reserves space cleanly across auto/panel/mini modes', () => {
    useNexStore.setState({
      mainView: 'workspace',
      activeSessionId: 'sess_panel_layout',
      locale: 'zh-CN',
      gitSummary: {
        is_git_repo: true,
        branch: 'main',
        changed_files: 0,
        additions: 0,
        deletions: 0,
      },
      sessions: [
        {
          id: 'sess_panel_layout',
          runner_id: 'run_1',
          depth: 0,
          title: 'Panel Layout Test',
          workspace_path: '/tmp/ws',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'full_access',
          status: 'idle',
          latest_context_tokens: 100,
          context_window_limit: 128000,
          created_at: 1000,
          updated_at: 2000,
          last_active_at: 2000,
        },
      ],
      streams: {
        sess_panel_layout: {
          lastSeq: 2,
          running: false,
          items: [
            { id: 'usr_1', seq: 1, role: 'user', text: '整理一下当前的项目内容', thinking: '', toolCalls: [] },
            { id: 'ast_2', seq: 2, role: 'assistant', text: '已整理完成。', thinking: '', toolCalls: [] },
          ],
          latestTelemetry: null,
          turnTelemetries: [],
          uiBadges: {},
          pendingApproval: null,
          pendingUIPrompt: null,
          todos: [],
          goals: [],
        },
      },
    });

    render(<App />);
    // 1. Clean git repo with no active todos/goals/agents hides status panel completely
    expect(screen.queryByTestId('conversation-status-panel')).toBeNull();
    const chatStream = screen.getByTestId('chat-stream');
    expect(chatStream.className).not.toContain('pr-[22rem]');

    // 2. Dirty repo mounts status panel in 'auto' mode and reserves space cleanly
    act(() => {
      useNexStore.setState({
        gitSummary: {
          is_git_repo: true,
          branch: 'main',
          changed_files: 2,
          additions: 12,
          deletions: 3,
        },
      });
    });

    const panel = screen.getByTestId('conversation-status-panel');
    expect(panel.getAttribute('data-display-mode')).toBe('auto');
    expect(chatStream.className).toContain('pt-14');
    expect(chatStream.className).toContain('@min-[1280px]/conversation:!pr-[22rem]');

    // 3. Expanding from mini trigger enters 'panel' mode (reserves space starting at >=960px container width)
    fireEvent.click(screen.getByTestId('status-panel-mini'));
    expect(panel.getAttribute('data-display-mode')).toBe('panel');
    expect(panel.getAttribute('data-state')).toBe('expanded');
    expect(chatStream.className).toContain('@min-[960px]/conversation:!pr-[22rem]');

    // 4. Collapsing to capsule enters 'mini' mode (removes right reserve while preserving top clearance)
    fireEvent.click(screen.getByTestId('status-panel-collapse'));
    expect(panel.getAttribute('data-display-mode')).toBe('mini');
    expect(panel.getAttribute('data-state')).toBe('collapsed');
    expect(chatStream.className).toContain('pt-14');
    expect(chatStream.className).not.toContain('pr-[22rem]');
  });

  it('shows optimistic user message and ZCode ChatLoading Working indicator immediately upon send, deduplicates on user echo, and clears on approval/end', () => {
    const sendSpy = vi.spyOn(wsClient, 'sendEnvelope').mockReturnValue('cli_test_prompt');
    try {
      useNexStore.setState({
        mainView: 'workspace',
        activeSessionId: 'sess_working_1',
        selectedRunnerId: 'run_1',
        selectedProviderId: 'p1',
        selectedModelId: 'm1',
        locale: 'zh-CN',
        runners: [
          {
            id: 'run_1',
            name: 'LocalRunner',
            hostname: 'mac',
            os: 'darwin',
            arch: 'arm64',
            shell: '/bin/zsh',
            version: '0.3.0',
            tags: [],
            status: 'online',
            last_seen_at: Date.now(),
            synced_config_rev: 1,
            workspace_default: '/tmp/ws',
          },
        ],
        models: [
          {
            id: 'm1',
            provider_id: 'p1',
            display_name: 'Model One',
            source: 'custom',
            context_window: 128000,
            max_output_tokens: 4096,
            supports_vision: false,
            supports_pdf: false,
            supports_tools: true,
            supports_reasoning: true,
            supports_caching: false,
            pricing: { input_per_1m: 0, output_per_1m: 0, cache_read_per_1m: 0, cache_write_per_1m: 0 },
          },
        ],
        sessions: [
          {
            id: 'sess_working_1',
            runner_id: 'run_1',
            depth: 0,
            title: 'Working Feedback Test',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'full_access',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 2000,
            last_active_at: 2000,
          },
        ],
        streams: {
          sess_working_1: {
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
          },
        },
        composerText: '',
      });

      render(<App />);
      expect(screen.queryByTestId('chat-loading')).toBeNull();

      const input = screen.getByTestId('composer-input') as HTMLTextAreaElement;
      fireEvent.change(input, { target: { value: '请帮我分析项目结构' } });
      fireEvent.click(screen.getByTestId('send-btn'));

      // 1. Optimistic user bubble and ChatLoading indicator appear immediately before any WS event arrives
      expect(screen.getAllByText('请帮我分析项目结构').length).toBe(1);
      const loadingEl = screen.getByTestId('chat-loading');
      expect(loadingEl.textContent).toContain('工作中');

      // 2. Authoritative server events replace optimistic bubble cleanly without duplication
      act(() => {
        const cur = useNexStore.getState().streams.sess_working_1;
        let next = applySessionEvent(cur, 'run_1', {
          session_id: 'sess_working_1',
          seq: 1,
          turn_idx: 1,
          event_type: 'agent_start',
          payload: {},
        });
        next = applySessionEvent(next, 'run_1', {
          session_id: 'sess_working_1',
          seq: 2,
          turn_idx: 1,
          event_type: 'message_end',
          payload: { message: { role: 'user', content: [{ type: 'text', text: '请帮我分析项目结构' }] } },
        });
        useNexStore.setState((s) => ({
          streams: { ...s.streams, sess_working_1: next },
        }));
      });
      expect(screen.getAllByText('请帮我分析项目结构').length).toBe(1);
      expect(screen.getByTestId('chat-loading')).toBeTruthy();

      // 3. Approval prompt takes priority over ChatLoading
      act(() => {
        const cur = useNexStore.getState().streams.sess_working_1;
        const next = applySessionEvent(cur, 'run_1', {
          session_id: 'sess_working_1',
          seq: 3,
          turn_idx: 1,
          event_type: 'approval_requested',
          payload: { approval_id: 'appr_1', tool_call_id: 'tc_1', tool_name: 'bash', tool_args: { command: 'ls' } },
        });
        useNexStore.setState((s) => ({
          streams: { ...s.streams, sess_working_1: next },
        }));
      });
      expect(screen.queryByTestId('chat-loading')).toBeNull();

      // 4. Resolving approval restores ChatLoading until agent_end
      act(() => {
        const cur = useNexStore.getState().streams.sess_working_1;
        const next = applySessionEvent(cur, 'run_1', {
          session_id: 'sess_working_1',
          seq: 4,
          turn_idx: 1,
          event_type: 'approval_resolved',
          payload: { approval_id: 'appr_1' },
        });
        useNexStore.setState((s) => ({
          streams: { ...s.streams, sess_working_1: next },
        }));
      });
      expect(screen.getByTestId('chat-loading')).toBeTruthy();

      act(() => {
        const cur = useNexStore.getState().streams.sess_working_1;
        const next = applySessionEvent(cur, 'run_1', {
          session_id: 'sess_working_1',
          seq: 5,
          turn_idx: 1,
          event_type: 'agent_end',
          payload: {},
        });
        useNexStore.setState((s) => ({
          streams: { ...s.streams, sess_working_1: next },
        }));
      });
      expect(screen.queryByTestId('chat-loading')).toBeNull();
    } finally {
      sendSpy.mockRestore();
    }
  });

  it('previews conversation and markdown images in a translucent overlay modal', () => {
    const origBootstrap = useNexStore.getState().bootstrap;
    try {
      useNexStore.setState({
        bootstrap: vi.fn().mockResolvedValue(undefined),
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'workspace',
        activeSessionId: 'sess_img_1',
        selectedRunnerId: 'run_1',
        selectedProviderId: 'p1',
        selectedModelId: 'm1',
        models: [
          {
            id: 'm1',
            provider_id: 'p1',
            display_name: 'Model One',
            source: 'custom',
            context_window: 128000,
            max_output_tokens: 4096,
            supports_vision: true,
            supports_pdf: false,
            supports_tools: true,
            supports_reasoning: false,
            supports_caching: false,
            pricing: { input_per_1m: 0, output_per_1m: 0, cache_read_per_1m: 0, cache_write_per_1m: 0 },
          },
        ],
        sessions: [
          {
            id: 'sess_img_1',
            runner_id: 'run_1',
            depth: 0,
            title: 'Image Preview Test',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'full_access',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 2000,
            last_active_at: 2000,
          },
        ],
        streams: {
          sess_img_1: {
            lastSeq: 2,
            running: false,
            items: [
              {
                id: 'usr_1',
                seq: 1,
                role: 'user',
                text: 'Check this mockup',
                thinking: '',
                images: [{ data_b64: 'iVBORw0KGgo=', mime_type: 'image/png' }],
                toolCalls: [],
              },
              {
                id: 'ast_2',
                seq: 2,
                role: 'assistant',
                text: 'Rendered preview:\n\n![architecture diagram](https://example.com/diagram.png)',
                thinking: '',
                toolCalls: [],
              },
            ],
            latestTelemetry: null,
            turnTelemetries: [],
            uiBadges: {},
            pendingApproval: null,
            pendingUIPrompt: null,
            todos: [],
            goals: [],
          },
        },
        previewImage: null,
      });

      render(<App />);
      expect(screen.queryByTestId('image-preview-modal')).toBeNull();

      // Clicking user uploaded image opens translucent preview overlay
      fireEvent.click(screen.getByTestId('user-image-0'));
      expect(screen.getByTestId('image-preview-modal')).toBeTruthy();
      fireEvent.keyDown(window, { key: 'Escape' });
      expect(screen.queryByTestId('image-preview-modal')).toBeNull();

      // Clicking markdown image opens preview overlay and close button dismisses it
      fireEvent.click(screen.getByTestId('markdown-image-btn'));
      expect(screen.getByTestId('image-preview-modal')).toBeTruthy();
      fireEvent.click(screen.getByTestId('image-preview-close'));
      expect(screen.queryByTestId('image-preview-modal')).toBeNull();
    } finally {
      useNexStore.setState({ bootstrap: origBootstrap });
    }
  });

  it('opens only one concrete tab for file/review when closed, auto-collapses when closing last tab, and shows launcher on manual toggle', async () => {
    const origBootstrap = useNexStore.getState().bootstrap;
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(
      async () => new Response(JSON.stringify({ file: { content: 'package main\n', size: 13 } }), { status: 200 }),
    );
    const rpcSpy = vi.spyOn(wsClient, 'requestRPC').mockImplementation(async (method) => ({
      v: 1,
      id: 'rpc_1',
      type: method,
      ts: Date.now(),
      payload:
        method === 'runner.git_diff'
          ? { branch: 'main', dirty: false, additions: 0, deletions: 0, files: [], untracked: [] }
          : { content: 'package main\n' },
    }));

    try {
      useNexStore.setState({
        bootstrap: vi.fn().mockResolvedValue(undefined),
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'workspace',
        activeSessionId: 'sess_tab_1',
        selectedRunnerId: 'run_1',
        sidePaneVisible: false,
        rightTabs: [],
        activeRightTabId: '',
        sessions: [
          {
            id: 'sess_tab_1',
            runner_id: 'run_1',
            depth: 0,
            title: 'Single Tab Test',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'full_access',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 2000,
            last_active_at: 2000,
          },
        ],
      });

      render(<App />);
      expect(useNexStore.getState().sidePaneVisible).toBe(false);

      // Opening Review while closed shows ONLY the review tab (no New tab)
      act(() => {
        void useNexStore.getState().openRightTab('review');
      });
      await waitFor(() => {
        const tabs = useNexStore.getState().rightTabs;
        expect(tabs.length).toBe(1);
        expect(tabs[0].kind).toBe('review');
        expect(useNexStore.getState().sidePaneVisible).toBe(true);
      });
      expect(screen.queryByTestId('workbench-tab-new_tab')).toBeNull();
      expect(screen.getByTestId('workbench-tab-review')).toBeTruthy();

      // Closing the last tab auto-collapses side pane
      fireEvent.click(screen.getByTestId('workbench-tab-close-review'));
      expect(useNexStore.getState().rightTabs).toHaveLength(0);
      expect(useNexStore.getState().sidePaneVisible).toBe(false);

      // Manual toggle when empty creates and activates 'new_tab' launcher
      fireEvent.click(screen.getByTestId('side-pane-toggle'));
      expect(useNexStore.getState().sidePaneVisible).toBe(true);
      expect(useNexStore.getState().rightTabs.map((t) => t.kind)).toEqual(['new_tab']);
    } finally {
      useNexStore.setState({ bootstrap: origBootstrap });
      rpcSpy.mockRestore();
      fetchMock.mockRestore();
    }
  });

  it('keeps ConversationStatusPanel rounded-2xl without foreground bash clutter and clears stale sidebar running dot', () => {
    const origBootstrap = useNexStore.getState().bootstrap;
    try {
      useNexStore.setState({
        bootstrap: vi.fn().mockResolvedValue(undefined),
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'workspace',
        sidebarVisible: true,
        activeSessionId: 'sess_st_1',
        selectedRunnerId: 'run_1',
        gitSummary: null,
        rightTabs: [],
        bottomTerminals: [],
        sessions: [
          {
            id: 'sess_st_1',
            runner_id: 'run_1',
            depth: 0,
            title: 'Active Stale Status',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'full_access',
            status: 'running', // stale DB status while stream already stopped
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 2000,
            last_active_at: 2000,
          },
        ],
        streams: {
          sess_st_1: {
            lastSeq: 2,
            running: false,
            items: [
              {
                id: 'ast_1',
                seq: 1,
                role: 'assistant',
                text: '',
                thinking: '',
                toolCalls: [
                  {
                    id: 'tc_fg',
                    name: 'bash',
                    argsText: JSON.stringify({ command: 'ls -la' }),
                    streamOutput: '',
                    finalOutput: '',
                    status: 'running',
                  },
                ],
              },
            ],
            latestTelemetry: null,
            turnTelemetries: [],
            uiBadges: {},
            pendingApproval: null,
            pendingUIPrompt: null,
            todos: [],
            goals: [],
          },
        },
      });

      render(<App />);
      // Stale running status in DB does NOT show pulsing blue dot once stream finished
      expect(document.querySelector('.bg-brand-blue.animate-pulse')).toBeNull();
      // Ordinary foreground bash does NOT open status panel
      expect(screen.queryByTestId('conversation-status-panel')).toBeNull();

      // Background bash DOES show status panel with uniform rounded-2xl (never rounded-full)
      act(() => {
        const cur = useNexStore.getState().streams.sess_st_1;
        useNexStore.setState({
          streams: {
            sess_st_1: {
              ...cur,
              running: true,
              items: [
                {
                  id: 'ast_1',
                  seq: 2,
                  role: 'assistant',
                  text: '',
                  thinking: '',
                  toolCalls: [
                    {
                      id: 'tc_bg',
                      name: 'bash',
                      argsText: JSON.stringify({ command: 'npm run dev', run_in_background: true }),
                      streamOutput: '',
                      finalOutput: '',
                      status: 'running',
                    },
                  ],
                },
              ],
            },
          },
        });
      });

      const panel = screen.getByTestId('conversation-status-panel');
      expect(panel.className).toContain('rounded-2xl');
      expect(panel.className).not.toContain('rounded-full');

      // Expanding to panel mode preserves rounded-2xl and never switches to rounded-full
      fireEvent.click(screen.getByTestId('status-panel-mini'));
      expect(panel.getAttribute('data-display-mode')).toBe('panel');
      expect(panel.className).toContain('rounded-2xl');
      expect(panel.className).not.toContain('rounded-full');
    } finally {
      useNexStore.setState({ bootstrap: origBootstrap });
    }
  });

  it('binds models per conversation, keeps draft changes isolated, and blocks sending when saved model is missing', () => {
    const origBootstrap = useNexStore.getState().bootstrap;
    const sendSpy = vi.spyOn(wsClient, 'sendEnvelope').mockImplementation(() => 'env_1');
    try {
      useNexStore.setState({
        bootstrap: vi.fn().mockResolvedValue(undefined),
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'workspace',
        activeSessionId: 'sess_m1',
        selectedRunnerId: 'run_1',
        selectedProviderId: 'p1',
        selectedModelId: 'model-a',
        draftProviderId: 'p1',
        draftModelId: 'model-a',
        sessionModels: {},
        models: [
          {
            id: 'model-a',
            provider_id: 'p1',
            display_name: 'Model Alpha',
            source: 'custom',
            context_window: 128000,
            max_output_tokens: 4096,
            supports_vision: false,
            supports_pdf: false,
            supports_tools: true,
            supports_reasoning: false,
            supports_caching: false,
            pricing: { input_per_1m: 0, output_per_1m: 0, cache_read_per_1m: 0, cache_write_per_1m: 0 },
          },
          {
            id: 'model-b',
            provider_id: 'p1',
            display_name: 'Model Beta',
            source: 'custom',
            context_window: 128000,
            max_output_tokens: 4096,
            supports_vision: false,
            supports_pdf: false,
            supports_tools: true,
            supports_reasoning: false,
            supports_caching: false,
            pricing: { input_per_1m: 0, output_per_1m: 0, cache_read_per_1m: 0, cache_write_per_1m: 0 },
          },
        ],
        sessions: [
          {
            id: 'sess_m1',
            runner_id: 'run_1',
            depth: 0,
            title: 'Session Alpha',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'model-a',
            provider_id: 'p1',
            permission_mode: 'full_access',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 2000,
            last_active_at: 2000,
          },
          {
            id: 'sess_missing',
            runner_id: 'run_1',
            depth: 0,
            title: 'Session Missing Model',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'deleted-model',
            provider_id: 'p_gone',
            permission_mode: 'full_access',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 1500,
            last_active_at: 1500,
          },
        ],
        composerText: 'hello',
      });

      render(<App />);
      expect(screen.getByTestId('model-effort-btn').textContent).toContain('Model Alpha');

      // New conversation draft selection does NOT mutate sess_m1
      act(() => {
        useNexStore.getState().startNewSession();
        useNexStore.getState().setSelectedModel('p1', 'model-b');
      });
      expect(useNexStore.getState().draftModelId).toBe('model-b');

      // Switching back to sess_m1 restores Model Alpha
      act(() => {
        void useNexStore.getState().selectSession('sess_m1');
      });
      expect(useNexStore.getState().selectedModelId).toBe('model-a');
      expect(screen.getByTestId('model-effort-btn').textContent).toContain('Model Alpha');

      // Switching to sess_missing leaves model empty and blocks sending
      act(() => {
        void useNexStore.getState().selectSession('sess_missing');
      });
      expect(useNexStore.getState().selectedModelId).toBe('');
      expect(screen.getByTestId('model-effort-btn').textContent).toContain('选择模型');
      expect((screen.getByTestId('send-btn') as HTMLButtonElement).disabled).toBe(true);

      sendSpy.mockClear();
      act(() => {
        void useNexStore.getState().sendPromptOrSteer('should not send');
      });
      expect(sendSpy).not.toHaveBeenCalled();
    } finally {
      useNexStore.setState({ bootstrap: origBootstrap });
      sendSpy.mockRestore();
    }
  });

  it('preserves Ctrl+K for Emacs line editing and opens Command Palette only with Cmd+K', () => {
    const origBootstrap = useNexStore.getState().bootstrap;
    try {
      useNexStore.setState({
        bootstrap: vi.fn().mockResolvedValue(undefined),
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'workspace',
        quickSearchOpen: false,
      });
      render(<App />);

      // Ctrl+K (no Meta) must NOT hijack into Command Palette
      fireEvent.keyDown(window, { key: 'k', ctrlKey: true, metaKey: false });
      expect(useNexStore.getState().quickSearchOpen).toBe(false);
      expect(screen.queryByTestId('command-palette-modal')).toBeNull();

      // Cmd+K (Meta without Ctrl) opens Command Palette
      fireEvent.keyDown(window, { key: 'k', metaKey: true, ctrlKey: false });
      expect(useNexStore.getState().quickSearchOpen).toBe(true);
      expect(screen.getByTestId('command-palette-modal')).toBeTruthy();
    } finally {
      useNexStore.setState({ bootstrap: origBootstrap, quickSearchOpen: false });
    }
  });

  it('renders built-in explore/worker subagent model overrides defaulting to main model and saves changes', async () => {
    const origBootstrap = useNexStore.getState().bootstrap;
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(
      async () => new Response(JSON.stringify({ slug: 'explore' }), { status: 200 }),
    );
    const refreshSpy = vi.fn().mockResolvedValue(undefined);
    try {
      useNexStore.setState({
        bootstrap: vi.fn().mockResolvedValue(undefined),
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'settings',
        settingsSection: 'agents',
        providers: [
          { id: 'prov_1', name: 'OpenAI', driver: 'openai_chat', base_url: '', api_key: '', proxy_mode: 'direct', enabled: true },
        ],
        models: [
          {
            id: 'gpt-4o-mini',
            provider_id: 'prov_1',
            display_name: 'GPT-4o Mini',
            source: 'custom',
            context_window: 128000,
            max_output_tokens: 4096,
            supports_vision: false,
            supports_pdf: false,
            supports_tools: true,
            supports_reasoning: true,
            reasoning_levels: ['low', 'medium', 'high'],
            supports_caching: false,
            pricing: { input_per_1m: 0, output_per_1m: 0, cache_read_per_1m: 0, cache_write_per_1m: 0 },
          },
        ],
        agentProfiles: [],
        refreshRegistries: refreshSpy,
      });

      render(<App />);
      expect(screen.getByTestId('builtin-subagents-section')).toBeTruthy();
      const exploreSelect = screen.getByTestId('builtin-subagent-explore-model-select') as HTMLSelectElement;
      const workerSelect = screen.getByTestId('builtin-subagent-worker-model-select') as HTMLSelectElement;
      expect(exploreSelect.value).toBe('');
      expect(workerSelect.value).toBe('');
      expect(exploreSelect.options[0].textContent).toContain('默认（与主模型一致）');

      fireEvent.change(exploreSelect, { target: { value: 'prov_1::gpt-4o-mini' } });
      await waitFor(() => {
        expect(fetchMock).toHaveBeenCalledWith(
          expect.stringContaining('/api/v1/agents'),
          expect.objectContaining({ method: 'POST' }),
        );
      });
      const postCall = fetchMock.mock.calls.find(([u]) => String(u).includes('/api/v1/agents'));
      expect(postCall).toBeTruthy();
      const sentBody = JSON.parse(String(postCall?.[1]?.body || '{}'));
      expect(sentBody.slug).toBe('explore');
      expect(sentBody.model_ref).toBe('prov_1::gpt-4o-mini');
    } finally {
      useNexStore.setState({ bootstrap: origBootstrap });
      fetchMock.mockRestore();
    }
  });
});
