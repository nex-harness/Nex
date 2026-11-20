import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { App } from '../App';
import { decodePTYBinaryFrame, encodePTYBinaryFrame, PTY_BINARY_STDOUT } from '../core/protocol';
import { applySessionEvent, emptyStreamState } from '../core/reducer';
import { useNexStore } from '../core/store';

afterEach(() => {
  cleanup();
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
    useNexStore.setState({
      mainView: 'workspace',
      activeSessionId: '',
      locale: 'zh-CN',
      selectedRunnerId: 'run_1',
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
});
