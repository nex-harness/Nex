import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { App } from '../App';
import { MarkdownRenderer } from '../features/MarkdownRenderer';
import { BROWSER_BINARY_SCREENCAST, decodePTYBinaryFrame, encodePTYBinaryFrame, PTY_BINARY_STDOUT } from '../core/protocol';
import { applySessionEvent, emptyStreamState } from '../core/reducer';
import {
  persistActiveSessionId,
  persistComposerDraft,
  persistViewState,
  readPersistedComposerDraft,
  readPersistedSessionId,
  readPersistedViewState,
  useNexStore,
} from '../core/store';
import { wsClient } from '../core/wsClient';
import { createHostBridge } from '../host/bridge';
import { formatRunnerVersion, parsePastedSSHTarget } from '../features/SettingsRunners';
import { computeTurnTickLayout } from '../features/ConversationPane';
import { ConversationStatusPanel } from '../features/ConversationStatusPanel';

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

describe('Nex-v3 Workbench Layout, Blocks, i18n & On-Demand Side Pane', () => {
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

  it('starts with Side Pane closed by default, opens New Tab launcher on demand, and navigates Settings Center', () => {
    useNexStore.setState({ locale: 'zh-CN' });
    render(<App />);
    expect(screen.getByTestId('project-sidebar')).toBeTruthy();

    // Right Side Pane is NOT persistent by default!
    expect(screen.queryByTestId('workbench-plus-btn')).toBeNull();

    // Click header Side Pane toggle -> reveals New Tab launcher & moves close button into Side Pane tab bar
    fireEvent.click(screen.getByTestId('side-pane-toggle'));
    expect(screen.getByTestId('workbench-plus-btn')).toBeTruthy();
    expect(screen.queryByTestId('side-pane-toggle')).toBeNull();

    // Close Side Pane again via its tab-strip close button (smooth animated collapse keeps container mounted with data-state="closed")
    fireEvent.click(screen.getByTestId('side-pane-close'));
    expect(screen.getByTestId('side-pane-container').getAttribute('data-state')).toBe('closed');
    expect(screen.getByTestId('side-pane-toggle')).toBeTruthy();

    // Open localized Context Windows popover (`上下文容量`)
    fireEvent.click(screen.getByTestId('context-ring-btn'));
    expect(screen.getByTestId('context-popover')).toBeTruthy();
    expect(screen.getByText('上下文容量')).toBeTruthy();

    // Navigate to full-page Settings Center and switch Settings Center sections
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

  it('preserves frozen history item object references across high-frequency streaming deltas (O(tail) rule)', () => {
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
        { name: 'DemoApp', path: '/Users/dev/Projects/DemoApp' },
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
      expect(screen.getByTestId('dir-entry-DemoApp')).toBeTruthy();
    });

    // Quick-select DemoApp directory
    fireEvent.click(screen.getByTestId('dir-pick-DemoApp'));
    expect(useNexStore.getState().selectedWorkspacePath).toBe('/Users/dev/Projects/DemoApp');
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
    // Skills page is now a discovery & workspace-visibility manager (no inline skill creation editor)
    expect(screen.getByTestId('skills-manager-section')).toBeTruthy();
    expect(screen.getByTestId('skills-workspace-select')).toBeTruthy();
    expect(screen.queryByTestId('skill-md-editor')).toBeNull();

    // 5. MCP section exposes install modal (Presets/Form/JSON), exposure policy, and workspace visibility
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
    const originalOpenFile = useNexStore.getState().openFileInPreviewTab;
    const openFileSpy = vi.fn().mockResolvedValue(undefined);
    try {
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
    } finally {
      useNexStore.setState({ openFileInPreviewTab: originalOpenFile });
    }
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

    // 2. Smart SSH paste parser (RDUX-27)
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

      // 5. Remote Devices (Runners) Settings exposes unified Connect via SSH + Manual Join + Refresh toolbar and modals
      useNexStore.setState({
        settingsSection: 'runners',
        sshTargets: [],
      });
      rerender(<App />);
      expect(screen.queryByTestId('ssh-target-ssh_rb1')).toBeNull();
      expect(screen.getByTestId('manual-join-btn')).toBeTruthy();
      fireEvent.click(screen.getByTestId('manual-join-btn'));
      expect(screen.getByTestId('manual-join-modal')).toBeTruthy();
      expect(screen.getByTestId('runner-manual-join-cmd')).toBeTruthy();
      fireEvent.mouseDown(screen.getByTestId('manual-join-modal-backdrop'));
      expect(screen.queryByTestId('manual-join-modal')).toBeNull();

      fireEvent.click(screen.getByTestId('add-ssh-server-btn'));
      const modal = screen.getByTestId('ssh-server-modal');
      expect(modal).toBeTruthy();
      expect((screen.getByTestId('ssh-username-input') as HTMLInputElement).placeholder).toBe('root');
      expect(modal.querySelector('select')).toBeNull();
      fireEvent.click(screen.getByTestId('ssh-advanced-toggle'));
      expect((screen.getByTestId('ssh-workspace-input') as HTMLInputElement).placeholder).toBe('$HOME');
      expect(screen.getByTestId('ssh-import-config-btn')).toBeTruthy();
      fireEvent.click(screen.getByTestId('ssh-mode-remote_install'));
      expect(screen.queryByTestId('ssh-download-url-input')).toBeNull();
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

  it('shows optimistic user message and ChatLoading Working indicator immediately upon send, deduplicates on user echo, and clears on approval/end', () => {
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

      // Clicking markdown image opens preview overlay, zoom/rotate controls update transform, and close button dismisses it
      fireEvent.click(screen.getByTestId('markdown-image-btn'));
      expect(screen.getByTestId('image-preview-modal')).toBeTruthy();
      const previewImg = screen.getByTestId('image-preview-img');
      expect(previewImg.className).not.toContain('nex-dialog-content');
      expect(previewImg.style.transform).toBe('scale(1) rotate(0deg)');

      fireEvent.click(screen.getByLabelText(/Zoom in|放大/i));
      expect(previewImg.style.transform).toBe('scale(1.25) rotate(0deg)');
      fireEvent.click(screen.getByLabelText(/Rotate right|向右旋转/i));
      expect(previewImg.style.transform).toBe('scale(1.25) rotate(90deg)');
      fireEvent.click(screen.getByLabelText(/Rotate left|向左旋转/i));
      expect(previewImg.style.transform).toBe('scale(1.25) rotate(0deg)');
      fireEvent.click(screen.getByLabelText(/Zoom out|缩小/i));
      expect(previewImg.style.transform).toBe('scale(1) rotate(0deg)');

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
      // Stale running status in DB does NOT show running spinner once stream finished
      expect(document.querySelector('.nex-running-dots')).toBeNull();
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

  it('encodes and decodes binary 0x10 browser screencast frames', () => {
    const tabId = 'b9777673-535b-4d67-a726-b390c39ed46d';
    const jpegBytes = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 0x01, 0x02]);
    const frame = encodePTYBinaryFrame(BROWSER_BINARY_SCREENCAST, tabId, jpegBytes);
    const decoded = decodePTYBinaryFrame(frame);
    expect(decoded).not.toBeNull();
    expect(decoded?.op).toBe(BROWSER_BINARY_SCREENCAST);
    expect(decoded?.op).toBe(0x10);
    expect(decoded?.ptyId).toBe(tabId);
    expect(Array.from(decoded!.data)).toEqual(Array.from(jpegBytes));
  });

  it('renders browser tool screenshot thumbnail and opens image preview modal on click', () => {
    let st = emptyStreamState();
    st = applySessionEvent(st, 'run_1', {
      session_id: 's_browser_img',
      seq: 1,
      turn_idx: 1,
      event_type: 'message_end',
      payload: {
        message: {
          role: 'assistant',
          content: [
            {
              type: 'tool_call',
              tool_call_id: 'call_browser_1',
              tool_name: 'browser',
              arguments: { action: 'screenshot', url: 'http://127.0.0.1:3000' },
            },
          ],
        },
      },
    });
    st = applySessionEvent(st, 'run_1', {
      session_id: 's_browser_img',
      seq: 2,
      turn_idx: 1,
      event_type: 'tool_execution_start',
      payload: {
        tool_call_id: 'call_browser_1',
        tool_name: 'browser',
        args: { action: 'screenshot', url: 'http://127.0.0.1:3000' },
      },
    });
    st = applySessionEvent(st, 'run_1', {
      session_id: 's_browser_img',
      seq: 3,
      turn_idx: 1,
      event_type: 'tool_execution_end',
      payload: {
        tool_call_id: 'call_browser_1',
        tool_name: 'browser',
        tool_result: {
          output: '{"ok":true}',
          extra_blocks: [{ type: 'image', mime_type: 'image/png', data_b64: 'iVBORw0KGgo=' }],
        },
      },
    });

    useNexStore.setState({
      authEnabled: false,
      authenticated: true,
      mainView: 'workspace',
      locale: 'en-US',
      activeSessionId: 's_browser_img',
      selectedRunnerId: 'run_1',
      sidePaneVisible: false,
      rightTabs: [],
      previewImage: null,
      streams: { s_browser_img: st },
    });

    render(<App />);
    const thumbBtn = screen.getByTestId('tool-image-0');
    const img = thumbBtn.querySelector('img') as HTMLImageElement;
    expect(img).toBeTruthy();
    expect(img.src).toContain('data:image/png;base64,iVBORw0KGgo=');

    fireEvent.click(thumbBtn);
    expect(screen.getByTestId('image-preview-modal')).toBeTruthy();
  });

  it('opens singleton browser tab, renders handoff banner, dispatches URL navigate & handoff actions, and auto-collapses on close', async () => {
    const origConnected = wsClient.connected;
    wsClient.connected = true;
    const rpcSpy = vi.spyOn(wsClient, 'requestRPC').mockResolvedValue({
      v: 1,
      type: 'rpc.response',
      payload: {
        session_id: 's_browser_tab',
        active_tab_id: 'tab_uuid_1',
        tabs: [
          {
            tab_id: 'tab_uuid_1',
            url: 'https://example.com',
            title: 'Example',
            viewport: { width: 1280, height: 720 },
            active: true,
            can_go_back: false,
            can_go_forward: false,
          },
        ],
        handoff: {
          session_id: 's_browser_tab',
          tab_id: 'tab_uuid_1',
          state: 'awaiting_human',
          reason: 'Login required',
        },
      },
    } as never);
    const sendSpy = vi.spyOn(wsClient, 'sendEnvelope').mockReturnValue('req_1');

    try {
      useNexStore.setState({
        authEnabled: false,
        authenticated: true,
        mainView: 'workspace',
        locale: 'en-US',
        activeSessionId: 's_browser_tab',
        selectedRunnerId: 'run_1',
        sidePaneVisible: false,
        rightTabs: [],
        activeRightTabId: '',
        browserStates: {
          s_browser_tab: {
            session_id: 's_browser_tab',
            active_tab_id: 'tab_uuid_1',
            tabs: [
              {
                tab_id: 'tab_uuid_1',
                url: 'http://localhost:3000',
                title: 'Local App',
                viewport: { width: 1280, height: 720 },
                active: true,
                can_go_back: false,
                can_go_forward: false,
              },
            ],
            handoff: {
              session_id: 's_browser_tab',
              tab_id: 'tab_uuid_1',
              state: 'awaiting_human',
              reason: 'Login required',
            },
          },
        },
      });

      await act(async () => {
        await useNexStore.getState().openRightTab('browser');
      });

      expect(useNexStore.getState().sidePaneVisible).toBe(true);
      expect(useNexStore.getState().rightTabs.filter((t) => t.kind === 'browser')).toHaveLength(1);

      render(<App />);
      expect(screen.getByTestId('browser-tab-panel')).toBeTruthy();
      expect(screen.getByTestId('browser-handoff-banner')).toBeTruthy();

      const urlInput = screen.getByTestId('browser-url-input') as HTMLInputElement;
      fireEvent.change(urlInput, { target: { value: 'https://example.com' } });
      fireEvent.submit(urlInput.closest('form')!);

      await waitFor(() => {
        expect(rpcSpy).toHaveBeenCalledWith(
          'browser.control',
          expect.objectContaining({
            session_id: 's_browser_tab',
            tab_id: 'tab_uuid_1',
            action: 'navigate',
            url: 'https://example.com',
          }),
          expect.objectContaining({ runner_id: 'run_1', session_id: 's_browser_tab' }),
        );
      });

      fireEvent.click(screen.getByTestId('browser-handoff-resume'));
      expect(sendSpy).toHaveBeenCalledWith(
        'browser.handoff',
        expect.objectContaining({
          session_id: 's_browser_tab',
          tab_id: 'tab_uuid_1',
          state: 'resumed',
          resumed: true,
        }),
        expect.objectContaining({ runner_id: 'run_1', session_id: 's_browser_tab' }),
      );

      const browserTabId = useNexStore.getState().rightTabs.find((t) => t.kind === 'browser')?.id || '';
      act(() => {
        useNexStore.getState().closeRightTab(browserTabId);
      });
      expect(useNexStore.getState().rightTabs).toHaveLength(0);
      expect(useNexStore.getState().sidePaneVisible).toBe(false);
    } finally {
      wsClient.connected = origConnected;
      rpcSpy.mockRestore();
      sendSpy.mockRestore();
    }
  });

  it('renders Browser settings section with runner probe/install and built-in Browser subagent model selector', async () => {
    const origProbe = useNexStore.getState().probeRunnerBrowser;
    const probeMock = vi.fn(async (runnerId: string) => {
      const readyRes = {
        status: 'ready',
        browser_name: 'Google Chrome',
        version: 'Google Chrome 158.0.8127.89',
        executable_path: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
      };
      useNexStore.setState((s) => ({
        browserProbes: { ...s.browserProbes, [runnerId]: readyRes },
      }));
      return readyRes;
    });

    try {
      useNexStore.setState({
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'settings',
        settingsSection: 'browser',
        runners: [
          {
            id: 'runner_1',
            name: 'Local Runner',
            status: 'online',
            hostname: 'macbook.local',
            os: 'darwin',
            arch: 'arm64',
            workspace_root: '/tmp/ws',
          } as never,
        ],
        browserProbes: {},
        probeRunnerBrowser: probeMock,
      });

      const { rerender } = render(<App />);
      expect(screen.getByTestId('settings-browser-section')).toBeTruthy();
      expect(screen.getByTestId('browser-mode-select')).toBeTruthy();
      expect(screen.getByTestId('browser-runner-probe-runner_1')).toBeTruthy();

      await waitFor(() => {
        expect(probeMock).toHaveBeenCalledWith('runner_1');
        expect(screen.getByTestId('browser-runner-active-runner_1')).toBeTruthy();
      });

      const activeInfo = screen.getByTestId('browser-runner-active-runner_1');
      expect(activeInfo.textContent).toContain('Google Chrome 158.0.8127.89');
      expect(activeInfo.textContent).toContain('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome');
      expect(screen.queryByTestId('browser-runner-install-runner_1')).toBeNull();

      act(() => {
        useNexStore.setState({
          browserProbes: {
            runner_1: { status: 'missing_binary' },
          },
        });
      });
      expect(screen.getByTestId('browser-runner-install-runner_1')).toBeTruthy();

      act(() => {
        useNexStore.setState({ settingsSection: 'agents', agentProfiles: [] });
      });
      rerender(<App />);

      const browserModelSelect = screen.getByTestId('builtin-subagent-browser-model-select') as HTMLSelectElement;
      expect(browserModelSelect).toBeTruthy();
      expect(browserModelSelect.value).toBe('');
    } finally {
      useNexStore.setState({ probeRunnerBrowser: origProbe });
    }
  });

  it('detects mygo desktop/mobile hosts via window.mygo and invokes typed NexHost IPC & lifecycle events', async () => {
    const origMyGo = (globalThis as { mygo?: unknown }).mygo;
    try {
      const listeners = new Map<string, (payload: unknown) => void>();
      const callMock = vi.fn(async (method: string, ...args: unknown[]) => {
        if (method === 'NexHost.Info') {
          return {
            platform: 'ios',
            form_factor: 'mobile',
            version: '0.3.0',
            connection_mode: 'unconfigured',
            server_url: '',
            supports_local_mode: false,
            recent_servers: [],
          };
        }
        if (method === 'NexHost.SetServerURL') {
          return {
            platform: 'ios',
            form_factor: 'mobile',
            version: '0.3.0',
            connection_mode: 'remote',
            server_url: String(args[0] || ''),
            supports_local_mode: false,
            recent_servers: [String(args[0] || '')],
          };
        }
        return null;
      });
      const onMock = vi.fn((ev: string, fn: (payload: unknown) => void) => {
        listeners.set(ev, fn);
        return () => listeners.delete(ev);
      });

      (globalThis as { mygo?: unknown }).mygo = {
        platform: 'ios',
        windowId: 1,
        version: '0.10.0',
        call: callMock,
        callWith: (method: string, args: unknown[]) => callMock(method, ...(args || [])),
        on: onMock,
      };
      const iosBridge = createHostBridge();
      expect(iosBridge.kind).toBe('mygo-mobile');
      expect(iosBridge.platform).toBe('ios');
      const info = await iosBridge.getHostInfo();
      expect(info?.platform).toBe('ios');
      expect(callMock).toHaveBeenCalledWith('NexHost.Info');
      await iosBridge.setServerUrl('https://nex.tylinux.cn');
      expect(callMock).toHaveBeenCalledWith('NexHost.SetServerURL', 'https://nex.tylinux.cn');

      const resumeSpy = vi.fn();
      iosBridge.onAppResume(resumeSpy);
      expect(onMock).toHaveBeenCalledWith('nex:app-resume', resumeSpy);
      listeners.get('nex:app-resume')?.(1700000000000);
      expect(resumeSpy).toHaveBeenCalledWith(1700000000000);

      (globalThis as { mygo?: unknown }).mygo = { platform: 'android', call: callMock, on: onMock };
      expect(createHostBridge().kind).toBe('mygo-mobile');
      expect(createHostBridge().platform).toBe('android');

      (globalThis as { mygo?: unknown }).mygo = { platform: 'darwin', call: callMock, on: onMock };
      expect(createHostBridge().kind).toBe('mygo-desktop');
      expect(createHostBridge().platform).toBe('darwin');
    } finally {
      (globalThis as { mygo?: unknown }).mygo = origMyGo;
    }
  });

  it('renders ServerConnectGate with health check validation and toggles local-mode button by host capability', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(
      async () => new Response(JSON.stringify({ ok: true }), { status: 200 }),
    );
    const connectSpy = vi.fn(async () => ({ ok: true }));
    const origConnect = useNexStore.getState().connectToServer;

    try {
      useNexStore.setState({
        authEnabled: false,
        authenticated: true,
        locale: 'en-US',
        serverConnectRequired: true,
        activeServerUrl: '',
        hostInfo: {
          platform: 'darwin',
          form_factor: 'desktop',
          version: '0.3.0',
          connection_mode: 'unconfigured',
          server_url: '',
          paired_hosts: [],
          supports_local_mode: true,
          notifications_supported: true,
          recent_servers: [],
        },
        connectToServer: connectSpy,
      });

      const { rerender } = render(<App />);
      expect(screen.getByTestId('server-connect-gate')).toBeTruthy();
      expect(screen.getByTestId('server-local-mode-btn')).toBeTruthy();

      fireEvent.change(screen.getByTestId('server-url-input'), {
        target: { value: 'https://nex.tylinux.cn/' },
      });
      fireEvent.click(screen.getByTestId('server-connect-btn'));

      await waitFor(() => {
        expect(fetchSpy).toHaveBeenCalledWith('https://nex.tylinux.cn/api/v1/health');
        expect(connectSpy).toHaveBeenCalledWith('https://nex.tylinux.cn');
      });

      act(() => {
        useNexStore.setState({
          hostInfo: {
            platform: 'android',
            form_factor: 'mobile',
            version: '0.3.0',
            connection_mode: 'unconfigured',
            server_url: '',
            paired_hosts: [],
            supports_local_mode: false,
            notifications_supported: true,
            recent_servers: [],
          },
        });
      });
      rerender(<App />);
      expect(screen.queryByTestId('server-local-mode-btn')).toBeNull();
    } finally {
      useNexStore.setState({ serverConnectRequired: false, hostInfo: null, connectToServer: origConnect });
      fetchSpy.mockRestore();
    }
  });

  it('adapts sidebar drawer, full-screen right pane, and settings navigation on compact mobile viewports (<768px)', () => {
    const origWidth = window.innerWidth;
    Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: 390 });

    try {
      useNexStore.setState({
        authEnabled: false,
        authenticated: true,
        serverConnectRequired: false,
        mainView: 'workspace',
        locale: 'en-US',
        sidebarVisible: true,
        sidePaneVisible: true,
        rightTabs: [{ id: 'tab_m_1', kind: 'browser', title: 'Browser', closable: true }],
        activeRightTabId: 'tab_m_1',
        hostInfo: {
          platform: 'ios',
          form_factor: 'mobile',
          version: '0.3.0',
          connection_mode: 'remote',
          server_url: 'https://nex.tylinux.cn',
          paired_hosts: [],
          supports_local_mode: false,
          notifications_supported: true,
          recent_servers: ['https://nex.tylinux.cn'],
        },
        activeServerUrl: 'https://nex.tylinux.cn',
      });

      const { rerender } = render(<App />);
      expect(screen.getByTestId('mobile-sidebar-backdrop')).toBeTruthy();
      expect(screen.getByTestId('side-pane-container').className).toContain('fixed inset-0');
      expect(screen.getByTestId('project-sidebar').innerHTML).toContain('nex-titlebar-inset-left');

      act(() => {
        useNexStore.setState({ mainView: 'settings', settingsSection: 'general' });
      });
      rerender(<App />);
      expect(screen.getByTestId('settings-mobile-nav-back')).toBeTruthy();
      expect(screen.getByTestId('settings-page').querySelector('nav')?.className).toContain('nex-titlebar-top');
      expect(screen.getByTestId('settings-server-connection')).toBeTruthy();
      expect(screen.getByTestId('settings-switch-server-btn')).toBeTruthy();
    } finally {
      Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: origWidth });
      useNexStore.setState({
        mainView: 'workspace',
        sidePaneVisible: false,
        rightTabs: [],
        activeRightTabId: '',
        hostInfo: null,
        activeServerUrl: '',
      });
    }
  });

  it('dispatches task_completed and question notifications with focus/background rules and click-to-open-session', async () => {
    const origNotif = (globalThis as { Notification?: unknown }).Notification;
    const origHasFocus = document.hasFocus;
    const origVisDesc = Object.getOwnPropertyDescriptor(document, 'visibilityState');
    const origSelect = useNexStore.getState().selectSession;
    const selectSpy = vi.fn(async (sid: string) => {
      useNexStore.setState({ activeSessionId: sid });
    });

    const createdNotifications: Array<{ title: string; options?: NotificationOptions; onclick: (() => void) | null; close: ReturnType<typeof vi.fn> }> = [];
    class MockNotification {
      static permission: NotificationPermission = 'granted';
      static requestPermission = vi.fn(async () => 'granted' as NotificationPermission);
      onclick: (() => void) | null = null;
      close = vi.fn();
      constructor(title: string, options?: NotificationOptions) {
        const entry = { title, options, onclick: null as (() => void) | null, close: this.close };
        createdNotifications.push(entry);
        return new Proxy(this, {
          set(target, prop, val) {
            if (prop === 'onclick') entry.onclick = val as (() => void) | null;
            (target as Record<PropertyKey, unknown>)[prop] = val;
            return true;
          },
        });
      }
    }

    try {
      (globalThis as { Notification?: unknown }).Notification = MockNotification;
      document.hasFocus = () => true;
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });

      useNexStore.setState({
        authEnabled: false,
        authenticated: true,
        serverConnectRequired: false,
        locale: 'en-US',
        mainView: 'workspace',
        activeSessionId: 'sess_active',
        notificationToasts: [],
        sessions: [
          {
            id: 'sess_active',
            runner_id: 'r1',
            depth: 0,
            title: 'Active Task',
            workspace_path: '/ws',
            agent_profile_slug: 'default',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'interactive',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 0,
            created_at: 1,
            updated_at: 1,
            last_active_at: 1,
          },
          {
            id: 'sess_bg_1',
            runner_id: 'r1',
            depth: 0,
            title: 'Build Release',
            workspace_path: '/ws',
            agent_profile_slug: 'default',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'interactive',
            status: 'running',
            latest_context_tokens: 0,
            context_window_limit: 0,
            created_at: 1,
            updated_at: 1,
            last_active_at: 1,
          },
        ],
        preferences: {
          theme: 'light',
          code_theme: 'github-light',
          font_family: 'Inter',
          mono_font_family: 'monospace',
          expand_thinking: false,
          auto_archive_after_days: 30,
          notify_enabled: true,
          notify_on_complete: true,
          notify_on_input: true,
          notify_only_background: true,
        },
        selectSession: selectSpy,
      });

      // 1. Focused on same active session with notify_only_background=true -> suppressed
      await act(async () => {
        await useNexStore.getState().dispatchClientNotification({
          session_id: 'sess_active',
          kind: 'task_completed',
          session_title: 'Active Task',
          request_id: 'r_suppressed',
        });
      });
      expect(createdNotifications).toHaveLength(0);

      // 2. Different session while focused -> delivers browser Notification AND pushes in-app toast
      await act(async () => {
        await useNexStore.getState().dispatchClientNotification({
          session_id: 'sess_bg_1',
          kind: 'question',
          session_title: 'Build Release',
          title: 'Choose deploy target',
          body: 'prod or staging',
          request_id: 'q_100',
        });
      });
      expect(createdNotifications).toHaveLength(1);
      expect(createdNotifications[0].title).toContain('Build Release');
      expect(createdNotifications[0].options?.body).toContain('Choose deploy target');
      expect(useNexStore.getState().notificationToasts).toHaveLength(1);

      // Clicking notification switches to the session
      act(() => {
        createdNotifications[0].onclick?.();
      });
      expect(selectSpy).toHaveBeenCalledWith('sess_bg_1');
      expect(useNexStore.getState().activeSessionId).toBe('sess_bg_1');

      // 3. With notify_only_background=false, even focused active session notifies
      act(() => {
        useNexStore.setState((s) => ({
          preferences: { ...s.preferences, notify_only_background: false },
          notificationToasts: [],
        }));
      });
      await act(async () => {
        await useNexStore.getState().dispatchClientNotification({
          session_id: 'sess_bg_1',
          kind: 'task_completed',
          session_title: 'Build Release',
          request_id: 'c_101',
        });
      });
      expect(createdNotifications).toHaveLength(2);

      // 4. Master & per-category opt-out flags suppress delivery
      act(() => {
        useNexStore.setState((s) => ({
          preferences: { ...s.preferences, notify_on_complete: false, notify_on_input: false },
        }));
      });
      await act(async () => {
        await useNexStore.getState().dispatchClientNotification({
          session_id: 'sess_bg_1',
          kind: 'task_completed',
          request_id: 'c_102',
        });
        await useNexStore.getState().dispatchClientNotification({
          session_id: 'sess_bg_1',
          kind: 'approval',
          tool_name: 'bash',
          request_id: 'a_103',
        });
      });
      expect(createdNotifications).toHaveLength(2);

      // 5. In-app toast click navigates to toast session and dismisses toast
      act(() => {
        useNexStore.setState({ activeSessionId: 'sess_active', mainView: 'workspace' });
        useNexStore.getState().pushNotificationToast({
          kind: 'plan_approval',
          sessionId: 'sess_bg_1',
          title: 'Plan ready',
          body: 'Review plan',
        });
      });
      render(<App />);
      const toastEl = screen.getByTestId('in-app-notification-toast');
      fireEvent.click(toastEl);
      expect(selectSpy).toHaveBeenLastCalledWith('sess_bg_1');
      expect(useNexStore.getState().notificationToasts).toHaveLength(0);
    } finally {
      (globalThis as { Notification?: unknown }).Notification = origNotif;
      document.hasFocus = origHasFocus;
      if (origVisDesc) {
        Object.defineProperty(document, 'visibilityState', origVisDesc);
      }
      useNexStore.setState({ notificationToasts: [], selectSession: origSelect });
    }
  });

  it('renders Notifications settings in GeneralSettingsSection with permission request, toggles, and test button', async () => {
    const origNotif = (globalThis as { Notification?: unknown }).Notification;
    const origRefresh = useNexStore.getState().refreshInitialData;
    const reqPermSpy = vi.fn(async () => {
      MockNotification.permission = 'granted';
      return 'granted' as NotificationPermission;
    });
    const shownTests: string[] = [];
    class MockNotification {
      static permission: NotificationPermission = 'default';
      static requestPermission = reqPermSpy;
      onclick: (() => void) | null = null;
      close = vi.fn();
      constructor(title: string) {
        shownTests.push(title);
      }
    }
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(
      async () => new Response(JSON.stringify({ ok: true }), { status: 200 }),
    );
    const refreshSpy = vi.fn(async () => {});

    try {
      (globalThis as { Notification?: unknown }).Notification = MockNotification;
      useNexStore.setState({
        authEnabled: false,
        authenticated: true,
        serverConnectRequired: false,
        locale: 'en-US',
        mainView: 'settings',
        settingsSection: 'general',
        hostInfo: null,
        preferences: {
          theme: 'light',
          code_theme: 'github-light',
          font_family: 'Inter',
          mono_font_family: 'monospace',
          expand_thinking: false,
          auto_archive_after_days: 30,
          notify_enabled: true,
          notify_on_complete: true,
          notify_on_input: true,
          notify_only_background: true,
        },
        refreshInitialData: refreshSpy,
      });

      render(<App />);
      expect(screen.getByTestId('settings-notifications')).toBeTruthy();
      expect(screen.getByTestId('settings-notify-request-btn')).toBeTruthy();

      fireEvent.click(screen.getByTestId('settings-notify-enabled'));
      await waitFor(() => {
        expect(fetchSpy).toHaveBeenCalledWith(
          expect.stringContaining('/api/v1/preferences'),
          expect.objectContaining({
            method: 'PUT',
            body: expect.stringContaining('"notify_enabled":false'),
          }),
        );
      });

      fireEvent.click(screen.getByTestId('settings-notify-test-btn'));
      await waitFor(() => {
        expect(reqPermSpy).toHaveBeenCalledTimes(1);
        expect(shownTests).toHaveLength(1);
      });
    } finally {
      (globalThis as { Notification?: unknown }).Notification = origNotif;
      useNexStore.setState({ mainView: 'workspace', refreshInitialData: origRefresh });
      fetchSpy.mockRestore();
    }
  });

  it('renders Remote Access settings, saves config, renders offline SVG QR code, and revokes paired devices', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method || 'GET').toUpperCase();
      if (url.endsWith('/api/v1/remote/status')) {
        return new Response(
          JSON.stringify({
            status: {
              node_id: 'nex_studio_abc123',
              fingerprint: 'a'.repeat(64),
              short_fingerprint: 'aaaa:aaaa:aaaa:aaaa',
              enabled: true,
              host_name: 'Studio Mac',
              relay_url: 'https://nex.tylinux.cn',
              relay_connected: true,
              direct_urls: ['https://studio.tailnet.ts.net'],
              lan_urls: ['http://192.168.1.50:8080'],
              active_tunnels: 1,
              system_tailscale: {
                available: false,
                running: false,
                backend_state: 'not_installed',
              },
              tsnet: {
                platform_supported: true,
                installed: false,
                running: false,
                state: 'not_installed',
              },
              config: {
                enabled: true,
                host_name: 'Studio Mac',
                relay_url: 'https://nex.tylinux.cn',
                direct_urls: ['https://studio.tailnet.ts.net'],
                prefer_lan: true,
                tsnet_enabled: false,
              },
            },
            paired_device_count: 1,
          }),
          { status: 200 },
        );
      }
      if (url.endsWith('/api/v1/remote/devices') && method === 'GET') {
        return new Response(
          JSON.stringify({
            devices: [
              {
                id: 'dev_iphone_1',
                name: 'iPhone 16 Pro',
                platform: 'ios',
                pubkey_fp: 'bbbbccccddddeeeeffff111122223333',
                created_at: Date.now() - 60_000,
                last_seen_at: Date.now() - 5_000,
                last_transport: 'relay',
                revoked_at: 0,
              },
            ],
          }),
          { status: 200 },
        );
      }
      if (url.endsWith('/api/v1/remote/config') && method === 'PUT') {
        return new Response(
          JSON.stringify({
            node_id: 'nex_studio_abc123',
            fingerprint: 'a'.repeat(64),
            short_fingerprint: 'aaaa:aaaa:aaaa:aaaa',
            enabled: true,
            host_name: 'Updated Mac',
            relay_url: 'https://relay.custom.cn',
            relay_connected: true,
            direct_urls: [],
            lan_urls: ['http://192.168.1.50:8080'],
            active_tunnels: 1,
            config: {
              enabled: true,
              host_name: 'Updated Mac',
              relay_url: 'https://relay.custom.cn',
              direct_urls: [],
              prefer_lan: true,
            },
          }),
          { status: 200 },
        );
      }
      if (url.endsWith('/api/v1/remote/pair/start') && method === 'POST') {
        return new Response(
          JSON.stringify({
            pair_uri: 'nex://pair?v=1&node=nex_studio_abc123&fp=' + 'a'.repeat(64) + '&token=one_time_tok',
            token: 'one_time_tok',
            expires_at: Date.now() + 300_000,
            node_id: 'nex_studio_abc123',
            fingerprint: 'a'.repeat(64),
            short_fingerprint: 'aaaa:aaaa:aaaa:aaaa',
            host_name: 'Studio Mac',
          }),
          { status: 200 },
        );
      }
      if (url.includes('/api/v1/remote/devices/dev_iphone_1') && method === 'DELETE') {
        return new Response(JSON.stringify({ ok: true, device_id: 'dev_iphone_1' }), { status: 200 });
      }
      if (url.endsWith('/api/v1/remote/tsnet/probe')) {
        return new Response(
          JSON.stringify({
            system_tailscale: {
              available: true,
              running: true,
              backend_state: 'Running',
              dns_name: 'studio-mac.ts.net',
            },
            tsnet: {
              platform_supported: true,
              installed: false,
              running: false,
              state: 'not_installed',
            },
          }),
          { status: 200 },
        );
      }
      return new Response(JSON.stringify({ ok: true }), { status: 200 });
    });

    try {
      useNexStore.setState({
        authEnabled: false,
        authenticated: true,
        serverConnectRequired: false,
        locale: 'en-US',
        mainView: 'settings',
        settingsSection: 'remote',
      });

      render(<App />);

      await waitFor(() => {
        expect(screen.getByTestId('remote-node-id').textContent).toBe('nex_studio_abc123');
        expect(screen.getByTestId('remote-revoke-device-dev_iphone_1')).toBeTruthy();
      });

      fireEvent.change(screen.getByTestId('remote-relay-url-input'), {
        target: { value: 'https://relay.custom.cn' },
      });
      fireEvent.click(screen.getByTestId('remote-save-btn'));
      await waitFor(() => {
        expect(fetchSpy).toHaveBeenCalledWith(
          expect.stringContaining('/api/v1/remote/config'),
          expect.objectContaining({
            method: 'PUT',
            body: expect.stringContaining('https://relay.custom.cn'),
          }),
        );
      });

      fireEvent.click(screen.getByTestId('remote-pair-start-btn'));
      await waitFor(() => {
        const svg = screen.getByTestId('remote-pair-qr-svg');
        expect(svg.querySelector('path')?.getAttribute('d')).toBeTruthy();
      });

      fireEvent.click(screen.getByTestId('remote-revoke-device-dev_iphone_1'));
      await waitFor(() => {
        expect(screen.queryByTestId('remote-revoke-device-dev_iphone_1')).toBeNull();
      });

      expect(screen.getByTestId('remote-tailscale-card')).toBeTruthy();
      fireEvent.click(screen.getByTestId('remote-tsnet-probe-btn'));
      await waitFor(() => {
        expect(screen.getByTestId('remote-system-tailscale-badge').textContent).toContain('studio-mac.ts.net');
      });
    } finally {
      useNexStore.setState({ mainView: 'workspace', settingsSection: 'general' });
      fetchSpy.mockRestore();
    }
  });

  it('shows paired hosts and opens QRScannerModal in ServerConnectGate to submit nex://pair URI', async () => {
    const pairSpy = vi.fn(async () => ({ ok: true }));
    const connectPairedSpy = vi.fn(async () => ({ ok: true }));
    const removePairedSpy = vi.fn(async () => ({ ok: true }));
    const origPair = useNexStore.getState().pairWithURI;
    const origConnectPaired = useNexStore.getState().connectPairedHost;
    const origRemovePaired = useNexStore.getState().removePairedHost;

    try {
      useNexStore.setState({
        authEnabled: false,
        authenticated: true,
        serverConnectRequired: true,
        activeServerUrl: '',
        pendingPairInviteURI: '',
        locale: 'en-US',
        hostInfo: {
          platform: 'ios',
          form_factor: 'mobile',
          version: '0.3.0',
          connection_mode: 'unconfigured',
          server_url: '',
          paired_hosts: [
            {
              node_id: 'nex_home_001',
              host_name: 'Home Server',
              fingerprint: 'b'.repeat(64),
              device_id: 'dev_1',
              last_transport: 'relay',
              paired_at: 1700000000000,
            },
          ],
          supports_local_mode: false,
          notifications_supported: true,
          recent_servers: [],
        },
        pairWithURI: pairSpy,
        connectPairedHost: connectPairedSpy,
        removePairedHost: removePairedSpy,
      });

      render(<App />);

      expect(screen.getByTestId('paired-hosts-list')).toBeTruthy();
      fireEvent.click(screen.getByTestId('paired-host-connect-nex_home_001'));
      expect(connectPairedSpy).toHaveBeenCalledWith('nex_home_001');

      fireEvent.click(screen.getByTestId('server-pair-qr-btn'));
      expect(screen.getByTestId('qr-scanner-modal')).toBeTruthy();

      const pairURI = `nex://pair?v=1&node=nex_new_1&fp=${'c'.repeat(64)}&token=tok_pair_9`;
      fireEvent.change(screen.getByTestId('qr-manual-uri-input'), { target: { value: pairURI } });
      fireEvent.change(screen.getByTestId('qr-device-name-input'), { target: { value: 'iPad Pro' } });
      fireEvent.click(screen.getByTestId('qr-manual-submit-btn'));

      await waitFor(() => {
        expect(pairSpy).toHaveBeenCalledWith(pairURI, 'iPad Pro');
      });
    } finally {
      useNexStore.setState({
        serverConnectRequired: false,
        hostInfo: null,
        pendingPairInviteURI: '',
        pairWithURI: origPair,
        connectPairedHost: origConnectPaired,
        removePairedHost: origRemovePaired,
      });
    }
  });

  it('renders inline Question Card without modal overlay and supports pager + custom answer + keyboard nav', () => {
    const origResolve = useNexStore.getState().resolveUIPrompt;
    const resolveSpy = vi.fn();

    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        mainView: 'workspace',
        previewImage: null,
        locale: 'en-US',
        activeSessionId: 'sess_q_1',
        sessions: [
          {
            id: 'sess_q_1',
            runner_id: 'r_local',
            depth: 0,
            title: 'Question Session',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'interactive',
            status: 'running',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 2000,
            last_active_at: 2000,
          },
        ],
        resolveUIPrompt: resolveSpy,
        streams: {
          sess_q_1: {
            lastSeq: 1,
            running: true,
            items: [
              {
                id: 'u_q_1',
                seq: 1,
                role: 'user',
                text: 'Test question',
                thinking: '',
                toolCalls: [],
              },
            ],
            latestTelemetry: null,
            turnTelemetries: [],
            uiBadges: {},
            pendingApproval: null,
            pendingUIPrompt: {
              requestId: 'uip_card_1',
              sessionId: 'sess_q_1',
              runnerId: 'r_local',
              kind: 'select',
              header: 'Testing',
              title: 'How do you prefer to work?',
              questions: [
                {
                  header: 'Testing',
                  question: 'How do you prefer to work?',
                  kind: 'select',
                  options: [
                    { label: 'Plan first', description: 'Propose steps before coding' },
                    { label: 'Dive right in', description: 'Start coding immediately' },
                  ],
                },
                {
                  header: 'Scope',
                  question: 'Which target?',
                  kind: 'select',
                  options: [{ label: 'Web' }, { label: 'Desktop' }],
                },
              ],
            },
            todos: [],
            goals: [],
          },
        },
      });

      render(<App />);

      const card = screen.getByTestId('inline-question-card');
      expect(card).toBeTruthy();
      expect(card.closest('.nex-dialog-overlay')).toBeNull();
      expect(document.querySelector('.nex-dialog-overlay')).toBeNull();
      expect(screen.getByTestId('question-card-header').textContent).toBe('Testing');
      expect(screen.getByTestId('question-card-title').textContent).toBe('How do you prefer to work?');
      expect(screen.getByTestId('question-pager-indicator').textContent).toBe('1 / 2');
      expect(screen.getByTestId('question-option-0').textContent).toContain('Plan first');
      expect(screen.getByTestId('question-option-0').textContent).toContain('Propose steps before coding');

      // Keyboard select first option via Space
      fireEvent.keyDown(card, { key: ' ' });
      expect(screen.getByTestId('question-option-0').getAttribute('aria-selected')).toBe('true');

      // Advance to page 2 and type custom answer
      fireEvent.click(screen.getByTestId('question-pager-next'));
      expect(screen.getByTestId('question-pager-indicator').textContent).toBe('2 / 2');
      expect(screen.getByTestId('question-card-header').textContent).toBe('Scope');
      const customInput = screen.getByTestId('question-custom-input') as HTMLInputElement;
      fireEvent.change(customInput, { target: { value: 'Mobile App' } });

      fireEvent.click(screen.getByTestId('question-submit-btn'));
      expect(resolveSpy).toHaveBeenCalledWith(
        'uip_card_1',
        true,
        JSON.stringify({
          answers: [
            { question: 'How do you prefer to work?', answer: 'Plan first' },
            { question: 'Which target?', answer: 'Mobile App' },
          ],
        }),
      );

      fireEvent.click(screen.getByTestId('question-dismiss-btn'));
      expect(resolveSpy).toHaveBeenCalledWith('uip_card_1', false, '', true);
    } finally {
      useNexStore.setState({
        resolveUIPrompt: origResolve,
      });
    }
  });

  it('auto-grows composer textarea height cleanly on multiline input up to 200px', () => {
    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      mainView: 'workspace',
      activeSessionId: 'sess_comp_1',
      sessions: [
        {
          id: 'sess_comp_1',
          runner_id: 'r_local',
          depth: 0,
          title: 'Composer Test',
          workspace_path: '/tmp/ws',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'idle',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: 1000,
          updated_at: 2000,
          last_active_at: 2000,
        },
      ],
      streams: {
        sess_comp_1: {
          lastSeq: 1,
          running: false,
          items: [
            {
              id: 'u_comp_1',
              seq: 1,
              role: 'user',
              text: 'Hello',
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
    });

    render(<App />);
    const textarea = screen.getByTestId('composer-input') as HTMLTextAreaElement;

    let mockScrollHeight = 88;
    Object.defineProperty(textarea, 'scrollHeight', {
      configurable: true,
      get: () => mockScrollHeight,
    });

    fireEvent.change(textarea, { target: { value: 'Line 1\nLine 2\nLine 3' } });
    expect(textarea.style.height).toBe('88px');
    expect(textarea.style.overflowY).toBe('hidden');

    mockScrollHeight = 280;
    fireEvent.change(textarea, { target: { value: 'Line 1\nLine 2\nLine 3\nLine 4\nLine 5\nLine 6\nLine 7\nLine 8' } });
    expect(textarea.style.height).toBe('200px');
    expect(textarea.style.overflowY).toBe('auto');
  });

  it('prevents top-right status controls from overlapping Progress 5/5 when Progress is first', () => {
    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      mainView: 'workspace',
      activeSessionId: 'sess_prog_1',
      gitSummary: null,
      sessions: [
        {
          id: 'sess_prog_1',
          runner_id: 'r_local',
          depth: 0,
          title: 'Progress Only',
          workspace_path: '/tmp/ws',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'idle',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: 1000,
          updated_at: 2000,
          last_active_at: 2000,
        },
      ],
      streams: {
        sess_prog_1: {
          lastSeq: 1,
          running: false,
          items: [
            {
              id: 'u_prog_1',
              seq: 1,
              role: 'user',
              text: 'Check progress',
              thinking: '',
              toolCalls: [],
            },
          ],
          latestTelemetry: null,
          turnTelemetries: [],
          uiBadges: {},
          pendingApproval: null,
          pendingUIPrompt: null,
          goals: [],
          todos: [
            { content: 'Task 1', status: 'completed' },
            { content: 'Task 2', status: 'completed' },
            { content: 'Task 3', status: 'completed' },
            { content: 'Task 4', status: 'completed' },
            { content: 'Task 5', status: 'completed' },
          ],
        },
      },
    });

    render(<App />);
    const progressSection = screen.getByTestId('status-panel-progress-section');
    const progressHeader = screen.getByTestId('status-panel-progress-header');
    expect(progressSection.className).not.toContain('border-t');
    expect(progressHeader.className).toContain('pr-14');
    expect(progressHeader.textContent).toContain('5/5');
  });

  it('displays runner badges per project in ProjectSidebar and supports runner version display & removal in Runners settings', async () => {
    const origFetch = globalThis.fetch;
    const deletedIds: string[] = [];
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes('/api/v1/runners/') && init?.method === 'DELETE') {
        const id = decodeURIComponent(url.split('/api/v1/runners/')[1]);
        deletedIds.push(id);
        return new Response(JSON.stringify({ deleted: id }), { status: 200 });
      }
      if (url.endsWith('/api/v1/runners')) {
        const remaining = [
          {
            id: 'run_mac',
            name: 'macM4',
            hostname: 'MacMini.local',
            os: 'darwin',
            arch: 'arm64',
            shell: '/bin/zsh',
            version: '0.1.2',
            tags: [],
            status: 'online',
            last_seen_at: 1000,
            synced_config_rev: 146,
            workspace_default: '/Users/tylinux',
          },
          {
            id: 'run_win_stale',
            name: 'WindowsT2',
            hostname: 'WorkStation',
            os: 'windows',
            arch: 'amd64',
            shell: 'bash.exe',
            version: 'v0.1.1',
            tags: [],
            status: 'offline',
            last_seen_at: 500,
            synced_config_rev: 2,
            workspace_default: 'C:\\Users\\tylinux',
          },
        ].filter((r) => !deletedIds.includes(r.id));
        return new Response(JSON.stringify(remaining), { status: 200 });
      }
      return new Response(JSON.stringify([]), { status: 200 });
    }) as typeof fetch;

    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'workspace',
        sidebarVisible: true,
        runners: [
          {
            id: 'run_mac',
            name: 'macM4',
            hostname: 'MacMini.local',
            os: 'darwin',
            arch: 'arm64',
            shell: '/bin/zsh',
            version: '0.1.2',
            tags: [],
            status: 'online',
            last_seen_at: 1000,
            synced_config_rev: 146,
            workspace_default: '/Users/tylinux',
          },
          {
            id: 'run_remote',
            name: 'RemoteBuilder',
            hostname: 'RemoteBuilder',
            os: 'linux',
            arch: 'amd64',
            shell: '/bin/bash',
            version: 'v0.1.2',
            tags: [],
            status: 'online',
            last_seen_at: 1000,
            synced_config_rev: 197,
            workspace_default: '/home/tylinux/Developer',
          },
          {
            id: 'run_win_stale',
            name: 'WindowsT2',
            hostname: 'WorkStation',
            os: 'windows',
            arch: 'amd64',
            shell: 'bash.exe',
            version: 'v0.1.1',
            tags: [],
            status: 'offline',
            last_seen_at: 500,
            synced_config_rev: 2,
            workspace_default: 'C:\\Users\\tylinux',
          },
        ],
        sessions: [
          {
            id: 'sess_mac_1',
            runner_id: 'run_mac',
            depth: 0,
            title: 'Mac Task',
            workspace_path: '/Users/tylinux/Projects/Nex-v3',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'interactive',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 2000,
            last_active_at: 2000,
          },
          {
            id: 'sess_remote_1',
            runner_id: 'run_remote',
            depth: 0,
            title: 'Remote Task',
            workspace_path: '/home/tylinux/Developer/Nex-v3',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'interactive',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 2000,
            last_active_at: 2000,
          },
        ],
      });

      const { rerender } = render(<App />);

      const macBadge = screen.getByTestId('project-runner-badge-run_mac::/Users/tylinux/Projects/Nex-v3');
      const remoteBadge = screen.getByTestId('project-runner-badge-run_remote::/home/tylinux/Developer/Nex-v3');
      expect(macBadge.textContent).toContain('macM4');
      expect(remoteBadge.textContent).toContain('RemoteBuilder');

      act(() => {
        useNexStore.setState({ mainView: 'settings', settingsSection: 'runners' });
      });
      rerender(<App />);

      expect(screen.getByTestId('runner-version-run_mac').textContent).toBe('v0.1.2');
      expect(screen.getByTestId('runner-version-run_win_stale').textContent).toBe('v0.1.1');

      const removeBtn = screen.getByTestId('remove-runner-run_win_stale');
      fireEvent.click(removeBtn);
      expect(screen.getByTestId('confirm-delete-runner-modal')).toBeTruthy();
      expect(deletedIds).toEqual([]);

      await act(async () => {
        fireEvent.click(screen.getByTestId('confirm-delete-runner-btn'));
      });

      expect(deletedIds).toEqual(['run_win_stale']);
    } finally {
      useNexStore.setState({
        appUpdate: null,
        appUpdateError: '',
        checkingAppUpdate: false,
        updateDownload: null,
        updateBannerDismissed: false,
      });
      globalThis.fetch = origFetch;
    }
  });

  it('persists Settings view/section across reload, caches composer drafts per session, and progressively renders long conversations', async () => {
    // 1. Settings view + section round-trips via URL query & storage
    persistViewState('settings', 'runners');
    expect(window.location.search).toContain('view=settings');
    expect(window.location.search).toContain('section=runners');
    expect(readPersistedViewState()).toEqual({ mainView: 'settings', settingsSection: 'runners' });

    persistViewState('workspace', 'runners');
    expect(window.location.search).not.toContain('view=settings');
    expect(readPersistedViewState()).toEqual({ mainView: 'workspace', settingsSection: 'runners' });

    // 2. Per-session & draft composer localStorage caching
    persistComposerDraft('', 'draft prompt unsent');
    persistComposerDraft('sess_alpha', 'alpha follow-up draft');
    expect(readPersistedComposerDraft('')).toBe('draft prompt unsent');
    expect(readPersistedComposerDraft('sess_alpha')).toBe('alpha follow-up draft');

    useNexStore.setState({ activeSessionId: 'sess_alpha', composerText: 'alpha follow-up draft' });
    act(() => {
      useNexStore.getState().setComposerText('updated alpha draft');
    });
    expect(readPersistedComposerDraft('sess_alpha')).toBe('updated alpha draft');

    act(() => {
      useNexStore.getState().startNewSession();
    });
    expect(useNexStore.getState().composerText).toBe('draft prompt unsent');

    // Clean up persisted drafts
    persistComposerDraft('', '');
    persistComposerDraft('sess_alpha', '');
    expect(readPersistedComposerDraft('')).toBe('');
    expect(readPersistedComposerDraft('sess_alpha')).toBe('');

    // 3. Tail-window progressive rendering on long conversations (> 24 turns)
    const manyItems = Array.from({ length: 30 }, (_, idx) => ({
      id: `turn_${idx + 1}`,
      seq: idx + 1,
      role: (idx % 2 === 0 ? 'user' : 'assistant') as 'user' | 'assistant',
      text: `Turn message #${idx + 1}`,
      thinking: '',
      toolCalls: [],
    }));

    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: 'sess_long_hist',
      sessions: [
        {
          id: 'sess_long_hist',
          runner_id: 'run_1',
          depth: 0,
          title: 'Long History Session',
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
        sess_long_hist: {
          lastSeq: 30,
          running: false,
          items: manyItems,
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
    // Latest turn is rendered immediately; oldest 6 turns are deferred behind the load-earlier bar
    expect(screen.getByText('Turn message #30')).toBeTruthy();
    expect(screen.queryByText('Turn message #1')).toBeNull();
    const loadEarlierBtn = screen.getByTestId('load-earlier-turns-btn');
    expect(loadEarlierBtn.textContent).toContain('6');

    fireEvent.click(loadEarlierBtn);
    await waitFor(() => expect(screen.getByText('Turn message #1')).toBeTruthy());
    expect(screen.queryByTestId('load-earlier-turns-bar')).toBeNull();
  });

  it('bounds turn-tick-rail within max height and increases density / samples cleanly for long conversations', () => {
    const makeItems = (n: number) =>
      Array.from({ length: n }, (_, i) => ({
        id: `tick_t_${i + 1}`,
        seq: i + 1,
        role: (i % 2 === 0 ? 'user' : 'assistant') as 'user' | 'assistant',
        text: `Turn ${i + 1}`,
        thinking: '',
        toolCalls: [],
      }));

    // 1. Short conversation (6 turns): normal 16px slot height and 2px gap
    const shortLayout = computeTurnTickLayout(makeItems(6), 'tick_t_6', 360);
    expect(shortLayout.entries).toHaveLength(6);
    expect(shortLayout.slotHeightPx).toBe(16);
    expect(shortLayout.gapPx).toBe(2);
    expect(shortLayout.totalHeightPx).toBeLessThanOrEqual(360);

    // 2. Long conversation (60 turns): tighter density (6px per tick, 0 gap) fits all 60 within 360px
    const denseLayout = computeTurnTickLayout(makeItems(60), 'tick_t_60', 360);
    expect(denseLayout.entries).toHaveLength(60);
    expect(denseLayout.slotHeightPx).toBe(6);
    expect(denseLayout.gapPx).toBe(0);
    expect(denseLayout.totalHeightPx).toBeLessThanOrEqual(360);

    // 3. Ultra-long conversation (200 turns): sampled cleanly to fit <= 360px, keeping first, active, and last
    const hugeItems = makeItems(200);
    const sampledLayout = computeTurnTickLayout(hugeItems, 'tick_t_77', 360);
    expect(sampledLayout.entries.length).toBeLessThanOrEqual(90);
    expect(sampledLayout.slotHeightPx).toBe(4);
    expect(sampledLayout.totalHeightPx).toBeLessThanOrEqual(360);
    expect(sampledLayout.entries[0].idx).toBe(0);
    expect(sampledLayout.entries[sampledLayout.entries.length - 1].idx).toBe(199);
    expect(sampledLayout.entries.some((e) => e.item.id === 'tick_t_77')).toBe(true);

    // 4. DOM rendering for 60 turns uses tightened density and keeps turn-tick-0 clickable
    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      mainView: 'workspace',
      activeSessionId: 'sess_dense_ticks',
      sessions: [
        {
          id: 'sess_dense_ticks',
          runner_id: 'r_local',
          depth: 0,
          title: 'Dense Ticks',
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
        sess_dense_ticks: {
          lastSeq: 60,
          running: false,
          items: makeItems(60),
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
    const rail = screen.getByTestId('turn-tick-rail');
    expect(Number(rail.getAttribute('data-tick-slot-height'))).toBeLessThan(16);
    expect(rail.style.maxHeight).toBe('calc(100% - 24px)');
    expect(screen.getByTestId('turn-tick-0')).toBeTruthy();
    expect(screen.getByTestId('turn-tick-59')).toBeTruthy();
  });

  it('places session status dots before conversation titles and renders CloudOff only on offline runner badges', () => {
    const now = Date.now();
    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      mainView: 'workspace',
      sidebarVisible: true,
      activeSessionId: 'sess_run',
      runners: [
        {
          id: 'run_online',
          name: 'macM4',
          hostname: 'macM4',
          os: 'darwin',
          arch: 'arm64',
          shell: 'zsh',
          tags: [],
          synced_config_rev: 1,
          status: 'online',
          workspace_default: '/tmp/proj-online',
          version: '0.3.0',
          last_seen_at: now,
        },
        {
          id: 'run_offline',
          name: 'WindowsT2',
          hostname: 'WindowsT2',
          os: 'windows',
          arch: 'amd64',
          shell: 'powershell',
          tags: [],
          synced_config_rev: 1,
          status: 'offline',
          workspace_default: '/tmp/proj-offline',
          version: '0.3.0',
          last_seen_at: now - 60_000,
        },
      ],
      sessions: [
        {
          id: 'sess_run',
          runner_id: 'run_online',
          depth: 0,
          title: 'Running Task Title',
          workspace_path: '/tmp/proj-online',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'running',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: now - 10_000,
          updated_at: now - 1_000,
          last_active_at: now - 1_000,
        },
        {
          id: 'sess_await',
          runner_id: 'run_online',
          depth: 0,
          title: 'Awaiting Input Title',
          workspace_path: '/tmp/proj-online',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'running',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: now - 20_000,
          updated_at: now - 2_000,
          last_active_at: now - 2_000,
        },
        {
          id: 'sess_err',
          runner_id: 'run_offline',
          depth: 0,
          title: 'Errored Task Title',
          workspace_path: '/tmp/proj-offline',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'error',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: now - 30_000,
          updated_at: now - 3_000,
          last_active_at: now - 3_000,
        },
        {
          id: 'sess_idle',
          runner_id: 'run_offline',
          depth: 0,
          title: 'Idle Task Title',
          workspace_path: '/tmp/proj-offline',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'idle',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: now - 40_000,
          updated_at: now - 4_000,
          last_active_at: now - 4_000,
        },
      ],
      streams: {
        sess_run: {
          ...emptyStreamState(),
          running: true,
        },
        sess_await: {
          ...emptyStreamState(),
          running: true,
          pendingUIPrompt: {
            requestId: 'uip_await',
            sessionId: 'sess_await',
            runnerId: 'run_online',
            kind: 'input',
            title: 'Confirm?',
          },
        },
      },
    });

    render(<App />);

    // Running spinner appears BEFORE the title element in DOM order (left side) as a 2x2 4-dot loader
    const runRow = screen.getByTestId('session-row-sess_run');
    const runDot = screen.getByTestId('session-status-dot-sess_run');
    const runTitle = Array.from(runRow.querySelectorAll('span')).find((el) => el.textContent === 'Running Task Title')!;
    expect(runTitle).toBeTruthy();
    expect(runDot.getAttribute('data-status')).toBe('running');
    expect(runDot.className).toContain('nex-running-dots');
    expect(runDot.children).toHaveLength(4);
    expect(runRow.contains(runDot)).toBe(true);
    expect(runDot.compareDocumentPosition(runTitle) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    // Awaiting input dot uses amber pulse
    const awaitDot = screen.getByTestId('session-status-dot-sess_await');
    expect(awaitDot.getAttribute('data-status')).toBe('awaiting');
    expect(awaitDot.className).toContain('bg-amber-500');

    // Errored session shows red dot
    const errDot = screen.getByTestId('session-status-dot-sess_err');
    expect(errDot.getAttribute('data-status')).toBe('error');
    expect(errDot.className).toContain('bg-destructive');

    // Idle session renders no dot
    expect(screen.queryByTestId('session-status-dot-sess_idle')).toBeNull();

    // Online runner badge has NO green dot and NO offline cloud icon
    const onlineBadge = screen.getByTestId('project-runner-badge-run_online::/tmp/proj-online');
    expect(onlineBadge.textContent).toContain('macM4');
    expect(onlineBadge.querySelector('.bg-success')).toBeNull();
    expect(screen.queryByTestId('project-runner-offline-run_online::/tmp/proj-online')).toBeNull();

    // Offline runner badge displays CloudOff icon
    const offlineBadge = screen.getByTestId('project-runner-badge-run_offline::/tmp/proj-offline');
    expect(offlineBadge.textContent).toContain('WindowsT2');
    expect(screen.getByTestId('project-runner-offline-run_offline::/tmp/proj-offline')).toBeTruthy();
  });

  it('inserts new sessions optimistically, shows steady unread blue dot on background finish, and omits sidebar delete button', async () => {
    const origFetch = globalThis.fetch;
    const origSendEnvelope = wsClient.sendEnvelope;
    const sendSpy = vi.fn();
    wsClient.sendEnvelope = sendSpy;
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => [],
    }) as unknown as typeof fetch;

    try {
      const now = Date.now();
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        locale: 'en-US',
        mainView: 'workspace',
        sidebarVisible: true,
        activeSessionId: '',
        unreadSessionIds: {},
        selectedRunnerId: 'run_opt',
        selectedWorkspacePath: '/tmp/proj-opt',
        selectedProviderId: 'p1',
        selectedModelId: 'm1',
        runners: [
          {
            id: 'run_opt',
            name: 'macM4',
            hostname: 'macM4',
            os: 'darwin',
            arch: 'arm64',
            shell: 'zsh',
            tags: [],
            synced_config_rev: 1,
            status: 'online',
            workspace_default: '/tmp/proj-opt',
            version: '0.3.0',
            last_seen_at: now,
          },
        ],
        providers: [
          {
            id: 'p1',
            name: 'P1',
            driver: 'anthropic',
            base_url: '',
            api_key: '',
            proxy_mode: 'direct',
            enabled: true,
          },
        ],
        models: [
          {
            id: 'm1',
            provider_id: 'p1',
            display_name: 'Model 1',
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
            id: 'sess_bg',
            runner_id: 'run_opt',
            depth: 0,
            title: 'Background Job',
            workspace_path: '/tmp/proj-opt',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'interactive',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: now - 5_000,
            updated_at: now - 5_000,
            last_active_at: now - 5_000,
          },
        ],
        streams: {},
      });

      render(<App />);

      // 1. Sending a prompt from draft inserts the new session immediately into the sidebar BEFORE any reply
      act(() => {
        useNexStore.getState().sendPromptOrSteer('Check sidebar immediately');
      });

      const createdId = useNexStore.getState().activeSessionId;
      expect(createdId).toBeTruthy();
      expect(useNexStore.getState().sessions.some((s) => s.id === createdId)).toBe(true);

      const newRow = screen.getByTestId(`session-row-${createdId}`);
      expect(newRow.textContent).toContain('Check sidebar immediately');
      const newDot = screen.getByTestId(`session-status-dot-${createdId}`);
      expect(newDot.getAttribute('data-status')).toBe('running');
      expect(newDot.className).toContain('nex-running-dots');
      expect(newDot.children).toHaveLength(4);

      // 3. Sidebar row has Pin / Rename / Archive (3 action buttons), and NO Delete button
      expect(newRow.querySelectorAll('button')).toHaveLength(3);
      expect(newRow.querySelector('button[title="Delete task"]')).toBeNull();
      expect(newRow.querySelector('button[title="Archive task"]')).toBeTruthy();

      // 2. Non-active session completed while user is on another session -> steady (non-pulsing) blue unread dot
      act(() => {
        useNexStore.setState((prev) => ({
          unreadSessionIds: { ...prev.unreadSessionIds, sess_bg: true },
        }));
      });

      const unreadDot = screen.getByTestId('session-status-dot-sess_bg');
      expect(unreadDot.getAttribute('data-status')).toBe('unread');
      expect(unreadDot.className).toContain('bg-brand-blue');
      expect(unreadDot.className).not.toContain('animate-pulse');

      // Clicking the unread session clears the unread dot immediately
      await act(async () => {
        fireEvent.click(screen.getByTestId('session-row-sess_bg'));
      });
      expect(useNexStore.getState().unreadSessionIds.sess_bg).toBeUndefined();
      expect(screen.queryByTestId('session-status-dot-sess_bg')).toBeNull();
    } finally {
      wsClient.sendEnvelope = origSendEnvelope;
      globalThis.fetch = origFetch;
      useNexStore.setState({ unreadSessionIds: {} });
    }
  });

  it('isolates thinkingEffort per conversation and preserves draft model + effort across switches', async () => {
    const origFetch = globalThis.fetch;
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => [],
    }) as unknown as typeof fetch;

    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        locale: 'en-US',
        mainView: 'workspace',
        activeSessionId: 'sess_iso_a',
        selectedRunnerId: 'run_iso',
        selectedProviderId: 'p1',
        selectedModelId: 'model-a',
        draftProviderId: 'p1',
        draftModelId: 'model-b',
        draftThinkingEffort: 'high',
        thinkingEffort: 'medium',
        sessionModels: {},
        sessionEfforts: {},
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
            supports_reasoning: true,
            supports_caching: false,
            reasoning_levels: ['off', 'low', 'medium', 'high'],
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
            supports_reasoning: true,
            supports_caching: false,
            reasoning_levels: ['off', 'low', 'medium', 'high', 'max'],
            pricing: { input_per_1m: 0, output_per_1m: 0, cache_read_per_1m: 0, cache_write_per_1m: 0 },
          },
        ],
        sessions: [
          {
            id: 'sess_iso_a',
            runner_id: 'run_iso',
            depth: 0,
            title: 'Conv A',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'model-a',
            provider_id: 'p1',
            thinking_effort: 'medium',
            permission_mode: 'interactive',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 2000,
            last_active_at: 2000,
          },
          {
            id: 'sess_iso_b',
            runner_id: 'run_iso',
            depth: 0,
            title: 'Conv B',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'model-b',
            provider_id: 'p1',
            thinking_effort: 'max',
            permission_mode: 'interactive',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 3000,
            last_active_at: 3000,
          },
        ],
      });

      render(<App />);

      // Adjust Conv A effort to 'low' -> Conv B and draft effort MUST NOT change!
      act(() => {
        useNexStore.getState().setThinkingEffort('low');
      });
      expect(useNexStore.getState().thinkingEffort).toBe('low');
      expect(useNexStore.getState().draftThinkingEffort).toBe('high');

      // Switch to Conv B -> restores Conv B's own model-b and 'max' effort
      await act(async () => {
        await useNexStore.getState().selectSession('sess_iso_b');
      });
      expect(useNexStore.getState().selectedModelId).toBe('model-b');
      expect(useNexStore.getState().thinkingEffort).toBe('max');

      // Switch back to Conv A -> restores Conv A's 'low' effort
      await act(async () => {
        await useNexStore.getState().selectSession('sess_iso_a');
      });
      expect(useNexStore.getState().selectedModelId).toBe('model-a');
      expect(useNexStore.getState().thinkingEffort).toBe('low');

      // Start new session -> uses draft model-b and draft 'high' (unaffected by Conv A/B edits)
      act(() => {
        useNexStore.getState().startNewSession();
      });
      expect(useNexStore.getState().selectedModelId).toBe('model-b');
      expect(useNexStore.getState().thinkingEffort).toBe('high');
    } finally {
      globalThis.fetch = origFetch;
      useNexStore.setState({ sessionEfforts: {}, draftThinkingEffort: 'high' });
    }
  });

  it('renders runner self-update badges/phases, triggers single & batch upgrades, and shows incompatible banner in composer', async () => {
    const now = Date.now();
    const fetchCalls: Array<{ url: string; method?: string; body?: string }> = [];
    const origFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      fetchCalls.push({ url, method: init?.method, body: typeof init?.body === 'string' ? init.body : undefined });
      if (url.includes('/api/v1/runners/update-all')) {
        return new Response(
          JSON.stringify({
            triggered: [{ runner_id: 'run_req' }, { runner_id: 'run_avail' }],
            skipped: [{ runner_id: 'run_dev', reason: 'dev' }],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        );
      }
      if (url.includes('/api/v1/runners/') && url.endsWith('/update')) {
        return new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      if (url.includes('/api/v1/runners/pairing')) {
        return new Response(JSON.stringify({ token: 'tok', ws_url: 'ws://localhost/ws/runner' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      if (url.includes('/api/v1/runners')) {
        return new Response(JSON.stringify(useNexStore.getState().runners), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      return new Response(JSON.stringify([]), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }) as typeof fetch;

    try {
      const testRunners = [
        {
          id: 'run_req',
          name: 'LegacyWin',
          hostname: 'win-box',
          os: 'windows',
          arch: 'amd64',
          shell: 'powershell',
          tags: [],
          synced_config_rev: 1,
          status: 'online' as const,
          workspace_default: 'C:/work',
          version: '0.1.0',
          last_seen_at: now,
          elevated: true,
          incompatible: true,
          update_status: 'update_required' as const,
          update_reason: 'protocol_version 0 < min 1',
          target_version: '0.2.0',
        },
        {
          id: 'run_avail',
          name: 'ProdLinux',
          hostname: 'linux-box',
          os: 'linux',
          arch: 'amd64',
          shell: 'bash',
          tags: [],
          synced_config_rev: 1,
          status: 'online' as const,
          workspace_default: '/srv/app',
          version: '0.1.9',
          last_seen_at: now,
          update_status: 'update_available' as const,
          target_version: '0.2.0',
          update_phase: 'downloading',
          update_progress: 42,
        },
        {
          id: 'run_dev',
          name: 'LocalDev',
          hostname: 'mac-dev',
          os: 'darwin',
          arch: 'arm64',
          shell: 'zsh',
          tags: [],
          synced_config_rev: 1,
          status: 'online' as const,
          workspace_default: '/tmp/dev',
          version: '0.1.9-dev',
          last_seen_at: now,
          is_dev: true,
          update_status: 'update_available' as const,
          target_version: '0.2.0',
        },
      ];

      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        mainView: 'settings',
        settingsSection: 'runners',
        runners: testRunners,
        selectedRunnerId: 'run_req',
        selectedWorkspacePath: 'C:/work',
        selectedProviderId: 'p1',
        selectedModelId: 'm1',
        providers: [
          {
            id: 'p1',
            name: 'OpenAI',
            driver: 'openai_responses',
            base_url: 'https://api.openai.com',
            api_key: 'sk-test',
            proxy_mode: 'direct',
            enabled: true,
          },
        ],
      });

      render(<App />);

      // 1. SettingsRunners shows Admin badge, Dev badge, downloading phase, required/available banners, and Update All (2 non-dev)
      expect(screen.getByTestId('runner-admin-badge-run_req')).toBeTruthy();
      expect(screen.getByTestId('runner-dev-badge-run_dev')).toBeTruthy();
      expect(screen.getByTestId('runner-update-phase-run_avail').textContent).toContain('42%');
      expect(screen.getByTestId('runner-update-required-run_req')).toBeTruthy();
      expect(screen.getByTestId('runner-update-available-run_dev')).toBeTruthy();
      const updateAllBtn = screen.getByTestId('update-all-runners-btn');
      expect(updateAllBtn.textContent).toContain('2');

      // 2. Clicking Update All posts to /api/v1/runners/update-all
      await act(async () => {
        fireEvent.click(updateAllBtn);
      });
      expect(fetchCalls.some((c) => c.url.includes('/api/v1/runners/update-all') && c.method === 'POST')).toBe(true);

      // 3. Clicking single upgrade on required runner posts immediate + force=true
      await act(async () => {
        fireEvent.click(screen.getByTestId('upgrade-runner-run_req'));
      });
      const singleReq = fetchCalls.find((c) => c.url.includes('/api/v1/runners/run_req/update') && c.method === 'POST');
      expect(singleReq).toBeTruthy();
      expect(JSON.parse(singleReq!.body || '{}')).toMatchObject({ mode: 'immediate', force: true });

      // 4. Switching to workspace with incompatible runner shows ConversationPane banner & upgrade button
      act(() => {
        useNexStore.setState({ mainView: 'workspace', activeSessionId: '' });
      });
      const banner = screen.getByTestId('runner-incompatible-banner');
      expect(banner.textContent).toContain('LegacyWin');
      expect(banner.textContent).toContain('protocol_version 0 < min 1');

      const bannerBtn = screen.getByTestId('banner-upgrade-runner-btn');
      const beforeCount = fetchCalls.filter((c) => c.url.includes('/api/v1/runners/run_req/update')).length;
      await act(async () => {
        fireEvent.click(bannerBtn);
      });
      const afterCount = fetchCalls.filter((c) => c.url.includes('/api/v1/runners/run_req/update')).length;
      expect(afterCount).toBe(beforeCount + 1);
    } finally {
      useNexStore.setState({
        appUpdate: null,
        appUpdateError: '',
        checkingAppUpdate: false,
        updateDownload: null,
        updateBannerDismissed: false,
      });
      globalThis.fetch = origFetch;
    }
  });

  it('seals dangling running tool calls on agent_end/idle select, targets session runner_id on abort, and skips follow-up auto-drain after interrupt', async () => {
    // 1. agent_end seals dangling running tool calls
    let st = emptyStreamState();
    st = applySessionEvent(st, 'run_mac', {
      session_id: 'sess_seal_1',
      seq: 1,
      turn_idx: 0,
      event_type: 'agent_start',
      payload: {},
      created_at: 1000,
    });
    st = applySessionEvent(st, 'run_mac', {
      session_id: 'sess_seal_1',
      seq: 2,
      turn_idx: 0,
      event_type: 'message_end',
      payload: {
        message: {
          role: 'assistant',
          content: [{ type: 'tool_call', tool_call_id: 'tc_stuck', tool_name: 'bash', arguments: { command: 'sleep 99' } }],
        },
      },
      created_at: 1001,
    });
    st = applySessionEvent(st, 'run_mac', {
      session_id: 'sess_seal_1',
      seq: 3,
      turn_idx: 0,
      event_type: 'tool_execution_start',
      payload: { tool_call_id: 'tc_stuck', tool_name: 'bash' },
      created_at: 1002,
    });
    expect(st.items[0].toolCalls[0].status).toBe('running');

    st = applySessionEvent(st, 'run_mac', {
      session_id: 'sess_seal_1',
      seq: 4,
      turn_idx: 0,
      event_type: 'agent_end',
      payload: { reason: 'runner_restarted' },
      created_at: 1005,
    });
    expect(st.running).toBe(false);
    expect(st.items[0].streaming).toBe(false);
    expect(st.items[0].toolCalls[0].status).toBe('error');

    // 2. selectSession seals dangling running tool calls on an idle session even if history lacked agent_end
    const origFetch = globalThis.fetch;
    const origWS = globalThis.WebSocket;
    let lastFakeWS: { onmessage?: ((ev: MessageEvent) => void) | null; onopen?: (() => void) | null } | null = null;
    class FakeWebSocket {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSING = 2;
      static CLOSED = 3;
      readyState = 1;
      binaryType = 'arraybuffer';
      onopen: (() => void) | null = null;
      onclose: (() => void) | null = null;
      onmessage: ((ev: MessageEvent) => void) | null = null;
      constructor(_url: string) {
        lastFakeWS = this;
      }
      send(_data: string) {}
      close() {}
    }
    globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket;

    const sendSpy = vi.spyOn(wsClient, 'sendEnvelope').mockImplementation(() => 'cli_mock');
    const idleEvents = [
      {
        session_id: 'sess_idle_stuck',
        seq: 1,
        turn_idx: 0,
        event_type: 'agent_start',
        payload: {},
        created_at: 1000,
      },
      {
        session_id: 'sess_idle_stuck',
        seq: 2,
        turn_idx: 0,
        event_type: 'message_end',
        payload: {
          message: {
            role: 'assistant',
            content: [{ type: 'tool_call', tool_call_id: 'tc_orphan', tool_name: 'bash', arguments: { command: 'kill -TERM 1' } }],
          },
        },
        created_at: 1001,
      },
      {
        session_id: 'sess_idle_stuck',
        seq: 3,
        turn_idx: 0,
        event_type: 'tool_execution_start',
        payload: { tool_call_id: 'tc_orphan', tool_name: 'bash' },
        created_at: 1002,
      },
    ];
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.includes('/api/v1/auth/status')) {
        return new Response(JSON.stringify({ enabled: false, authenticated: true }), { status: 200 });
      }
      if (url.includes('/events')) {
        return new Response(JSON.stringify(idleEvents), { status: 200 });
      }
      return new Response(JSON.stringify([]), { status: 200 });
    }) as typeof fetch;

    try {
      await act(async () => {
        await originalBootstrap?.();
      });

      useNexStore.setState({
        wsConnected: true,
        selectedRunnerId: 'run_other',
        sessions: [
          {
            id: 'sess_idle_stuck',
            runner_id: 'run_target',
            depth: 0,
            title: 'Stuck but idle',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'interactive',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: 1000,
            updated_at: 1002,
            last_active_at: 1002,
          },
        ],
      });

      await act(async () => {
        await useNexStore.getState().selectSession('sess_idle_stuck');
      });

      const hydrated = useNexStore.getState().streams['sess_idle_stuck'];
      expect(hydrated.running).toBe(false);
      expect(hydrated.items[0].toolCalls[0].status).toBe('error');

      // 3. abortActiveSession uses session's runner_id ('run_target') rather than stale selectedRunnerId ('run_other')
      useNexStore.setState({ selectedRunnerId: 'run_other', activeSessionId: 'sess_idle_stuck' });
      useNexStore.getState().abortActiveSession();
      expect(sendSpy).toHaveBeenCalledWith(
        'session.abort',
        {},
        { session_id: 'sess_idle_stuck', runner_id: 'run_target' },
      );

      // 4. Interrupted agent_end (aborted_timeout / runner_restarted) does not auto-drain followUpQueues
      sendSpy.mockClear();
      useNexStore.setState({
        followUpQueues: {
          sess_idle_stuck: [{ id: 'fq_1', text: 'do not auto run after stop', createdAt: 2000 }],
        },
      });
      await act(async () => {
        lastFakeWS?.onmessage?.({
          data: JSON.stringify({
            v: 1,
            id: 'ev_aborted',
            type: 'session.event',
            ts: 3000,
            session_id: 'sess_idle_stuck',
            runner_id: 'run_target',
            payload: {
              session_id: 'sess_idle_stuck',
              seq: 4,
              turn_idx: 0,
              event_type: 'agent_end',
              payload: { reason: 'aborted_timeout' },
              created_at: 3000,
            },
          }),
        } as MessageEvent);
        await new Promise((r) => setTimeout(r, 40));
      });
      expect(useNexStore.getState().followUpQueues['sess_idle_stuck']?.length).toBe(1);
      expect(sendSpy).not.toHaveBeenCalledWith('session.prompt', expect.anything(), expect.anything());
    } finally {
      wsClient.disconnect();
      sendSpy.mockRestore();
      globalThis.WebSocket = origWS;
      globalThis.fetch = origFetch;
    }
  });

  it('opens About modal from Help menu with logo, version, shortcuts, and conditional debug build hash', async () => {
    const origFetch = globalThis.fetch;
    let healthPayload = { ok: true, version: 'dev', build_sha: 'abc1234', is_dev: true };
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.includes('/api/v1/health')) {
        return new Response(JSON.stringify(healthPayload), { status: 200 });
      }
      if (url.includes('/api/v1/updates/check')) {
        return new Response(
          JSON.stringify({
            checked_at: Date.now(),
            release_url: 'https://github.com/nex-harness/Nex/releases/tag/v0.4.0',
            latest_version: 'v0.4.0',
            server_version: healthPayload.version,
            server_is_dev: healthPayload.is_dev,
            server_has_update: !healthPayload.is_dev,
            client_download_url: 'https://example.com/Nex-v0.4.0.dmg',
          }),
          { status: 200 },
        );
      }
      return new Response(JSON.stringify([]), { status: 200 });
    }) as typeof fetch;

    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'workspace',
        activeSessionId: '',
      });

      const { unmount } = render(<App />);

      // 1. Open Help menu -> click About Nex -> shows debug badge, build hash, version, and shortcuts
      fireEvent.click(screen.getByTestId('header-help-btn'));
      fireEvent.click(screen.getByTestId('open-about-btn'));

      await waitFor(() => {
        expect(screen.getByTestId('about-build-sha').textContent).toBe('abc1234');
      });
      expect(screen.getByTestId('about-app-name').textContent).toBe('Nex');
      expect(screen.getByTestId('about-version').textContent).toBe('dev');
      expect(screen.getByTestId('about-debug-badge')).toBeTruthy();
      expect(screen.getByTestId('about-shortcuts')).toBeTruthy();

      unmount();

      // 2. Release build hides debug badge & build hash
      healthPayload = { ok: true, version: 'v0.2.0', build_sha: 'abc1234', is_dev: false };
      render(<App />);
      fireEvent.click(screen.getByTestId('header-help-btn'));
      fireEvent.click(screen.getByTestId('open-about-btn'));

      await waitFor(() => {
        expect(screen.getByTestId('about-version').textContent).toBe('v0.2.0');
      });
      expect(screen.queryByTestId('about-build-sha')).toBeNull();
      expect(screen.queryByTestId('about-debug-badge')).toBeNull();

      // 3. Clicking Check for Updates displays available update and download/release actions
      fireEvent.click(screen.getByTestId('about-check-updates-btn'));
      await waitFor(() => {
        expect(screen.getByTestId('about-update-available').textContent).toContain('v0.4.0');
      });
      expect(screen.getByTestId('about-download-desktop-btn')).toBeTruthy();
      expect(screen.getByTestId('about-view-release-btn')).toBeTruthy();
    } finally {
      useNexStore.setState({
        appUpdate: null,
        appUpdateError: '',
        checkingAppUpdate: false,
        updateDownload: null,
        updateBannerDismissed: false,
      });
      globalThis.fetch = origFetch;
    }
  });

  it('keeps detached background bash and subagent tasks visible when idle and sends per-task session.bg_task_stop', async () => {
    const sendSpy = vi.spyOn(wsClient, 'sendEnvelope').mockImplementation(() => 'env_test');
    try {
      const sid = 'sess_bg_ui_1';
      let stream = applySessionEvent(emptyStreamState(), 'run_mac', {
        session_id: sid,
        seq: 1,
        turn_idx: 0,
        event_type: 'agent_start',
        payload: {},
      });
      stream = applySessionEvent(stream, 'run_mac', {
        session_id: sid,
        seq: 2,
        turn_idx: 0,
        event_type: 'background_tasks_updated',
        payload: {
          tasks: [
            {
              task_id: 'bg_ci123456',
              kind: 'local_bash',
              title: 'gh run watch --exit-status',
              status: 'running',
              started_at: Date.now() - 5000,
            },
            {
              task_id: 'sub_ci123456',
              kind: 'local_agent',
              title: 'Watch GitHub Actions CI',
              status: 'running',
              started_at: Date.now() - 4000,
            },
          ],
        },
      });
      // Foreground turn ends, but detached background tasks remain active
      stream = applySessionEvent(stream, 'run_mac', {
        session_id: sid,
        seq: 3,
        turn_idx: 0,
        event_type: 'agent_end',
        payload: { reason: 'completed' },
      });

      expect(stream.running).toBe(false);
      expect(stream.backgroundTasks).toHaveLength(2);

      useNexStore.setState({
        activeSessionId: sid,
        selectedRunnerId: 'run_mac',
        sessions: [
          {
            id: sid,
            runner_id: 'run_mac',
            title: 'Watch CI',
            workspace_path: '/tmp/ws',
            status: 'idle',
            created_at: 1000,
            updated_at: 2000,
          } as never,
        ],
        streams: { [sid]: stream },
      });

      render(<ConversationStatusPanel mode="panel" />);

      expect(screen.getByText('gh run watch --exit-status')).toBeTruthy();
      expect(screen.getByText('Watch GitHub Actions CI')).toBeTruthy();

      fireEvent.click(screen.getByTestId('stop-bg-task-bg_ci123456'));
      fireEvent.click(screen.getByTestId('stop-bg-task-sub_ci123456'));

      expect(sendSpy).toHaveBeenCalledWith(
        'session.bg_task_stop',
        { session_id: sid, task_id: 'bg_ci123456' },
        { session_id: sid, runner_id: 'run_mac' },
      );
      expect(sendSpy).toHaveBeenCalledWith(
        'session.bg_task_stop',
        { session_id: sid, task_id: 'sub_ci123456' },
        { session_id: sid, runner_id: 'run_mac' },
      );
      expect(sendSpy).not.toHaveBeenCalledWith('session.abort', expect.anything(), expect.anything());
    } finally {
      sendSpy.mockRestore();
    }
  });

  it('displays stream error banner on turn_end error and clears it when next turn starts', () => {
    const sid = 'sess_stream_err_1';
    let stream = applySessionEvent(emptyStreamState(), 'run_mac', {
      session_id: sid,
      seq: 1,
      turn_idx: 0,
      event_type: 'agent_start',
      payload: {},
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 2,
      turn_idx: 0,
      event_type: 'message_end',
      payload: {
        message: { role: 'user', content: [{ type: 'text', text: '/goal finish task' }] },
      },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 3,
      turn_idx: 1,
      event_type: 'turn_end',
      payload: { error: 'upstream stream idle timeout: no events received for 1m30s' },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 4,
      turn_idx: 0,
      event_type: 'agent_end',
      payload: {},
    });

    expect(stream.running).toBe(false);
    expect(stream.lastError).toBe('upstream stream idle timeout: no events received for 1m30s');

    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: sid,
      selectedRunnerId: 'run_mac',
      sessions: [
        {
          id: sid,
          runner_id: 'run_mac',
          title: 'Stream error test',
          workspace_path: '/tmp/ws',
          status: 'idle',
          created_at: 1000,
          updated_at: 2000,
        } as never,
      ],
      streams: { [sid]: stream },
    });

    const { rerender } = render(<App />);
    const banner = screen.getByTestId('stream-error-banner');
    expect(banner.textContent).toContain('upstream stream idle timeout: no events received for 1m30s');
    expect(screen.getByTestId('stream-error-details-btn')).toBeTruthy();
    expect(screen.getByTestId('stream-error-copy-btn')).toBeTruthy();
    fireEvent.click(screen.getByTestId('stream-error-details-btn'));
    expect(screen.getByTestId('stream-error-details').textContent).toContain(
      'upstream stream idle timeout: no events received for 1m30s',
    );

    const restarted = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 5,
      turn_idx: 0,
      event_type: 'agent_start',
      payload: {},
    });
    expect(restarted.lastError).toBeUndefined();
    useNexStore.setState({ streams: { [sid]: restarted } });
    rerender(<App />);
    expect(screen.queryByTestId('stream-error-banner')).toBeNull();
  });

  it('uses chronological recorded_at order for latestTelemetry even when older turn_idx is larger', async () => {
    const sid = 'sess_tel_order_1';
    useNexStore.setState({
      sessions: [
        {
          id: sid,
          runner_id: 'run_mac',
          title: 'Telemetry sort test',
          workspace_path: '/tmp/ws',
          status: 'idle',
          created_at: 1000,
          updated_at: 2000,
        } as never,
      ],
    });

    await useNexStore.getState().selectSession(sid, {
      preloaded: [
        [],
        [
          {
            id: 'tel_recent',
            session_id: sid,
            turn_idx: 3,
            runner_id: 'run_mac',
            provider_id: 'openai',
            model_id: 'auto/gemini-4-neon',
            trigger_source: 'chat',
            context_window: 1048576,
            total_context_tokens: 607707,
            window_utilization_pct: 58.0,
            input_tokens: 2973,
            output_tokens: 239,
            reasoning_tokens: 0,
            cache_read_tokens: 604734,
            cache_write_tokens: 0,
            cache_hit_rate_pct: 99.5,
            estimated_cost_usd: 0.5,
            cache_saved_usd: 0,
            breakdown: [],
            system_prompt_blob_hash: '',
            raw_trace_blob_hash: '',
            ttft_ms: 100,
            duration_ms: 500,
            recorded_at: 2000,
          },
          {
            id: 'tel_old_high_turn',
            session_id: sid,
            turn_idx: 40,
            runner_id: 'run_mac',
            provider_id: 'openai',
            model_id: 'auto/gemini-4-neon',
            trigger_source: 'chat',
            context_window: 1048576,
            total_context_tokens: 90434,
            window_utilization_pct: 8.6,
            input_tokens: 90434,
            output_tokens: 978,
            reasoning_tokens: 0,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            cache_hit_rate_pct: 0,
            estimated_cost_usd: 0.286,
            cache_saved_usd: 0,
            breakdown: [],
            system_prompt_blob_hash: '',
            raw_trace_blob_hash: '',
            ttft_ms: 100,
            duration_ms: 500,
            recorded_at: 1000,
          },
        ],
      ],
    });

    const st = useNexStore.getState().streams[sid];
    expect(st?.latestTelemetry?.total_context_tokens).toBe(607707);
    expect(st?.turnTelemetries.map((t) => t.total_context_tokens)).toEqual([90434, 607707]);
  });

  it('renders ZCode-style inline compaction divider with collapsible summary and refreshes context meter', () => {
    const sid = 'sess_compaction_ui_1';
    let stream = applySessionEvent(emptyStreamState(), 'run_mac', {
      session_id: sid,
      seq: 1,
      turn_idx: 1,
      event_type: 'turn_end',
      payload: {
        telemetry: {
          id: 'tel_before',
          session_id: sid,
          turn_idx: 1,
          runner_id: 'run_mac',
          provider_id: 'openai',
          model_id: 'auto/gemini-4-neon',
          trigger_source: 'chat',
          context_window: 1048576,
          total_context_tokens: 1059442,
          window_utilization_pct: 100,
          input_tokens: 0,
          output_tokens: 0,
          reasoning_tokens: 0,
          cache_read_tokens: 0,
          cache_write_tokens: 0,
          cache_hit_rate_pct: 0,
          estimated_cost_usd: 0,
          cache_saved_usd: 0,
          breakdown: [
            { category: 'messages', bytes: 2000000, tokens: 1056000, pct: 99.7, blob_hash: 'hash_old_msgs' },
            { category: 'system_tools', bytes: 20000, tokens: 3000, pct: 0.3, blob_hash: 'hash_tools' },
            { category: 'system_prompt', bytes: 2000, tokens: 442, pct: 0.0, blob_hash: 'hash_sysprompt' },
          ],
          system_prompt_blob_hash: 'hash_sysprompt',
          raw_trace_blob_hash: 'hash_old_trace',
          ttft_ms: 0,
          duration_ms: 0,
          recorded_at: 1000,
        },
      },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 2,
      turn_idx: 0,
      event_type: 'agent_start',
      payload: {},
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 3,
      turn_idx: 0,
      event_type: 'compaction',
      payload: {
        reason: 'manual',
        compaction: {
          status: 'compacting',
          reason: 'manual',
          before_messages: 950,
          before_tokens: 561010,
        },
      },
    });

    expect(stream.items).toHaveLength(1);
    expect(stream.items[0]?.role).toBe('compaction');
    expect(stream.items[0]?.compaction?.status).toBe('compacting');

    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: sid,
      selectedRunnerId: 'run_mac',
      sessions: [
        {
          id: sid,
          runner_id: 'run_mac',
          title: 'Compaction test',
          workspace_path: '/tmp/ws',
          status: 'running',
          created_at: 1000,
          updated_at: 2000,
        } as never,
      ],
      streams: { [sid]: stream },
    });

    const { rerender } = render(<App />);
    expect(screen.getByTestId('compaction-divider').getAttribute('data-status')).toBe('compacting');
    expect(screen.getByTestId('compaction-divider').textContent).toContain('正在压缩上下文');
    // Working spinner does NOT appear concurrently with "正在压缩上下文…"
    expect(screen.queryByText(/工作中/)).toBeNull();

    // Finish compaction: updates same divider item in place, collapses summary, and refreshes context meter!
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 4,
      turn_idx: 0,
      event_type: 'compaction',
      created_at: 2000000,
      payload: {
        reason: 'manual',
        delta: '## Goal & Constraints\n- Auth refactor summary',
        compaction: {
          status: 'completed',
          reason: 'manual',
          before_messages: 950,
          after_messages: 5,
          before_tokens: 561010,
          after_tokens: 1166,
          summary: '## Goal & Constraints\n- Auth refactor summary',
        },
      },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 5,
      turn_idx: 0,
      event_type: 'agent_end',
      payload: {},
    });

    expect(stream.items).toHaveLength(1);
    // Preserves system_tools (3000) + system_prompt (442) alongside compacted messages (~724 -> total 4166)
    expect(stream.latestTelemetry?.total_context_tokens).toBe(4166);

    useNexStore.setState({ streams: { [sid]: stream } });
    rerender(<App />);

    const div = screen.getByTestId('compaction-divider');
    expect(div.getAttribute('data-status')).toBe('completed');
    expect(div.textContent).toContain('上下文已压缩');
    expect(div.textContent).toContain('561K → 1.2K');
    expect(screen.queryByTestId('compaction-summary-panel')).toBeNull();

    fireEvent.click(screen.getByTestId('compaction-toggle-summary'));
    expect(screen.getByTestId('compaction-summary-panel').textContent).toContain('Auth refactor summary');

    // Context popover keeps non-message percentages & clickability, and opens inline compacted messages summary
    fireEvent.click(screen.getByTestId('context-ring-btn'));
    expect(screen.getByTestId('context-cat-messages').textContent).not.toContain('100.0%');
    expect(screen.getByTestId('context-cat-system_tools').hasAttribute('disabled')).toBe(false);
    expect(screen.getByTestId('context-cat-system_prompt').hasAttribute('disabled')).toBe(false);
    expect(screen.getByTestId('context-cat-messages').hasAttribute('disabled')).toBe(false);

    fireEvent.click(screen.getByTestId('context-cat-messages'));
    expect(screen.getAllByText(/Auth refactor summary/).length).toBeGreaterThanOrEqual(2);
  });

  it('hides Edit button on pre-compaction user bubbles while keeping Fork/Copy and post-compaction Edit', () => {
    const sid = 'sess_precompact_edit_1';
    let stream = applySessionEvent(emptyStreamState(), 'run_mac', {
      session_id: sid,
      seq: 1,
      turn_idx: 0,
      event_type: 'message_end',
      payload: { message: { role: 'user', content: [{ type: 'text', text: 'Pre-compaction prompt' }] } },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 2,
      turn_idx: 0,
      event_type: 'compaction',
      payload: {
        reason: 'manual',
        compaction: {
          status: 'completed',
          reason: 'manual',
          before_messages: 10,
          after_messages: 2,
          before_tokens: 50000,
          after_tokens: 1500,
          summary: 'Compacted earlier turns',
        },
      },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 3,
      turn_idx: 0,
      event_type: 'message_end',
      payload: { message: { role: 'user', content: [{ type: 'text', text: 'Post-compaction prompt' }] } },
    });

    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: sid,
      selectedRunnerId: 'run_mac',
      sessions: [
        {
          id: sid,
          runner_id: 'run_mac',
          title: 'Edit guard',
          workspace_path: '/tmp/ws',
          status: 'idle',
          created_at: 1000,
          updated_at: 2000,
        } as never,
      ],
      streams: { [sid]: stream },
    });

    render(<App />);
    // Pre-compaction message hides Edit while keeping Fork; post-compaction keeps Edit & Fork
    expect(screen.queryByTestId('edit-msg-1')).toBeNull();
    expect(screen.getByTestId('edit-msg-3')).toBeTruthy();
    expect(screen.getByTestId('fork-before-1')).toBeTruthy();
    expect(screen.getByTestId('fork-before-3')).toBeTruthy();
  });

  it('persists unconfirmed pending prompt across selectSession reload and clears after server confirms it', async () => {
    const sid = 'sess_pending_refresh_1';
    const sendSpy = vi.spyOn(wsClient, 'sendEnvelope').mockImplementation(() => 'env_pending');
    try {
      useNexStore.setState({
        activeSessionId: sid,
        selectedRunnerId: 'run_mac',
        selectedProviderId: 'openai',
        selectedModelId: 'gpt-5',
        models: [
          {
            id: 'gpt-5',
            provider_id: 'openai',
            display_name: 'GPT-5',
            source: 'custom',
            context_window: 200000,
            max_output_tokens: 16384,
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
            id: sid,
            runner_id: 'run_mac',
            title: 'Pending test',
            workspace_path: '/tmp/ws',
            status: 'running',
            provider_id: 'openai',
            model_id: 'gpt-5',
            created_at: 1000,
            updated_at: 2000,
          } as never,
        ],
        streams: { [sid]: emptyStreamState() },
      });

      useNexStore.setState({ composerText: 'Keep me across refresh' });
      useNexStore.getState().sendPromptOrSteer();

      // Simulate page refresh where server DB does not yet contain the user message_end event
      await useNexStore.getState().selectSession(sid, { preloaded: [[], []] });
      let items = useNexStore.getState().streams[sid]?.items || [];
      expect(items.some((it) => it.role === 'user' && it.text === 'Keep me across refresh')).toBe(true);

      // Once server events include the confirmed user message_end, pending cache is cleared cleanly
      await useNexStore.getState().selectSession(sid, {
        preloaded: [
          [
            {
              session_id: sid,
              seq: 1,
              turn_idx: 0,
              event_type: 'message_end',
              payload: { message: { role: 'user', content: [{ type: 'text', text: 'Keep me across refresh' }] } },
            },
          ],
          [],
        ],
      });
      items = useNexStore.getState().streams[sid]?.items || [];
      expect(items.filter((it) => it.role === 'user' && it.text === 'Keep me across refresh')).toHaveLength(1);
    } finally {
      sendSpy.mockRestore();
    }
  });

  it('keeps collapsed WorkGroup fixed-height without popping running tools and splits post-prose tool steps chronologically', () => {
    const sid = 'sess_workgroup_stable_1';
    let stream = applySessionEvent(emptyStreamState(), 'run_mac', {
      session_id: sid,
      seq: 1,
      turn_idx: 0,
      event_type: 'agent_start',
      payload: {},
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 2,
      turn_idx: 0,
      event_type: 'message_end',
      payload: { message: { role: 'user', content: [{ type: 'text', text: 'Investigate' }] } },
    });
    // Step 1: thinking + text ("机制基本都收集齐了。") + tool call
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 3,
      turn_idx: 1,
      event_type: 'message_start',
      payload: {},
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 4,
      turn_idx: 1,
      event_type: 'message_update',
      payload: { delta_kind: 'thinking', delta: 'Checking step 1' },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 5,
      turn_idx: 1,
      event_type: 'message_update',
      payload: { delta_kind: 'text', delta: '机制基本都收集齐了。' },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 6,
      turn_idx: 1,
      event_type: 'message_end',
      payload: {
        message: {
          role: 'assistant',
          content: [
            { type: 'thinking', thinking: 'Checking step 1' },
            { type: 'text', text: '机制基本都收集齐了。' },
            { type: 'tool_call', tool_call_id: 'tc_1', tool_name: 'read', arguments: { file_path: 'a.go' } },
          ],
        },
      },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 7,
      turn_idx: 1,
      event_type: 'tool_execution_end',
      payload: { tool_call_id: 'tc_1', tool_name: 'read', tool_result: { output: 'ok', is_error: false } },
    });

    // Step 2: new message_start AFTER visible prose -> splits into a new assistant block below prose!
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 8,
      turn_idx: 2,
      event_type: 'message_start',
      payload: {},
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 9,
      turn_idx: 2,
      event_type: 'message_end',
      payload: {
        message: {
          role: 'assistant',
          content: [
            { type: 'tool_call', tool_call_id: 'tc_2', tool_name: 'bash', arguments: { command: 'go test ./...' } },
          ],
        },
      },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 10,
      turn_idx: 2,
      event_type: 'tool_execution_start',
      payload: { tool_call_id: 'tc_2', tool_name: 'bash' },
    });

    // Expect user + unified assistant item (intermediate narration folded into thinking + all tool calls)
    expect(stream.items).toHaveLength(2);
    expect(stream.items[1]?.thinking).toContain('机制基本都收集齐了。');
    expect(stream.items[1]?.text).toBe('');
    expect(stream.items[1]?.toolCalls.map((t) => t.id)).toEqual(['tc_1', 'tc_2']);
    expect(stream.items[1]?.toolCalls[1]?.status).toBe('running');

    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: sid,
      selectedRunnerId: 'run_mac',
      sessions: [
        {
          id: sid,
          runner_id: 'run_mac',
          title: 'Stable WorkGroup',
          workspace_path: '/tmp/ws',
          status: 'running',
          created_at: 1000,
          updated_at: 2000,
        } as never,
      ],
      streams: { [sid]: stream },
    });

    render(<App />);
    // Collapsed WorkGroup does not expand body or pop ordinary running bash tool outside
    expect(screen.queryByTestId('workgroup-body')).toBeNull();
    const headers = screen.getAllByTestId('workgroup-header');
    expect(headers).toHaveLength(1);
    // Click unified WorkGroup header to inspect its folded narration and running bash tool inside workgroup-body
    fireEvent.click(headers[0]!);
    expect(screen.getByTestId('workgroup-body').textContent).toContain('go test ./...');
  });

  it('strips active workspace prefix when opening an absolute file path and renders preview tab', async () => {
    const sid = 'sess_preview_abs';
    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: sid,
      selectedRunnerId: 'run_mac',
      selectedWorkspacePath: '/Users/tylinux/Developer/Projects/Nex-v3',
      sidePaneVisible: false,
      rightTabs: [],
      activeRightTabId: '',
      sessions: [
        {
          id: sid,
          runner_id: 'run_mac',
          title: 'Preview Abs',
          workspace_path: '/Users/tylinux/Developer/Reverse/UURemote',
          status: 'idle',
          created_at: 1000,
          updated_at: 2000,
        } as never,
      ],
    });

    const spy = vi.spyOn(wsClient, 'requestRPC').mockResolvedValue({
      v: 1,
      id: 'res_prev_1',
      type: 'workspace.res',
      ts: Date.now(),
      payload: {
        rel_path: 'docs/00-总览.md',
        name: '00-总览.md',
        preview_as: 'markdown',
        content: '# 总览\n\n传输层与鉴权说明',
        total_size: 28,
        truncated: false,
      },
    } as never);

    try {
      render(<App />);
      await act(async () => {
        await useNexStore
          .getState()
          .openFileInPreviewTab('/Users/tylinux/Developer/Reverse/UURemote/docs/00-总览.md');
      });

      expect(spy).toHaveBeenCalledWith(
        'workspace.req',
        {
          action: 'fs_read_preview',
          rel_path: 'docs/00-总览.md',
          workspace_path: '/Users/tylinux/Developer/Reverse/UURemote',
        },
        { runner_id: 'run_mac' },
      );
      expect(useNexStore.getState().sidePaneVisible).toBe(true);
      expect(screen.getByTestId('workbench-tab-file_preview').textContent).toContain('00-总览.md');
      expect(screen.getByTestId('side-pane-container').textContent).toContain('传输层与鉴权说明');
    } finally {
      spy.mockRestore();
    }
  });

  it('shows a visible error card with Retry instead of a blank pane when fs_read_preview fails', async () => {
    const sid = 'sess_preview_err';
    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: sid,
      selectedRunnerId: 'run_mac',
      selectedWorkspacePath: '/tmp/ws',
      sidePaneVisible: false,
      rightTabs: [],
      activeRightTabId: '',
      sessions: [
        {
          id: sid,
          runner_id: 'run_mac',
          title: 'Preview Error',
          workspace_path: '/tmp/ws',
          status: 'idle',
          created_at: 1000,
          updated_at: 2000,
        } as never,
      ],
    });

    const spy = vi.spyOn(wsClient, 'requestRPC').mockRejectedValue(new Error('path escapes workspace root'));

    try {
      render(<App />);
      await act(async () => {
        await useNexStore.getState().openFileInPreviewTab('/other/dir/00-总览.md');
      });

      expect(useNexStore.getState().sidePaneVisible).toBe(true);
      expect(screen.getByTestId('workbench-tab-file_preview').textContent).toContain('00-总览.md');
      const errPanel = screen.getByTestId('file-preview-error');
      expect(errPanel.textContent).toContain('无法预览文件');
      expect(errPanel.textContent).toContain('path escapes workspace root');
      expect(errPanel.textContent).toContain('重试');
    } finally {
      spy.mockRestore();
    }
  });

  it('tracks goal_lifecycle phases, renders inline GoalDivider, hides verification feedback bubbles, and fixes agent_end error status', () => {
    const sid = 'sess_goal_lifecycle_ui_1';
    let stream = applySessionEvent(emptyStreamState(), 'run_mac', {
      session_id: sid,
      seq: 1,
      turn_idx: 0,
      event_type: 'agent_start',
      payload: {},
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 2,
      turn_idx: 0,
      event_type: 'goal_lifecycle',
      payload: {
        goal: {
          goal_id: 'goal_abc123',
          phase: 'started',
          objective: 'Ship goal mode',
          round: 0,
          max_rounds: 8,
        },
      },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 3,
      turn_idx: 1,
      event_type: 'goal_lifecycle',
      payload: {
        goal: {
          goal_id: 'goal_abc123',
          phase: 'round_start',
          objective: 'Ship goal mode',
          round: 1,
          max_rounds: 8,
        },
      },
    });
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 4,
      turn_idx: 1,
      event_type: 'message_end',
      payload: {
        message: { role: 'user', content: [{ type: 'text', text: '<untrusted_objective>\nShip goal mode\n</untrusted_objective>' }] },
      },
    });
    // Should not duplicate GoalEntry when message_end arrives after goal_lifecycle(started)
    expect(stream.goals).toHaveLength(1);
    expect(stream.goals[0]?.id).toBe('goal_abc123');

    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 5,
      turn_idx: 1,
      event_type: 'goal_lifecycle',
      payload: {
        goal: {
          goal_id: 'goal_abc123',
          phase: 'verifying',
          objective: 'Ship goal mode',
          round: 1,
          max_rounds: 8,
        },
      },
    });
    expect(stream.goals[0]?.phase).toBe('verifying');

    // round_verified updates the verifying divider in place!
    const itemsBeforeVerify = stream.items.length;
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 6,
      turn_idx: 1,
      event_type: 'goal_lifecycle',
      payload: {
        goal: {
          goal_id: 'goal_abc123',
          phase: 'round_verified',
          objective: 'Ship goal mode',
          round: 1,
          max_rounds: 8,
          achieved: false,
          verdict: 'Tests have not run yet.',
          missing_items: ['Run npm test'],
          next_step: 'Run the test suite',
        },
      },
    });
    expect(stream.items.length).toBe(itemsBeforeVerify);
    expect(stream.goals[0]?.lastVerdict).toBe('Tests have not run yet.');

    // <goal-verification-feedback> user message injected between rounds is NOT shown as a raw user bubble
    stream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 7,
      turn_idx: 2,
      event_type: 'message_end',
      payload: {
        message: {
          role: 'user',
          content: [{ type: 'text', text: '<goal-verification-feedback round="1" max_rounds="8">\nNot yet\n</goal-verification-feedback>' }],
        },
      },
    });
    expect(stream.items.some((it) => it.role === 'user' && it.text.includes('goal-verification-feedback'))).toBe(false);

    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: sid,
      selectedRunnerId: 'run_mac',
      sessions: [
        {
          id: sid,
          runner_id: 'run_mac',
          title: 'Ship goal mode',
          workspace_path: '/tmp/ws',
          status: 'running',
          created_at: 1000,
          updated_at: 2000,
        } as never,
      ],
      streams: { [sid]: stream },
    });

    render(<App />);
    const dividers = screen.getAllByTestId('goal-divider');
    expect(dividers.length).toBe(2);
    expect(dividers[1]?.getAttribute('data-phase')).toBe('round_verified');
    expect(dividers[1]?.textContent).toContain('尚未达标（剩余 1 项）');
    // Expand missing items panel
    fireEvent.click(screen.getByTestId('goal-toggle-details'));
    expect(screen.getByTestId('goal-details-panel').textContent).toContain('Run npm test');

    // Status panel shows real round badge + running stop button instead of static Pause
    expect(screen.getByTestId('status-panel-goal-round').textContent).toContain('第 1/8 轮');
    expect(screen.getByTestId('status-panel-goal-stop')).toBeTruthy();

    // agent_end with error MUST mark running goal failed, NOT completed!
    const failedStream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 8,
      turn_idx: 2,
      event_type: 'agent_end',
      payload: { reason: 'error', error: 'provider timeout' },
    });
    expect(failedStream.goals[0]?.status).toBe('failed');
    expect(failedStream.goals[0]?.error).toBe('provider timeout');

    // agent_end with aborted MUST mark running goal aborted
    const abortedStream = applySessionEvent(stream, 'run_mac', {
      session_id: sid,
      seq: 8,
      turn_idx: 2,
      event_type: 'agent_end',
      payload: { reason: 'aborted' },
    });
    expect(abortedStream.goals[0]?.status).toBe('aborted');
  });

  it('shows draining workload detail and Force/Immediate upgrade action on draining runners', async () => {
    const fetchCalls: Array<{ url: string; method: string; body?: string }> = [];
    const origFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method || 'GET').toUpperCase();
      fetchCalls.push({ url, method, body: typeof init?.body === 'string' ? init.body : undefined });
      if (url.includes('/api/v1/runners/run_drain_1/update')) {
        return new Response(JSON.stringify({ ok: true }), { status: 200 });
      }
      return new Response(JSON.stringify({ items: [] }), { status: 200 });
    }) as typeof fetch;

    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'settings',
        settingsSection: 'runners',
        runners: [
          {
            id: 'run_drain_1',
            name: 'macM4',
            hostname: 'MacMini-WiFi.lan',
            os: 'darwin',
            arch: 'arm64',
            shell: 'zsh',
            tags: [],
            synced_config_rev: 1,
            status: 'online',
            workspace_default: '/Users/tylinux',
            version: 'v358c14d-dev',
            last_seen_at: Date.now(),
            is_dev: true,
            update_status: 'update_available',
            target_version: '983c8db',
            update_phase: 'draining',
            update_message: 'waiting for 1 terminal(s)',
          },
        ],
        selectedRunnerId: 'run_drain_1',
      });

      render(<App />);
      const hint = screen.getByTestId('runner-draining-hint-run_drain_1');
      expect(hint.textContent).toContain('waiting for 1 terminal(s)');
      const forceBtn = screen.getByTestId('force-upgrade-runner-run_drain_1');
      await act(async () => {
        fireEvent.click(forceBtn);
      });
      const req = fetchCalls.find((c) => c.url.includes('/api/v1/runners/run_drain_1/update') && c.method === 'POST');
      expect(req).toBeTruthy();
      expect(JSON.parse(req!.body || '{}')).toMatchObject({ mode: 'immediate', force: true });
    } finally {
      useNexStore.setState({
        appUpdate: null,
        appUpdateError: '',
        checkingAppUpdate: false,
        updateDownload: null,
        updateBannerDismissed: false,
      });
      globalThis.fetch = origFetch;
    }
  });

  it('keeps SSH modal open with error on deploy failure and closes modal + refreshes connected runners on success', async () => {
    let deployAttempt = 0;
    const origFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method || 'GET').toUpperCase();
      if (url.includes('/api/v1/ssh/targets') && method === 'POST' && !url.includes('/deploy')) {
        return new Response(
          JSON.stringify({
            id: 'ssh_new_1',
            name: '10.0.0.55',
            host: '10.0.0.55',
            port: 22,
            username: 'root',
            auth_method: 'key',
            install_mode: 'scp',
          }),
          { status: 200 },
        );
      }
      if (url.includes('/api/v1/ssh/targets/ssh_new_1/deploy') && method === 'POST') {
        deployAttempt += 1;
        if (deployAttempt === 1) {
          return new Response(
            JSON.stringify({ ok: false, error: 'ssh handshake failed: permission denied', log: 'Permission denied (publickey).' }),
            { status: 200 },
          );
        }
        return new Response(JSON.stringify({ ok: true, log: 'started nex-runner' }), { status: 200 });
      }
      return new Response(JSON.stringify([]), { status: 200 });
    }) as typeof fetch;

    const origRefreshReg = useNexStore.getState().refreshRegistries;
    const origRefreshInit = useNexStore.getState().refreshInitialData;
    const refreshRegSpy = vi.fn().mockResolvedValue(undefined);
    const refreshInitSpy = vi.fn().mockResolvedValue(undefined);
    useNexStore.setState({
      refreshRegistries: refreshRegSpy as never,
      refreshInitialData: refreshInitSpy as never,
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'settings',
      settingsSection: 'runners',
      runners: [],
      sshTargets: [],
    });

    try {
      render(<App />);
      fireEvent.click(screen.getByTestId('add-ssh-server-btn'));
      fireEvent.change(screen.getByTestId('ssh-host-input'), { target: { value: '10.0.0.55' } });

      // 1. First deploy fails -> modal stays open and displays inline error + log
      await act(async () => {
        fireEvent.click(screen.getByTestId('ssh-save-deploy-btn'));
      });
      expect(screen.getByTestId('ssh-server-modal')).toBeTruthy();
      const errBox = screen.getByTestId('ssh-modal-error');
      expect(errBox.textContent).toContain('ssh handshake failed: permission denied');
      expect(errBox.textContent).toContain('Permission denied (publickey).');

      // 2. Retry succeeds -> modal closes and connected runners refresh
      await act(async () => {
        fireEvent.click(screen.getByTestId('ssh-save-deploy-btn'));
      });
      expect(screen.queryByTestId('ssh-server-modal')).toBeNull();
      expect(refreshInitSpy).toHaveBeenCalled();
    } finally {
      globalThis.fetch = origFetch;
      useNexStore.setState({ refreshRegistries: origRefreshReg, refreshInitialData: origRefreshInit });
    }
  });

  it('organizes sidebar with runner filter pills, active/unread promotion, recency-sorted smart-collapsing projects, and recent time buckets', () => {
    const now = Date.now();
    const dayMs = 24 * 60 * 60 * 1000;
    useNexStore.setState({
      serverConnectRequired: false,
      authEnabled: false,
      authenticated: true,
      locale: 'zh-CN',
      mainView: 'workspace',
      activeSessionId: 'sess_ws2',
      selectedRunnerId: 'run_mac',
      pinnedSessionIds: {},
      unreadSessionIds: { sess_unread: true },
      runners: [
        {
          id: 'run_mac',
          name: 'macM4',
          hostname: 'macmini',
          os: 'darwin',
          arch: 'arm64',
          shell: 'zsh',
          tags: [],
          synced_config_rev: 1,
          status: 'online',
          workspace_default: '/Users/tylinux',
          version: 'ec6bb8b',
          last_seen_at: now,
        },
        {
          id: 'run_hk',
          name: 'hk',
          hostname: 'hk-box',
          os: 'linux',
          arch: 'amd64',
          shell: 'bash',
          tags: [],
          synced_config_rev: 1,
          status: 'online',
          workspace_default: '/home/tylinux',
          version: '29e196c-dev',
          last_seen_at: now,
        },
      ],
      sessions: [
        {
          id: 'sess_unread',
          runner_id: 'run_hk',
          depth: 0,
          title: 'HK Proxy Test',
          workspace_path: '/home/tylinux/docs',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'idle',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: now - 5 * dayMs,
          updated_at: now - 10_000,
          last_active_at: now - 10_000,
        },
        {
          id: 'sess_ws1',
          runner_id: 'run_mac',
          depth: 0,
          title: 'Newest Project Chat',
          workspace_path: '/Users/tylinux/Projects/Nex-v3',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'idle',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: now - 20_000,
          updated_at: now - 20_000,
          last_active_at: now - 20_000,
        },
        {
          id: 'sess_ws2',
          runner_id: 'run_mac',
          depth: 0,
          title: 'Yesterday Chat',
          workspace_path: '/Users/tylinux/Projects/Zeta',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'idle',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: now - dayMs,
          updated_at: now - dayMs,
          last_active_at: now - dayMs,
        },
        {
          id: 'sess_ws3',
          runner_id: 'run_mac',
          depth: 0,
          title: 'Old Alpha Chat',
          workspace_path: '/Users/tylinux/Projects/Alpha',
          agent_profile_slug: 'main',
          model_id: 'm1',
          provider_id: 'p1',
          permission_mode: 'interactive',
          status: 'idle',
          latest_context_tokens: 0,
          context_window_limit: 128000,
          created_at: now - 10 * dayMs,
          updated_at: now - 10 * dayMs,
          last_active_at: now - 10 * dayMs,
        },
      ],
      streams: {},
    });

    try {
      render(<App />);

      // 1. Runner filter pills appear when 2+ runners exist
      expect(screen.getByTestId('sidebar-runner-filter')).toBeTruthy();
      expect(screen.getByTestId('sidebar-runner-pill-run_mac')).toBeTruthy();
      expect(screen.getByTestId('sidebar-runner-pill-run_hk')).toBeTruthy();

      // 2. Unread session is promoted into Active & Unread section with runner · workspace context
      const activeSec = screen.getByTestId('sidebar-active-section');
      expect(activeSec.textContent).toContain('HK Proxy Test');
      expect(activeSec.textContent).toContain('hk · docs');

      // 3. Alpha (10 days old) auto-collapses because index >= 2 and not active, while Nex-v3 & Zeta stay expanded
      const alphaGroup = screen.getByTestId('project-group-run_mac::/Users/tylinux/Projects/Alpha');
      expect(alphaGroup.getAttribute('data-collapsed')).toBe('true');
      const nexGroup = screen.getByTestId('project-group-run_mac::/Users/tylinux/Projects/Nex-v3');
      expect(nexGroup.getAttribute('data-collapsed')).toBe('false');

      // 4. Clicking hk pill filters sidebar exclusively to hk sessions
      fireEvent.click(screen.getByTestId('sidebar-runner-pill-run_hk'));
      expect(screen.queryByTestId('session-row-sess_ws1')).toBeNull();
      expect(screen.getByTestId('session-row-sess_unread')).toBeTruthy();

      // Switch back to All runners
      fireEvent.click(screen.getByTestId('sidebar-runner-pill-all'));
      expect(screen.getByTestId('session-row-sess_ws1')).toBeTruthy();

      // 5. Recent mode organizes sessions into time buckets (今天 / 昨天 / 更早)
      fireEvent.click(screen.getByTestId('sidebar-mode-recent'));
      const recentView = screen.getByTestId('sidebar-recent-view');
      expect(recentView.textContent).toContain('今天');
      expect(recentView.textContent).toContain('昨天');
      expect(recentView.textContent).toContain('更早');

      // 6. Multi-token search (`hk docs`) matches across runner + workspace
      fireEvent.click(screen.getByTestId('sidebar-search-toggle'));
      fireEvent.change(screen.getByTestId('sidebar-search-input'), { target: { value: 'hk docs' } });
      expect(screen.getByTestId('session-row-sess_unread')).toBeTruthy();
      expect(screen.queryByTestId('session-row-sess_ws1')).toBeNull();
    } finally {
      useNexStore.setState({ unreadSessionIds: {} });
      try {
        window.localStorage.removeItem('nex.sidebar.group_mode.v1');
      } catch {
        // ignore
      }
    }
  });

  it('switches sidebar device badge style between tinted and neutral in Appearance settings', async () => {
    const now = Date.now();
    const putBodies: string[] = [];
    const origFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.includes('/api/v1/preferences') && (init?.method || '').toUpperCase() === 'PUT') {
        putBodies.push(String(init?.body || ''));
        return new Response(JSON.stringify({ ok: true }), { status: 200 });
      }
      return new Response(JSON.stringify({ items: [] }), { status: 200 });
    }) as typeof fetch;

    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'workspace',
        preferences: {
          theme: 'dark',
          code_theme: 'github-dark',
          font_family: 'Inter',
          mono_font_family: 'monospace',
          expand_thinking: false,
          runner_badge_style: 'tinted',
          auto_archive_after_days: 0,
        },
        runners: [
          {
            id: 'run_mac',
            name: 'macM4',
            hostname: 'macmini',
            os: 'darwin',
            arch: 'arm64',
            shell: 'zsh',
            tags: [],
            synced_config_rev: 1,
            status: 'online',
            workspace_default: '/Users/tylinux',
            version: 'ec6bb8b',
            last_seen_at: now,
          },
          {
            id: 'run_hk',
            name: 'hk',
            hostname: 'hk-box',
            os: 'linux',
            arch: 'amd64',
            shell: 'bash',
            tags: [],
            synced_config_rev: 1,
            status: 'online',
            workspace_default: '/home/tylinux',
            version: '29e196c-dev',
            last_seen_at: now,
          },
        ],
        sessions: [
          {
            id: 'sess_tint_1',
            runner_id: 'run_hk',
            depth: 0,
            title: 'Check badge',
            workspace_path: '/home/tylinux',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'interactive',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: now,
            updated_at: now,
            last_active_at: now,
          },
        ],
        streams: {},
      });

      render(<App />);
      // Tinted mode applies dark-mode contrast classes (`dark:text-`)
      expect(screen.getByTestId('sidebar-runner-pill-run_hk').className).toContain('dark:text-');

      // Switch to Settings -> Appearance and pick Minimal ("极简边框")
      fireEvent.click(screen.getByTestId('sidebar-settings-btn'));
      fireEvent.click(screen.getByTestId('settings-nav-appearance'));
      await act(async () => {
        fireEvent.click(screen.getByTestId('appearance-runner-badge-neutral'));
      });
      expect(useNexStore.getState().preferences.runner_badge_style).toBe('neutral');
      expect(putBodies.some((b) => b.includes('"runner_badge_style":"neutral"'))).toBe(true);

      // Return to workspace -> pill switches to neutral hairline border style
      fireEvent.click(screen.getByTestId('settings-back-btn'));
      expect(screen.getByTestId('sidebar-runner-pill-run_hk').className).toContain('bg-background/45');
    } finally {
      useNexStore.setState({
        appUpdate: null,
        appUpdateError: '',
        checkingAppUpdate: false,
        updateDownload: null,
        updateBannerDismissed: false,
      });
      globalThis.fetch = origFetch;
    }
  });

  it('auto-advances and auto-submits single-select question options, and hides question card in Settings', () => {
    const origResolve = useNexStore.getState().resolveUIPrompt;
    const resolveSpy = vi.fn(async () => {});
    const now = Date.now();
    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        mainView: 'workspace',
        activeSessionId: 'sess_q_auto',
        runners: [
          {
            id: 'run_1',
            name: 'Local',
            hostname: 'local-mac',
            os: 'darwin',
            arch: 'arm64',
            shell: 'zsh',
            version: '1.0',
            tags: [],
            status: 'online',
            last_seen_at: now,
            synced_config_rev: 1,
            workspace_default: '/tmp/ws',
          },
        ],
        sessions: [
          {
            id: 'sess_q_auto',
            runner_id: 'run_1',
            depth: 0,
            title: 'Auto Question',
            workspace_path: '/tmp/ws',
            agent_profile_slug: 'main',
            model_id: 'm1',
            provider_id: 'p1',
            permission_mode: 'interactive',
            status: 'idle',
            latest_context_tokens: 0,
            context_window_limit: 128000,
            created_at: now,
            updated_at: now,
            last_active_at: now,
          },
        ],
        resolveUIPrompt: resolveSpy,
        streams: {
          sess_q_auto: {
            ...emptyStreamState(),
            pendingUIPrompt: {
              requestId: 'uip_auto_1',
              sessionId: 'sess_q_auto',
              runnerId: 'run_1',
              kind: 'select',
              title: 'Choose language?',
              questions: [
                {
                  header: 'Step 1',
                  question: 'Choose language?',
                  kind: 'select',
                  options: [{ label: 'Go' }, { label: 'Rust' }],
                },
                {
                  header: 'Step 2',
                  question: 'Choose mode?',
                  kind: 'select',
                  options: [{ label: 'Fast' }, { label: 'Thorough' }],
                },
              ],
            },
          },
        },
      });

      render(<App />);
      expect(screen.getByTestId('question-pager-indicator').textContent).toBe('1 / 2');

      // Clicking single-select option on page 1 auto-advances to page 2
      fireEvent.click(screen.getByTestId('question-option-0'));
      expect(screen.getByTestId('question-pager-indicator').textContent).toBe('2 / 2');
      expect(resolveSpy).not.toHaveBeenCalled();

      // Clicking single-select option on final page auto-submits both answers
      fireEvent.click(screen.getByTestId('question-option-1'));
      expect(resolveSpy).toHaveBeenCalledWith(
        'uip_auto_1',
        true,
        JSON.stringify({
          answers: [
            { question: 'Choose language?', answer: 'Go' },
            { question: 'Choose mode?', answer: 'Thorough' },
          ],
        }),
      );

      // Switching to Settings hides the question modal overlay completely
      fireEvent.click(screen.getByTestId('sidebar-settings-btn'));
      expect(screen.queryByTestId('inline-question-card')).toBeNull();
    } finally {
      useNexStore.setState({ resolveUIPrompt: origResolve });
    }
  });

  it('renames a connected runner inline and renders interactive usage analytics trend chart', async () => {
    const origFetch = globalThis.fetch;
    const patchCalls: Array<{ url: string; body: string }> = [];
    const now = Date.now();
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = (init?.method || 'GET').toUpperCase();
      if (method === 'PATCH' && url.includes('/api/v1/runners/run_hk')) {
        const body = String(init?.body || '');
        patchCalls.push({ url, body });
        return new Response(
          JSON.stringify({
            id: 'run_hk',
            name: 'HK Edge Relay',
            os: 'linux',
            arch: 'amd64',
            shell: 'bash',
            version: '1.0',
            tags: [],
            status: 'online',
            last_seen_at: now,
            synced_config_rev: 2,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        );
      }
      if (url.includes('/api/v1/stats')) {
        return new Response(
          JSON.stringify({
            window: '7d',
            turn_count: 5,
            input_tokens: 12000,
            output_tokens: 3000,
            cache_read_tokens: 8000,
            cache_write_tokens: 500,
            total_tokens: 23500,
            cache_hit_rate_pct: 39.0,
            estimated_cost_usd: 0.1425,
            cache_saved_usd: 0.041,
            by_source_tokens: { chat: 20000, subagent: 3500, cron: 0 },
            models: [],
            series: [
              { bucket_unix_sec: 1, label: '11-21', turn_count: 2, input_tokens: 4000, output_tokens: 1000, cache_read_tokens: 2000, cache_write_tokens: 0, total_tokens: 7000, estimated_cost_usd: 0.0425 },
              { bucket_unix_sec: 2, label: '11-22', turn_count: 3, input_tokens: 8000, output_tokens: 2000, cache_read_tokens: 6000, cache_write_tokens: 500, total_tokens: 16500, estimated_cost_usd: 0.1 },
            ],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        );
      }
      return new Response(JSON.stringify({}), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }) as typeof fetch;

    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        mainView: 'settings',
        settingsSection: 'runners',
        runners: [
          {
            id: 'run_hk',
            name: 'hk-node',
            hostname: 'hk-vps',
            os: 'linux',
            arch: 'amd64',
            shell: 'bash',
            version: '1.0',
            tags: [],
            status: 'online',
            last_seen_at: now,
            synced_config_rev: 2,
            workspace_default: '/home/tylinux',
          },
        ],
      });

      render(<App />);
      fireEvent.click(screen.getByTestId('rename-runner-run_hk'));
      const input = screen.getByTestId('rename-runner-input-run_hk') as HTMLInputElement;
      fireEvent.change(input, { target: { value: 'HK Edge Relay' } });
      await act(async () => {
        fireEvent.click(screen.getByTestId('save-runner-name-run_hk'));
      });
      expect(patchCalls.some((c) => c.body.includes('"HK Edge Relay"'))).toBe(true);
      expect(useNexStore.getState().runners[0].name).toBe('HK Edge Relay');

      // Switch to Usage Analytics tab -> chart renders with series and switches metrics
      await act(async () => {
        fireEvent.click(screen.getByTestId('settings-nav-usage'));
      });
      expect(screen.getByTestId('usage-trend-chart')).toBeTruthy();
      expect(screen.getByTestId('usage-bar-0')).toBeTruthy();
      expect(screen.getByTestId('usage-bar-1')).toBeTruthy();
      fireEvent.click(screen.getByTestId('usage-chart-metric-cost'));
      expect(screen.getByTestId('usage-chart-inspector').textContent).toContain('$0.1000');
    } finally {
      useNexStore.setState({
        appUpdate: null,
        appUpdateError: '',
        checkingAppUpdate: false,
        updateDownload: null,
        updateBannerDismissed: false,
      });
      globalThis.fetch = origFetch;
    }
  });

  it('shows Edit model modal without redundant inline rename, hides Provider ID, and renders provider names in Usage Analytics', async () => {
    const origFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.includes('/api/v1/stats')) {
        return new Response(
          JSON.stringify({
            window: 'today',
            turn_count: 2,
            input_tokens: 1000,
            output_tokens: 500,
            cache_read_tokens: 200,
            cache_write_tokens: 0,
            total_tokens: 1700,
            cache_hit_rate_pct: 16.6,
            estimated_cost_usd: 0.012,
            cache_saved_usd: 0.002,
            by_source_tokens: { chat: 1700, subagent: 0, cron: 0 },
            models: [
              {
                provider_id: 'custom_gw_1',
                model_id: 'gpt-5',
                turn_count: 2,
                total_tokens: 1700,
                estimated_cost_usd: 0.012,
                cache_saved_usd: 0.002,
              },
            ],
            series: [],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        );
      }
      return new Response(JSON.stringify({}), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }) as typeof fetch;

    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'settings',
        settingsSection: 'providers',
        providers: [
          {
            id: 'custom_gw_1',
            name: 'My OpenAI Gateway',
            driver: 'openai_responses',
            base_url: 'https://example.com/v1',
            api_key: 'sk-test',
            proxy_mode: 'direct',
            enabled: true,
          },
        ],
        models: [
          {
            id: 'gpt-5',
            provider_id: 'custom_gw_1',
            display_name: 'GPT-5',
            source: 'custom',
            context_window: 400000,
            max_output_tokens: 32000,
            supports_vision: true,
            supports_pdf: true,
            supports_tools: true,
            supports_reasoning: true,
            supports_caching: true,
            pricing: { input_per_1m: 1, output_per_1m: 2, cache_read_per_1m: 0.1, cache_write_per_1m: 0 },
          },
        ],
      });

      render(<App />);

      // Provider ID input stays hidden even when expanding Advanced
      fireEvent.click(screen.getByText('高级选项'));
      expect(screen.queryByText('供应商 ID')).toBeNull();

      // Clicking configure opens "编辑模型" (not "手动添加模型") and omits inline Pencil rename button
      expect(screen.queryByTitle('重命名显示名称')).toBeNull();
      fireEvent.click(screen.getByTestId('edit-model-gpt-5'));
      expect(screen.getByTestId('model-modal-title').textContent).toBe('编辑模型');
      expect(screen.getByText('PDF')).toBeTruthy();
      expect(screen.getByText('Tools')).toBeTruthy();
      expect(screen.getByText('Prompt Cache')).toBeTruthy();
      fireEvent.click(screen.getByText('取消'));

      // Switching to Usage Analytics displays friendly provider name instead of raw provider_id
      await act(async () => {
        fireEvent.click(screen.getByTestId('settings-nav-usage'));
      });
      expect(screen.getByText('My OpenAI Gateway')).toBeTruthy();
      expect(screen.queryByText('custom_gw_1')).toBeNull();
    } finally {
      useNexStore.setState({
        appUpdate: null,
        appUpdateError: '',
        checkingAppUpdate: false,
        updateDownload: null,
        updateBannerDismissed: false,
      });
      globalThis.fetch = origFetch;
    }
  });

  it('renders UpdateBanner for available/ready updates, persists skip version, and toggles auto-check preference', async () => {
    const origFetch = globalThis.fetch;
    const putBodies: string[] = [];
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.includes('/api/v1/preferences') && (init?.method || 'GET').toUpperCase() === 'PUT') {
        putBodies.push(String(init?.body || ''));
        return new Response(String(init?.body || '{}'), { status: 200 });
      }
      return new Response(JSON.stringify({}), { status: 200 });
    }) as typeof fetch;

    try {
      useNexStore.setState({
        serverConnectRequired: false,
        authEnabled: false,
        authenticated: true,
        locale: 'zh-CN',
        mainView: 'workspace',
        activeSessionId: '',
        updateBannerDismissed: false,
        updateDownload: null,
        appUpdate: {
          checked_at: Date.now(),
          release_url: 'https://github.com/nex-harness/Nex/releases/tag/v0.5.0',
          latest_version: 'v0.5.0',
          release_notes: '## Highlights\n- Signed macOS DMG',
          server_version: 'v0.4.0',
          server_is_dev: false,
          server_has_update: true,
          client_version: '0.4.0',
          client_platform: 'darwin',
          client_is_dev: false,
          client_has_update: true,
          client_download_url: 'https://github.com/nex-harness/Nex/releases/download/v0.5.0/Nex-v0.5.0-darwin-arm64.dmg',
          client_asset_name: 'Nex-v0.5.0-darwin-arm64.dmg',
        },
      });

      const { rerender } = render(<App />);

      const banner = screen.getByTestId('app-update-banner');
      expect(banner.textContent).toContain('v0.5.0');
      expect(screen.getByTestId('update-banner-download-btn')).toBeTruthy();

      // Switch to ready state -> shows Install update button
      act(() => {
        useNexStore.getState().setUpdateDownload({
          version: 'v0.5.0',
          asset_name: 'Nex-v0.5.0-darwin-arm64.dmg',
          downloaded_bytes: 1024,
          total_bytes: 1024,
          percent: 100,
          status: 'ready',
          local_path: '/tmp/nex-updates/v0.5.0/Nex-v0.5.0-darwin-arm64.dmg',
        });
      });
      rerender(<App />);
      expect(screen.getByTestId('update-banner-install-btn')).toBeTruthy();

      // Skip this version -> hides banner and persists skipped_update_version
      await act(async () => {
        fireEvent.click(screen.getByTestId('update-banner-skip-btn'));
      });
      expect(screen.queryByTestId('app-update-banner')).toBeNull();
      expect(putBodies.some((b) => b.includes('"skipped_update_version":"v0.5.0"'))).toBe(true);
    } finally {
      useNexStore.setState({
        appUpdate: null,
        updateDownload: null,
        updateBannerDismissed: false,
      });
      globalThis.fetch = origFetch;
    }
  });

  it('formats runner and target versions with build SHA cleanly', () => {
    expect(formatRunnerVersion('0.4.0', '49019b2abcdef')).toBe('v0.4.0(49019b2)');
    expect(formatRunnerVersion('v0.4.0', '49019b2')).toBe('v0.4.0(49019b2)');
    expect(formatRunnerVersion('v0.4.0+49019b2')).toBe('v0.4.0(49019b2)');
    expect(formatRunnerVersion('0.4.0', '')).toBe('v0.4.0');
    expect(formatRunnerVersion('v0.4.1-local', '704f4c9')).toBe('v0.4.1-local(704f4c9)');
    expect(formatRunnerVersion('1dd48cf', '')).toBe('1dd48cf');
    expect(formatRunnerVersion('dev', '49019b2')).toBe('dev(49019b2)');
  });

  it('shows macOS Full Disk Access badge, opens drag-to-grant guide modal, and updates status after check', async () => {
    const origFetch = globalThis.fetch;
    const permActions: string[] = [];
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes('/api/v1/runners/mac_fda/permissions') && init?.method === 'POST') {
        const body = JSON.parse(String(init.body || '{}')) as { action?: string };
        permActions.push(body.action || '');
        return new Response(
          JSON.stringify({
            ok: true,
            runner_id: 'mac_fda',
            action: body.action,
            full_disk_access: body.action === 'check',
            app_path: '/Users/tylinux/.nex-runner/NexRunner.app',
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        );
      }
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }) as typeof fetch;

    try {
      useNexStore.setState({
        mainView: 'settings',
        settingsSection: 'runners',
        runners: [
          {
            id: 'mac_fda',
            name: 'macM4',
            hostname: 'MacMini',
            os: 'darwin',
            arch: 'arm64',
            shell: '/bin/zsh',
            version: 'v0.4.1',
            build_sha: '5bd72ee',
            tags: [],
            status: 'online',
            last_seen_at: Date.now(),
            synced_config_rev: 1,
            workspace_default: '/Users/tylinux',
            full_disk_access: false,
            app_path: '/Users/tylinux/.nex-runner/NexRunner.app',
          },
        ],
      });

      render(<App />);

      const badgeBtn = screen.getByTestId('runner-fda-btn-mac_fda');
      fireEvent.click(badgeBtn);

      const modal = screen.getByTestId('runner-fda-modal');
      expect(modal.textContent).toContain('NexRunner.app');

      await act(async () => {
        fireEvent.click(screen.getByTestId('fda-open-guide-btn'));
      });
      await act(async () => {
        fireEvent.click(screen.getByTestId('fda-recheck-btn'));
      });

      expect(permActions).toEqual(['open_guide', 'check']);
      expect(screen.getByTestId('runner-fda-granted-mac_fda')).toBeTruthy();
    } finally {
      globalThis.fetch = origFetch;
    }
  });

  it('matches renamed runners to SSH targets by deterministic runner ID and supports per-runner Update Policy (auto-update + SCP deploy)', async () => {
    const origFetch = globalThis.fetch;
    const patchBodies: string[] = [];
    const deployHits: string[] = [];
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes('/api/v1/runners/run_macm4') && init?.method === 'PATCH') {
        const body = String(init.body || '{}');
        patchBodies.push(body);
        const parsed = JSON.parse(body) as { auto_update?: boolean; update_mode?: 'self_update' | 'scp' };
        return new Response(
          JSON.stringify({
            id: 'run_macm4',
            name: 'M4',
            ...parsed,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        );
      }
      if (url.includes('/api/v1/ssh/targets/ssh_mac/deploy') && init?.method === 'POST') {
        deployHits.push(url);
        return new Response(JSON.stringify({ ok: true, log: 'deployed via scp' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }) as typeof fetch;

    try {
      useNexStore.setState({
        mainView: 'settings',
        settingsSection: 'runners',
        sshTargets: [
          {
            id: 'ssh_mac',
            name: 'macM4',
            host: '192.168.123.22',
            port: 22,
            username: 'tylinux',
            auth_method: 'key',
            key_path: '~/.ssh/id_ed25519',
            install_mode: 'scp',
            install_dir: '~/.nex-runner',
            workspace_path: '/Users/tylinux',
            server_ws_url: 'wss://nex.tylinux.cn/ws/runner',
            last_status: 'deployed',
            last_error: '',
            last_log: '',
          },
        ],
        runners: [
          {
            id: 'run_macm4',
            name: 'M4',
            hostname: 'MacMini-WiFi.lan',
            os: 'darwin',
            arch: 'arm64',
            shell: '/bin/bash',
            version: 'v0.4.1-local',
            build_sha: '64cc395',
            tags: [],
            status: 'online',
            last_seen_at: Date.now(),
            synced_config_rev: 1,
            workspace_default: '/Users/tylinux',
            auto_update: true,
            update_mode: 'self_update',
            update_status: 'update_available',
            target_version: 'v0.4.1-local',
            target_build_sha: '7841c1e',
          },
        ],
      });

      render(<App />);

      // Renamed runner `M4` (id run_macm4) still displays SSH actions from target `macM4`
      expect(screen.getByTestId('ssh-deploy-ssh_mac')).toBeTruthy();

      // Toggle per-runner auto-update OFF and switch delivery mode to SCP
      await act(async () => {
        fireEvent.click(screen.getByTestId('runner-auto-update-run_macm4'));
      });
      await act(async () => {
        fireEvent.change(screen.getByTestId('runner-update-mode-run_macm4'), { target: { value: 'scp' } });
      });

      expect(patchBodies.some((b) => b.includes('"auto_update":false'))).toBe(true);
      expect(patchBodies.some((b) => b.includes('"update_mode":"scp"'))).toBe(true);

      // Clicking upgrade now triggers SSH SCP deploy
      await act(async () => {
        fireEvent.click(screen.getByTestId('upgrade-runner-run_macm4'));
      });
      expect(deployHits.length).toBe(1);
    } finally {
      globalThis.fetch = origFetch;
    }
  });
});
