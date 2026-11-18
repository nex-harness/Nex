import { ChevronDown, ChevronRight, FileCode, Folder, GitCompare, MessageSquare, Plus, TerminalSquare, X } from 'lucide-react';
import { type ReactNode, useEffect, useState } from 'react';
import { useNexStore } from '../core/store';
import { DirEntry, FilePreview, WorkbenchTabKind } from '../core/types';
import { GhosttyTerminal } from './GhosttyTerminal';

export function WorkbenchSidebar() {
  const {
    rightTabs,
    activeRightTabId,
    selectedRunnerId,
    wsConnected,
    gitSummary,
    openRightTab,
    closeRightTab,
    setActiveRightTab,
    openFileInPreviewTab,
    listWorkspaceDir,
    searchWorkspaceFiles,
    refreshGitSummary,
    appendComposerRef,
  } = useNexStore();

  const [plusMenuOpen, setPlusMenuOpen] = useState(false);
  const activeTab = rightTabs.find((t) => t.id === activeRightTabId) || rightTabs[0];

  const menuItems: Array<{ kind: WorkbenchTabKind; label: string; icon: ReactNode }> = [
    { kind: 'side_chat', label: 'Side conversation', icon: <MessageSquare className="h-3.5 w-3.5" /> },
    { kind: 'review', label: 'Review', icon: <GitCompare className="h-3.5 w-3.5" /> },
    { kind: 'files', label: 'Files', icon: <Folder className="h-3.5 w-3.5" /> },
    { kind: 'terminal', label: 'Terminal', icon: <TerminalSquare className="h-3.5 w-3.5" /> },
  ];

  return (
    <aside className="flex h-full w-full flex-col border-l border-zinc-800 bg-zinc-950">
      {/* Multi-Tab Bar + '+' Dropdown */}
      <div className="relative flex items-center gap-1 border-b border-zinc-800 bg-zinc-900/60 px-2 py-1.5 overflow-x-auto">
        {rightTabs.map((tab) => {
          const isActive = tab.id === activeTab?.id;
          return (
            <div
              key={tab.id}
              onClick={() => setActiveRightTab(tab.id)}
              className={`group flex cursor-pointer items-center gap-1.5 rounded-lg px-2.5 py-1 text-xs transition ${
                isActive ? 'bg-zinc-800 text-zinc-100 font-medium' : 'text-zinc-400 hover:bg-zinc-800/50 hover:text-zinc-200'
              }`}
            >
              <span className="max-w-[140px] truncate">{tab.title}</span>
              {tab.closable && rightTabs.length > 1 && (
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation();
                    closeRightTab(tab.id);
                  }}
                  className="rounded p-0.5 text-zinc-500 hover:bg-zinc-700 hover:text-zinc-200"
                >
                  <X className="h-3 w-3" />
                </button>
              )}
            </div>
          );
        })}

        <div className="relative ml-0.5">
          <button
            type="button"
            data-testid="workbench-plus-btn"
            onClick={() => setPlusMenuOpen((v) => !v)}
            className="flex h-6 w-6 items-center justify-center rounded-lg text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
          >
            <Plus className="h-3.5 w-3.5" />
          </button>

          {plusMenuOpen && (
            <div className="absolute left-0 top-8 z-50 w-48 rounded-xl border border-zinc-800 bg-zinc-900 p-1.5 shadow-2xl">
              {menuItems.map((m) => (
                <button
                  key={m.kind}
                  type="button"
                  onClick={() => {
                    setPlusMenuOpen(false);
                    void openRightTab(m.kind, m.label);
                  }}
                  className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-left text-xs text-zinc-200 hover:bg-zinc-800"
                >
                  {m.icon}
                  <span>{m.label}</span>
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Tab Body */}
      <div className="flex-1 overflow-auto">
        {activeTab?.kind === 'files' && (
          <FilesTreePanel
            wsConnected={wsConnected}
            runnerId={selectedRunnerId}
            listDir={listWorkspaceDir}
            searchFiles={searchWorkspaceFiles}
            onOpenFile={(rel) => void openFileInPreviewTab(rel)}
          />
        )}

        {activeTab?.kind === 'file_preview' && activeTab.preview && (
          <FilePreviewPanel preview={activeTab.preview} onAddRef={appendComposerRef} />
        )}

        {activeTab?.kind === 'review' && (
          <GitReviewPanel
            summary={gitSummary}
            onRefresh={() => void refreshGitSummary(true)}
            onOpenFile={(rel) => void openFileInPreviewTab(rel)}
          />
        )}

        {activeTab?.kind === 'terminal' && (
          <GhosttyTerminal ptyId={activeTab.ptyId || ''} runnerId={selectedRunnerId} />
        )}

        {activeTab?.kind === 'side_chat' && (
          <SideConversationPanel runnerId={selectedRunnerId} sideSessionId={activeTab.sideSessionId || ''} />
        )}
      </div>
    </aside>
  );
}

function FilesTreePanel({
  wsConnected,
  runnerId,
  listDir,
  searchFiles,
  onOpenFile,
}: {
  wsConnected: boolean;
  runnerId: string;
  listDir: (rel: string) => Promise<DirEntry[]>;
  searchFiles: (q: string) => Promise<string[]>;
  onOpenFile: (relPath: string) => void;
}) {
  const [rootEntries, setRootEntries] = useState<DirEntry[]>([]);
  const [expanded, setExpanded] = useState<Record<string, DirEntry[]>>({});
  const [query, setQuery] = useState('');
  const [searchResults, setSearchResults] = useState<string[]>([]);

  useEffect(() => {
    if (wsConnected && runnerId) {
      void listDir('').then(setRootEntries).catch(() => {});
    }
  }, [wsConnected, runnerId, listDir]);

  useEffect(() => {
    if (!query.trim()) {
      setSearchResults([]);
      return;
    }
    const timer = setTimeout(() => {
      void searchFiles(query).then(setSearchResults).catch(() => {});
    }, 180);
    return () => clearTimeout(timer);
  }, [query, searchFiles]);

  const toggleFolder = async (relPath: string) => {
    if (expanded[relPath]) {
      const cp = { ...expanded };
      delete cp[relPath];
      setExpanded(cp);
      return;
    }
    const children = await listDir(relPath);
    setExpanded((prev) => ({ ...prev, [relPath]: children }));
  };

  const renderNodes = (entries: DirEntry[], depth: number) =>
    entries.map((entry) => {
      const isDir = entry.kind === 'dir';
      const isOpen = Boolean(expanded[entry.rel_path]);
      return (
        <div key={entry.rel_path}>
          <div
            style={{ paddingLeft: `${depth * 14 + 10}px` }}
            onClick={() => (isDir ? void toggleFolder(entry.rel_path) : onOpenFile(entry.rel_path))}
            className={`flex cursor-pointer items-center justify-between py-1 pr-3 text-xs hover:bg-zinc-900 ${
              entry.git_ignored ? 'opacity-45' : 'text-zinc-200'
            }`}
          >
            <span className="flex items-center gap-1.5 truncate">
              {isDir ? (
                isOpen ? (
                  <ChevronDown className="h-3.5 w-3.5 shrink-0 text-zinc-500" />
                ) : (
                  <ChevronRight className="h-3.5 w-3.5 shrink-0 text-zinc-500" />
                )
              ) : (
                <FileCode className="h-3.5 w-3.5 shrink-0 text-blue-400/80" />
              )}
              <span className="truncate">{entry.name}</span>
            </span>
            {entry.git_status && <span className="font-mono text-[10px] text-amber-400">{entry.git_status}</span>}
          </div>
          {isDir && isOpen && expanded[entry.rel_path] && renderNodes(expanded[entry.rel_path], depth + 1)}
        </div>
      );
    });

  return (
    <div className="flex flex-col">
      <div className="border-b border-zinc-800 p-2">
        <input
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索文件..."
          className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-200 placeholder-zinc-500 outline-none focus:border-blue-500"
        />
      </div>

      {query.trim() ? (
        <div className="py-1">
          {searchResults.map((rel) => (
            <div
              key={rel}
              onClick={() => onOpenFile(rel)}
              className="cursor-pointer truncate px-3 py-1.5 font-mono text-xs text-zinc-300 hover:bg-zinc-900"
            >
              {rel}
            </div>
          ))}
          {searchResults.length === 0 && <div className="p-4 text-center text-xs text-zinc-500">无匹配文件</div>}
        </div>
      ) : (
        <div className="py-1">{renderNodes(rootEntries, 0)}</div>
      )}
    </div>
  );
}

function FilePreviewPanel({
  preview,
  onAddRef,
}: {
  preview: FilePreview;
  onAddRef: (ref: string) => void;
}) {
  const [rawView, setRawView] = useState(false);

  if (preview.preview_as === 'binary') {
    return (
      <div className="p-6 text-center text-xs text-zinc-400">
        <p className="font-medium text-zinc-200">{preview.rel_path}</p>
        <p className="mt-1">Binary file ({preview.total_size} bytes) — preview disabled</p>
        {preview.sha256 && <p className="mt-1 font-mono text-[11px] text-zinc-500">SHA-256: {preview.sha256.slice(0, 16)}...</p>}
      </div>
    );
  }

  if (preview.preview_as === 'image') {
    return (
      <div className="flex flex-col items-center p-4">
        <img
          src={`data:${preview.mime_type || 'image/png'};base64,${preview.content || ''}`}
          alt={preview.name}
          className="max-h-[70vh] max-w-full rounded border border-zinc-800 object-contain"
        />
        <span className="mt-2 text-xs text-zinc-500">
          {preview.rel_path} ({preview.total_size} bytes)
        </span>
      </div>
    );
  }

  const lines = (preview.content || '').split('\n');

  return (
    <div className="flex flex-col">
      <div className="flex items-center justify-between border-b border-zinc-800 bg-zinc-900/40 px-3 py-1.5 text-xs">
        <span className="truncate font-mono text-zinc-300">{preview.rel_path}</span>
        <div className="flex items-center gap-2">
          {preview.preview_as === 'markdown' && (
            <button
              type="button"
              onClick={() => setRawView((v) => !v)}
              className="rounded border border-zinc-700 px-2 py-0.5 text-[11px] text-zinc-300 hover:bg-zinc-800"
            >
              {rawView ? 'Rendered' : 'Source'}
            </button>
          )}
          <button
            type="button"
            onClick={() => onAddRef(`@${preview.rel_path}`)}
            className="rounded bg-blue-600/20 px-2 py-0.5 text-[11px] text-blue-300 hover:bg-blue-600/30"
          >
            @ Cite in chat
          </button>
        </div>
      </div>

      {preview.truncated && (
        <div className="bg-amber-500/10 px-3 py-1 text-[11px] text-amber-300">
          Large file ({preview.total_size} bytes) — showing first 256 KB
        </div>
      )}

      {preview.preview_as === 'markdown' && !rawView ? (
        <div className="prose prose-invert max-w-none p-4 text-xs whitespace-pre-wrap text-zinc-200">
          {preview.content}
        </div>
      ) : (
        <div className="overflow-x-auto py-2 font-mono text-xs leading-5">
          {lines.map((line, idx) => (
            <div key={idx} className="flex hover:bg-zinc-900/70">
              <span
                onClick={() => onAddRef(`@${preview.rel_path}:L${idx + 1}`)}
                title="Click line number to cite in chat"
                className="w-11 shrink-0 cursor-pointer select-none pr-3 text-right text-zinc-600 hover:text-blue-400"
              >
                {idx + 1}
              </span>
              <span className="whitespace-pre pr-4 text-zinc-200">{line}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function GitReviewPanel({
  summary,
  onRefresh,
  onOpenFile,
}: {
  summary: ReturnType<typeof useNexStore.getState>['gitSummary'];
  onRefresh: () => void;
  onOpenFile: (rel: string) => void;
}) {
  const [splitView, setSplitView] = useState(false);

  useEffect(() => {
    onRefresh();
  }, [onRefresh]);

  if (!summary || !summary.is_git_repo) {
    return <div className="p-6 text-center text-xs text-zinc-500">当前工作区未检测到 Git 仓库</div>;
  }

  return (
    <div className="flex flex-col">
      <div className="flex items-center justify-between border-b border-zinc-800 px-3 py-2 text-xs">
        <div className="flex items-center gap-2">
          <span className="rounded bg-zinc-800 px-2 py-0.5 font-mono text-zinc-300">{summary.branch || 'HEAD'}</span>
          <span className="font-mono text-emerald-400">+{summary.additions}</span>
          <span className="font-mono text-rose-400">-{summary.deletions}</span>
        </div>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => setSplitView((v) => !v)}
            className="rounded border border-zinc-700 px-2 py-0.5 text-[11px] text-zinc-300 hover:bg-zinc-800"
          >
            {splitView ? 'Unified' : 'Split'}
          </button>
          <button
            type="button"
            onClick={onRefresh}
            className="rounded border border-zinc-700 px-2 py-0.5 text-[11px] text-zinc-300 hover:bg-zinc-800"
          >
            Refresh
          </button>
        </div>
      </div>

      {(!summary.files || summary.files.length === 0) && (
        <div className="p-6 text-center text-xs text-zinc-500">Working tree clean</div>
      )}

      <div className="divide-y divide-zinc-800">
        {(summary.files || []).map((f) => (
          <div key={f.rel_path} className="p-2.5">
            <div className="flex items-center justify-between text-xs">
              <button
                type="button"
                onClick={() => onOpenFile(f.rel_path)}
                className="truncate font-mono font-medium text-zinc-200 hover:text-blue-400"
              >
                [{f.status}] {f.rel_path}
              </button>
              <span className="shrink-0 font-mono text-[11px]">
                <span className="text-emerald-400">+{f.additions}</span>{' '}
                <span className="text-rose-400">-{f.deletions}</span>
              </span>
            </div>
            {f.diff && (
              <pre className="mt-2 overflow-x-auto rounded-lg border border-zinc-800 bg-zinc-900/60 p-2 font-mono text-[11px] leading-5">
                {f.diff.split('\n').map((dl, i) => {
                  const cls = dl.startsWith('+')
                    ? 'bg-emerald-500/10 text-emerald-300'
                    : dl.startsWith('-')
                      ? 'bg-rose-500/10 text-rose-300'
                      : dl.startsWith('@@')
                        ? 'text-blue-400'
                        : 'text-zinc-400';
                  return (
                    <div key={i} className={`px-1 ${cls}`}>
                      {dl}
                    </div>
                  );
                })}
              </pre>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

function SideConversationPanel({ runnerId, sideSessionId }: { runnerId: string; sideSessionId: string }) {
  const { streams, selectedProviderId, selectedModelId } = useNexStore();
  const [input, setInput] = useState('');
  const st = streams[sideSessionId];

  const sendSide = () => {
    const text = input.trim();
    if (!text || !runnerId || !sideSessionId) return;
    import('../core/wsClient').then(({ wsClient }) => {
      wsClient.sendEnvelope('client.subscribe', { session_id: sideSessionId, last_seen_seq: st?.lastSeq || 0 });
      wsClient.sendEnvelope(
        'session.prompt',
        {
          session_id: sideSessionId,
          runner_id: runnerId,
          provider_id: selectedProviderId,
          model_id: selectedModelId,
          permission_mode: 'read_only',
          text,
        },
        { session_id: sideSessionId, runner_id: runnerId },
      );
    });
    setInput('');
  };

  return (
    <div className="flex h-full flex-col">
      <div className="flex-1 space-y-3 overflow-auto p-3 text-xs">
        {(!st || st.items.length === 0) && (
          <p className="text-zinc-500">旁路问答（只读模式，不打断左侧主对话上下文）</p>
        )}
        {st?.items.map((m) => (
          <div
            key={m.id}
            className={`rounded-lg p-2.5 whitespace-pre-wrap ${
              m.role === 'user' ? 'bg-blue-600/20 text-blue-100' : 'bg-zinc-900 text-zinc-200'
            }`}
          >
            {m.text}
          </div>
        ))}
      </div>
      <div className="border-t border-zinc-800 p-2 flex gap-1.5">
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && sendSide()}
          placeholder="Ask side question..."
          className="flex-1 rounded-lg border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-100 outline-none"
        />
        <button
          type="button"
          onClick={sendSide}
          className="rounded-lg bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-500"
        >
          Send
        </button>
      </div>
    </div>
  );
}
