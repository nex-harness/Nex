import {
  Archive,
  ArrowUp,
  BarChart3,
  Brain,
  ChevronDown,
  FolderGit2,
  PanelRight,
  Plus,
  Server,
  Settings,
  ShieldAlert,
  Square,
  Terminal as TerminalIcon,
  X,
} from 'lucide-react';
import { useEffect, useState } from 'react';
import { emptyStreamState } from './core/reducer';
import { useNexStore } from './core/store';
import { ContextWindowsButton } from './features/ContextPopover';
import { ApprovalModal, SettingsAndArchiveModal, StatsModal, UIPromptModal } from './features/ControlModals';
import { GhosttyTerminal } from './features/GhosttyTerminal';
import { WorkbenchSidebar } from './features/WorkbenchSidebar';

export function App() {
  const {
    wsConnected,
    runners,
    models,
    sessions,
    selectedRunnerId,
    selectedProviderId,
    selectedModelId,
    thinkingEffort,
    permissionMode,
    activeSessionId,
    composerText,
    streams,
    gitSummary,
    bottomTerminals,
    activeBottomTermId,
    showBottomPanel,
    bootstrap,
    setComposerText,
    setSelectedRunner,
    setSelectedModel,
    setThinkingEffort,
    setPermissionMode,
    selectSession,
    startNewSession,
    sendPromptOrSteer,
    abortActiveSession,
    archiveSession,
    openRightTab,
    spawnBottomTerminal,
    closeBottomTerminal,
  } = useNexStore();

  const [showSidebar, setShowSidebar] = useState(true);
  const [showSessionsDrawer, setShowSessionsDrawer] = useState(false);
  const [showStats, setShowStats] = useState(false);
  const [showSettings, setShowSettings] = useState(false);
  const [expandedThinking, setExpandedThinking] = useState<Record<string, boolean>>({});

  useEffect(() => {
    void bootstrap();
  }, [bootstrap]);

  const currentStream = (activeSessionId && streams[activeSessionId]) || emptyStreamState();
  const activeModelMeta = models.find((m) => m.provider_id === selectedProviderId && m.id === selectedModelId);
  const activeSessionMeta = sessions.find((s) => s.id === activeSessionId);
  const activeBottomTerm = bottomTerminals.find((t) => t.id === activeBottomTermId) || bottomTerminals[0];

  return (
    <div className="flex h-screen w-screen overflow-hidden bg-zinc-950 text-zinc-100">
      {/* Left Workspace + Bottom Terminal Stack */}
      <div className="flex min-w-0 flex-1 flex-col">
        {/* Top Header Bar */}
        <header className="flex h-12 shrink-0 items-center justify-between border-b border-zinc-800/80 px-4">
          <div className="flex items-center gap-2.5 min-w-0">
            <button
              type="button"
              onClick={() => setShowSessionsDrawer((v) => !v)}
              className="flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-900/70 px-2.5 py-1 text-xs font-medium text-zinc-200 hover:bg-zinc-800"
            >
              <FolderGit2 className="h-3.5 w-3.5 text-blue-400" />
              <span className="max-w-[220px] truncate">{activeSessionMeta?.title || 'New Conversation'}</span>
              <ChevronDown className="h-3 w-3 text-zinc-500" />
            </button>

            <button
              type="button"
              onClick={startNewSession}
              title="New conversation"
              className="rounded-lg border border-zinc-800 p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
            >
              <Plus className="h-3.5 w-3.5" />
            </button>

            {/* Runner Selector */}
            <div className="flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-900/50 px-2 py-1 text-xs">
              <Server className="h-3.5 w-3.5 text-zinc-400" />
              <select
                data-testid="runner-select"
                value={selectedRunnerId}
                onChange={(e) => setSelectedRunner(e.target.value)}
                className="bg-transparent text-xs text-zinc-200 outline-none"
              >
                {runners.length === 0 && <option value="">No Runners Connected</option>}
                {runners.map((r) => (
                  <option key={r.id} value={r.id} className="bg-zinc-900">
                    {r.status === 'online' ? '● ' : '○ '}
                    {r.name} ({r.os})
                  </option>
                ))}
              </select>
            </div>

            {/* Plugin ctx.ui Status Badges */}
            {Object.entries(currentStream.uiBadges).map(([k, badge]) => (
              <span key={k} className="rounded-full bg-blue-500/15 px-2.5 py-0.5 font-mono text-[11px] text-blue-300">
                {k}: {badge}
              </span>
            ))}
          </div>

          <div className="flex items-center gap-2">
            {/* Git Diff Capsule `[± 更改 +X -Y]` */}
            {gitSummary?.is_git_repo && (
              <button
                type="button"
                data-testid="git-diff-capsule"
                onClick={() => {
                  setShowSidebar(true);
                  void openRightTab('review', 'Review');
                }}
                className="flex items-center gap-1.5 rounded-full border border-zinc-800 bg-zinc-900 px-3 py-1 text-xs hover:border-zinc-700 hover:bg-zinc-800"
              >
                <span className="text-zinc-300">更改</span>
                <span className="font-mono text-emerald-400">+{gitSummary.additions}</span>
                <span className="font-mono text-rose-400">-{gitSummary.deletions}</span>
              </button>
            )}

            <span
              className={`h-2 w-2 rounded-full ${wsConnected ? 'bg-emerald-400' : 'bg-rose-500'}`}
              title={wsConnected ? 'Connected to nex-server' : 'Disconnected'}
            />

            <button
              type="button"
              onClick={() => setShowStats(true)}
              title="Usage & Cost Analytics"
              className="rounded-lg p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
            >
              <BarChart3 className="h-4 w-4" />
            </button>

            <button
              type="button"
              onClick={() => setShowSettings(true)}
              title="Settings, Providers & Archives"
              className="rounded-lg p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
            >
              <Settings className="h-4 w-4" />
            </button>

            <button
              type="button"
              onClick={() => setShowSidebar((v) => !v)}
              title="Toggle Workbench Sidebar"
              className="rounded-lg p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
            >
              <PanelRight className="h-4 w-4" />
            </button>
          </div>
        </header>

        {/* Sessions Slide-over List */}
        {showSessionsDrawer && (
          <div className="border-b border-zinc-800 bg-zinc-900/90 px-4 py-2.5 max-h-56 overflow-auto">
            <div className="mb-1.5 text-[11px] font-medium uppercase tracking-wider text-zinc-500">Active Sessions</div>
            {sessions.length === 0 ? (
              <div className="py-2 text-xs text-zinc-500">No active sessions yet. Send a message below to start!</div>
            ) : (
              <div className="space-y-1">
                {sessions.map((s) => (
                  <div
                    key={s.id}
                    className={`flex items-center justify-between rounded-lg px-2.5 py-1.5 text-xs ${
                      s.id === activeSessionId ? 'bg-blue-600/20 text-blue-200' : 'hover:bg-zinc-800 text-zinc-300'
                    }`}
                  >
                    <button
                      type="button"
                      onClick={() => {
                        setShowSessionsDrawer(false);
                        void selectSession(s.id);
                      }}
                      className="flex-1 truncate text-left"
                    >
                      {s.title}
                    </button>
                    <button
                      type="button"
                      onClick={() => void archiveSession(s.id)}
                      title="Archive session to cold archive.db"
                      className="ml-2 rounded p-1 text-zinc-500 hover:bg-zinc-700 hover:text-zinc-200"
                    >
                      <Archive className="h-3.5 w-3.5" />
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}

        {/* Main Conversation Stream */}
        <main data-testid="chat-stream" className="flex-1 space-y-4 overflow-y-auto px-6 py-5">
          {currentStream.items.length === 0 && (
            <div className="flex h-full flex-col items-center justify-center text-center">
              <div className="rounded-2xl border border-zinc-800/80 bg-zinc-900/40 px-6 py-5 max-w-md">
                <h2 className="text-sm font-semibold text-zinc-200">Nex-v3 Multi-Runner Agent Harness</h2>
                <p className="mt-1.5 text-xs leading-relaxed text-zinc-400">
                  Connected to Go-native headless Runner. Supports Codex Responses & Anthropic streaming, 6-category
                  Context Windows inspector, libghostty remote PTY, and lazy Code Mode MCP.
                </p>
              </div>
            </div>
          )}

          {currentStream.items.map((item) => (
            <div
              key={item.id}
              className={`flex flex-col ${item.role === 'user' ? 'items-end' : 'items-start'}`}
            >
              {item.role === 'user' ? (
                <div className="max-w-2xl rounded-2xl bg-zinc-800/90 px-4 py-2.5 text-sm whitespace-pre-wrap text-zinc-100">
                  {item.text}
                </div>
              ) : (
                <div className="w-full max-w-3xl space-y-2.5">
                  {item.thinking && (
                    <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/40 px-3 py-2 text-xs">
                      <button
                        type="button"
                        onClick={() => setExpandedThinking((p) => ({ ...p, [item.id]: !p[item.id] }))}
                        className="flex items-center gap-1.5 font-medium text-zinc-400 hover:text-zinc-200"
                      >
                        <Brain className="h-3.5 w-3.5 text-purple-400" />
                        <span>Thinking</span>
                      </button>
                      {expandedThinking[item.id] && (
                        <pre className="mt-2 whitespace-pre-wrap font-mono text-[11px] text-zinc-400">
                          {item.thinking}
                        </pre>
                      )}
                    </div>
                  )}

                  {item.toolCalls.map((tc) => (
                    <div key={tc.id} className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3 text-xs">
                      <div className="flex items-center justify-between">
                        <span className="font-mono font-medium text-blue-300">{tc.name}</span>
                        <span
                          className={`rounded px-1.5 py-0.5 text-[10px] ${
                            tc.status === 'running'
                              ? 'bg-amber-500/20 text-amber-300'
                              : tc.status === 'error'
                                ? 'bg-rose-500/20 text-rose-300'
                                : 'bg-emerald-500/20 text-emerald-300'
                          }`}
                        >
                          {tc.status}
                          {tc.additions || tc.deletions ? ` (+${tc.additions || 0} -${tc.deletions || 0})` : ''}
                        </span>
                      </div>
                      {(tc.finalOutput || tc.streamOutput) && (
                        <pre className="mt-2 max-h-48 overflow-auto rounded bg-zinc-950 p-2 font-mono text-[11px] whitespace-pre-wrap text-zinc-300">
                          {tc.finalOutput || tc.streamOutput}
                        </pre>
                      )}
                    </div>
                  ))}

                  {(item.text || item.streaming) && (
                    <div className="text-sm leading-relaxed whitespace-pre-wrap text-zinc-100">
                      {item.text}
                      {item.streaming && <span className="ml-1 inline-block h-3.5 w-1.5 animate-pulse bg-blue-400" />}
                    </div>
                  )}
                </div>
              )}
            </div>
          ))}
        </main>

        {/* Composer Box (matching screenshot layout) */}
        <div className="px-5 pb-3">
          <div className="rounded-2xl border border-zinc-800 bg-zinc-900/70 p-3 shadow-lg">
            <textarea
              data-testid="composer-input"
              rows={2}
              value={composerText}
              onChange={(e) => setComposerText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  sendPromptOrSteer();
                }
              }}
              placeholder={
                currentStream.running
                  ? 'Agent 运行中 — 输入并回车即时发送 Steer 转向指令...'
                  : '向 Nex 提问，使用 @ 添加上下文，使用 / 选择命令或能力'
              }
              className="w-full resize-none bg-transparent text-sm text-zinc-100 placeholder-zinc-500 outline-none"
            />

            <div className="mt-2 flex flex-wrap items-center justify-between gap-2 pt-1">
              {/* Left Bottom Controls: Permission Mode */}
              <div className="flex items-center gap-2">
                <div className="flex items-center gap-1 rounded-lg px-2 py-1 text-xs text-amber-400 hover:bg-zinc-800">
                  <ShieldAlert className="h-3.5 w-3.5" />
                  <select
                    value={permissionMode}
                    onChange={(e) => setPermissionMode(e.target.value as 'full_access' | 'interactive' | 'read_only')}
                    className="bg-transparent text-xs font-medium text-amber-400 outline-none"
                  >
                    <option value="full_access" className="bg-zinc-900 text-zinc-100">
                      完全访问 (Full access)
                    </option>
                    <option value="interactive" className="bg-zinc-900 text-zinc-100">
                      每次询问 (Interactive)
                    </option>
                    <option value="read_only" className="bg-zinc-900 text-zinc-100">
                      只读模式 (Read only)
                    </option>
                  </select>
                </div>
              </div>

              {/* Right Bottom Controls: Context Ring + Model Selector + Thinking Effort + Send/Stop */}
              <div className="flex items-center gap-2">
                <ContextWindowsButton
                  telemetry={currentStream.latestTelemetry}
                  contextLimit={activeModelMeta?.context_window || 200000}
                />

                <select
                  data-testid="model-select"
                  value={`${selectedProviderId}::${selectedModelId}`}
                  onChange={(e) => {
                    const [pid, mid] = e.target.value.split('::');
                    setSelectedModel(pid, mid);
                  }}
                  className="max-w-[230px] truncate rounded-lg bg-zinc-800/70 px-2.5 py-1 text-xs text-zinc-300 outline-none hover:bg-zinc-800"
                >
                  {models.map((m) => (
                    <option key={`${m.provider_id}::${m.id}`} value={`${m.provider_id}::${m.id}`} className="bg-zinc-900">
                      {m.provider_id}/{m.id}
                    </option>
                  ))}
                </select>

                <select
                  value={thinkingEffort}
                  onChange={(e) => setThinkingEffort(e.target.value as '' | 'low' | 'medium' | 'high')}
                  className="rounded-lg bg-zinc-800/70 px-2 py-1 text-xs text-zinc-300 outline-none hover:bg-zinc-800"
                >
                  <option value="" className="bg-zinc-900">Off</option>
                  <option value="low" className="bg-zinc-900">低 (Low)</option>
                  <option value="medium" className="bg-zinc-900">中 (Medium)</option>
                  <option value="high" className="bg-zinc-900">高 (High)</option>
                </select>

                {currentStream.running && !composerText.trim() ? (
                  <button
                    type="button"
                    onClick={abortActiveSession}
                    title="Stop current agent execution"
                    className="flex h-7 w-7 items-center justify-center rounded-lg bg-rose-600 text-white hover:bg-rose-500"
                  >
                    <Square className="h-3.5 w-3.5" />
                  </button>
                ) : (
                  <button
                    type="button"
                    data-testid="send-btn"
                    disabled={!composerText.trim() || !selectedRunnerId}
                    onClick={() => sendPromptOrSteer()}
                    className="flex h-7 w-7 items-center justify-center rounded-lg bg-zinc-200 text-zinc-900 hover:bg-white disabled:opacity-35"
                  >
                    <ArrowUp className="h-4 w-4" />
                  </button>
                )}
              </div>
            </div>
          </div>
        </div>

        {/* Bottom Dock: Multi-Tab libghostty Remote Terminal (matching screenshot bottom-left panel) */}
        {showBottomPanel && (
          <div className="flex h-52 shrink-0 flex-col border-t border-zinc-800 bg-zinc-950">
            <div className="flex items-center justify-between border-b border-zinc-800/80 bg-zinc-900/50 px-3 py-1">
              <div className="flex items-center gap-1.5 overflow-x-auto">
                <span className="flex items-center gap-1 text-xs font-medium text-zinc-300 mr-1">
                  <TerminalIcon className="h-3.5 w-3.5 text-zinc-400" />
                  终端
                </span>
                {bottomTerminals.map((bt) => (
                  <div
                    key={bt.id}
                    onClick={() => useNexStore.setState({ activeBottomTermId: bt.id })}
                    className={`flex cursor-pointer items-center gap-1.5 rounded-md px-2 py-0.5 text-xs ${
                      bt.id === activeBottomTerm?.id ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-400 hover:bg-zinc-800/50'
                    }`}
                  >
                    <span>{bt.title}</span>
                    <button
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        closeBottomTerminal(bt.id);
                      }}
                      className="text-zinc-500 hover:text-zinc-200"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  onClick={() => void spawnBottomTerminal()}
                  title="New remote libghostty terminal"
                  className="rounded p-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
                >
                  <Plus className="h-3.5 w-3.5" />
                </button>
              </div>
              <button
                type="button"
                onClick={() => useNexStore.setState({ showBottomPanel: false })}
                className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
            <div className="flex-1 overflow-hidden">
              <GhosttyTerminal ptyId={activeBottomTerm?.ptyId || ''} runnerId={selectedRunnerId} />
            </div>
          </div>
        )}
      </div>

      {/* Right Multi-Tab Workbench Sidebar (Files / New Tab Preview / Review Git Diff / Terminal / Side Chat) */}
      {showSidebar && (
        <div className="w-[420px] shrink-0">
          <WorkbenchSidebar />
        </div>
      )}

      {/* Modals */}
      {currentStream.pendingApproval && <ApprovalModal approval={currentStream.pendingApproval} />}
      {currentStream.pendingUIPrompt && <UIPromptModal prompt={currentStream.pendingUIPrompt} />}
      {showStats && <StatsModal onClose={() => setShowStats(false)} />}
      {showSettings && <SettingsAndArchiveModal onClose={() => setShowSettings(false)} />}
    </div>
  );
}
