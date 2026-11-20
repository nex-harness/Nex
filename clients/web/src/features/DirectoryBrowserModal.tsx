import {
  ArrowUp,
  Check,
  ChevronRight,
  Eye,
  EyeOff,
  Folder,
  FolderOpen,
  FolderSymlink,
  Home,
  LoaderCircle,
  RefreshCw,
  Search,
  Server,
  X,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useNexStore } from '../core/store';
import { BrowseDirEntry } from '../core/types';
import { formatMsg } from '../i18n/messages';

interface DirectoryBrowserModalProps {
  open: boolean;
  initialRunnerId?: string;
  initialPath?: string;
  onSelect: (runnerId: string, path: string) => void;
  onClose: () => void;
}

function splitBreadcrumbs(fullPath: string): Array<{ label: string; path: string }> {
  if (!fullPath) return [];
  // Handle Windows drive roots (e.g., C:\Users\dev) and POSIX roots (/Users/dev)
  const isWindows = /^[a-zA-Z]:[\\/]/.test(fullPath);
  if (isWindows) {
    const normalized = fullPath.replace(/\//g, '\\').replace(/\\+$/, '');
    const parts = normalized.split('\\').filter(Boolean);
    if (parts.length === 0) return [];
    const crumbs: Array<{ label: string; path: string }> = [];
    let acc = '';
    for (let i = 0; i < parts.length; i++) {
      acc = i === 0 ? `${parts[0]}\\` : `${acc.replace(/\\$/, '')}\\${parts[i]}`;
      crumbs.push({ label: parts[i], path: acc });
    }
    return crumbs;
  }

  const normalized = fullPath.replace(/\/+$/, '') || '/';
  if (normalized === '/') return [{ label: '/', path: '/' }];
  const parts = normalized.split('/').filter(Boolean);
  const crumbs: Array<{ label: string; path: string }> = [{ label: '/', path: '/' }];
  let acc = '';
  for (const part of parts) {
    acc += `/${part}`;
    crumbs.push({ label: part, path: acc });
  }
  return crumbs;
}

function leafName(pathText: string): string {
  return pathText.split(/[\\/]/).filter(Boolean).pop() || pathText;
}

export function DirectoryBrowserModal({
  open,
  initialRunnerId,
  initialPath,
  onSelect,
  onClose,
}: DirectoryBrowserModalProps) {
  const locale = useNexStore((s) => s.locale);
  const runners = useNexStore((s) => s.runners);
  const selectedRunnerId = useNexStore((s) => s.selectedRunnerId);
  const recentWorkspaces = useNexStore((s) => s.recentWorkspaces);
  const browseRunnerDirs = useNexStore((s) => s.browseRunnerDirs);

  const [activeRunnerId, setActiveRunnerId] = useState(initialRunnerId || selectedRunnerId || runners[0]?.id || '');
  const [currentPath, setCurrentPath] = useState('');
  const [parentPath, setParentPath] = useState('');
  const [homePath, setHomePath] = useState('');
  const [entries, setEntries] = useState<BrowseDirEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [showHidden, setShowHidden] = useState(false);
  const [filterQuery, setFilterQuery] = useState('');

  const reqSeqRef = useRef(0);
  const listRef = useRef<HTMLDivElement | null>(null);

  const activeRunner = useMemo(
    () => runners.find((r) => r.id === activeRunnerId) || runners[0],
    [runners, activeRunnerId],
  );

  const loadDir = useCallback(
    async (runnerId: string, dirPath: string, includeHidden: boolean) => {
      if (!runnerId) {
        setError(formatMsg(locale, 'dirBrowser.noRunner'));
        setEntries([]);
        setLoading(false);
        return;
      }
      const seq = ++reqSeqRef.current;
      setLoading(true);
      setError('');
      try {
        const res = await browseRunnerDirs(runnerId, dirPath, includeHidden);
        if (seq !== reqSeqRef.current) return;
        setCurrentPath(res.current_path || dirPath);
        setParentPath(res.parent_path || '');
        if (res.home_path) setHomePath(res.home_path);
        setEntries(res.entries || []);
        setFilterQuery('');
        listRef.current?.scrollTo({ top: 0 });
      } catch (err) {
        if (seq !== reqSeqRef.current) return;
        setError((err as Error).message || (formatMsg(locale, 'dirBrowser.failedToReadDirectory')));
      } finally {
        if (seq === reqSeqRef.current) setLoading(false);
      }
    },
    [browseRunnerDirs, locale],
  );

  useEffect(() => {
    if (!open) return;
    const rid = initialRunnerId || selectedRunnerId || runners[0]?.id || '';
    setActiveRunnerId(rid);
    const startPath = initialPath || runners.find((r) => r.id === rid)?.workspace_default || '';
    void loadDir(rid, startPath, showHidden);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, initialRunnerId, initialPath]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [open, onClose]);

  const filteredEntries = useMemo(() => {
    const q = filterQuery.trim().toLowerCase();
    if (!q) return entries;
    return entries.filter((e) => e.name.toLowerCase().includes(q));
  }, [entries, filterQuery]);

  const breadcrumbs = useMemo(() => splitBreadcrumbs(currentPath), [currentPath]);

  const quickShortcuts = useMemo(() => {
    const list: Array<{ label: string; path: string; kind: 'home' | 'default' | 'recent' }> = [];
    const seen = new Set<string>();
    const push = (label: string, path: string, kind: 'home' | 'default' | 'recent') => {
      if (!path || seen.has(path)) return;
      seen.add(path);
      list.push({ label, path, kind });
    };
    if (homePath) push(formatMsg(locale, 'dirBrowser.home'), homePath, 'home');
    if (activeRunner?.workspace_default) {
      push(leafName(activeRunner.workspace_default), activeRunner.workspace_default, 'default');
    }
    for (const item of recentWorkspaces) {
      if (item.runnerId === activeRunnerId && item.path) {
        push(leafName(item.path), item.path, 'recent');
      }
      if (list.length >= 6) break;
    }
    return list;
  }, [homePath, activeRunner?.workspace_default, recentWorkspaces, activeRunnerId, locale]);

  if (!open) return null;

  const toggleHidden = () => {
    const next = !showHidden;
    setShowHidden(next);
    if (activeRunnerId) {
      void loadDir(activeRunnerId, currentPath, next);
    }
  };

  const switchRunner = (rid: string) => {
    setActiveRunnerId(rid);
    const runnerObj = runners.find((r) => r.id === rid);
    void loadDir(rid, runnerObj?.workspace_default || '', showHidden);
  };

  const modalNode = (
    <div
      data-testid="directory-browser-modal"
      className="zcode-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 backdrop-blur-[2px]"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className="zcode-dialog-content flex max-h-[82vh] w-full max-w-xl flex-col overflow-hidden rounded-2xl border border-border bg-popover text-foreground shadow-xl">
        {/* Header */}
        <div className="flex items-center justify-between gap-3 border-b border-border px-5 py-3.5">
          <div className="flex min-w-0 items-center gap-2">
            <FolderOpen className="size-4 shrink-0 text-foreground-subtle" />
            <h3 className="truncate text-ui-lg font-semibold text-foreground">
              {formatMsg(locale, 'dirBrowser.title')}
            </h3>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={formatMsg(locale, 'common.close')}
            className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        </div>

        {/* Runner Switcher (only shown if multiple runners exist) */}
        {runners.length > 1 && (
          <div className="flex items-center gap-1.5 overflow-x-auto border-b border-border bg-popover-header/60 px-5 py-2">
            <Server className="size-3.5 shrink-0 text-foreground-subtlest" />
            {runners.map((r) => {
              const active = r.id === activeRunnerId;
              return (
                <button
                  key={r.id}
                  type="button"
                  onClick={() => switchRunner(r.id)}
                  className={`flex h-6 shrink-0 items-center gap-1.5 rounded-full px-2.5 text-ui-xs transition-colors ${
                    active
                      ? 'bg-selected font-medium text-foreground'
                      : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
                  }`}
                >
                  <span
                    className={`size-1.5 rounded-full ${r.status === 'online' ? 'bg-success' : 'bg-foreground-subtlest'}`}
                  />
                  <span>{r.name}</span>
                </button>
              );
            })}
          </div>
        )}

        {/* Breadcrumb & Quick Actions */}
        <div className="space-y-2 border-b border-border px-5 py-3">
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              disabled={!parentPath || loading}
              onClick={() => parentPath && void loadDir(activeRunnerId, parentPath, showHidden)}
              title={formatMsg(locale, 'dirBrowser.upOneLevel')}
              className="flex size-7 shrink-0 items-center justify-center rounded-lg border border-border bg-card text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
            >
              <ArrowUp className="size-3.5" />
            </button>
            <button
              type="button"
              disabled={!homePath || loading}
              onClick={() => homePath && void loadDir(activeRunnerId, homePath, showHidden)}
              title={formatMsg(locale, 'dirBrowser.home')}
              className="flex size-7 shrink-0 items-center justify-center rounded-lg border border-border bg-card text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
            >
              <Home className="size-3.5" />
            </button>

            {/* Interactive Breadcrumbs */}
            <div className="flex h-7 min-w-0 flex-1 items-center gap-0.5 overflow-x-auto rounded-lg border border-border bg-surface px-2 font-mono text-ui-xs [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
              {breadcrumbs.length === 0 ? (
                <span className="text-foreground-subtlest">{currentPath || '…'}</span>
              ) : (
                breadcrumbs.map((crumb, idx) => {
                  const isLast = idx === breadcrumbs.length - 1;
                  return (
                    <span key={crumb.path} className="flex shrink-0 items-center gap-0.5">
                      {idx > 0 && <ChevronRight className="size-3 text-foreground-subtlest" />}
                      <button
                        type="button"
                        onClick={() => !isLast && void loadDir(activeRunnerId, crumb.path, showHidden)}
                        className={`rounded px-1 py-0.5 transition-colors ${
                          isLast
                            ? 'font-medium text-foreground'
                            : 'text-foreground-subtle hover:bg-hover hover:text-foreground'
                        }`}
                      >
                        {crumb.label}
                      </button>
                    </span>
                  );
                })
              )}
            </div>

            <button
              type="button"
              onClick={toggleHidden}
              aria-pressed={showHidden}
              title={formatMsg(locale, showHidden ? 'dirBrowser.hideHidden' : 'dirBrowser.showHidden')}
              className={`flex size-7 shrink-0 items-center justify-center rounded-lg border transition-colors ${
                showHidden
                  ? 'border-border-hover bg-selected text-foreground'
                  : 'border-border bg-card text-foreground-subtle hover:bg-hover hover:text-foreground'
              }`}
            >
              {showHidden ? <Eye className="size-3.5" /> : <EyeOff className="size-3.5" />}
            </button>
            <button
              type="button"
              disabled={loading}
              onClick={() => void loadDir(activeRunnerId, currentPath, showHidden)}
              title={formatMsg(locale, 'common.refresh')}
              className="flex size-7 shrink-0 items-center justify-center rounded-lg border border-border bg-card text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground disabled:opacity-40"
            >
              <RefreshCw className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            </button>
          </div>

          {/* Quick-jump chips + filter */}
          <div className="flex flex-wrap items-center justify-between gap-2">
            {quickShortcuts.length > 0 && (
              <div className="flex min-w-0 flex-wrap items-center gap-1">
                {quickShortcuts.map((sc) => {
                  const isCurrent = sc.path === currentPath;
                  return (
                    <button
                      key={sc.path}
                      type="button"
                      title={sc.path}
                      onClick={() => void loadDir(activeRunnerId, sc.path, showHidden)}
                      className={`inline-flex h-6 max-w-[140px] items-center gap-1 rounded-full border px-2 text-ui-xs transition-colors ${
                        isCurrent
                          ? 'border-border-hover bg-selected font-medium text-foreground'
                          : 'border-border bg-card text-foreground-subtle hover:border-border-hover hover:text-foreground'
                      }`}
                    >
                      {sc.kind === 'home' ? (
                        <Home className="size-3 shrink-0" />
                      ) : (
                        <Folder className="size-3 shrink-0" />
                      )}
                      <span className="truncate">{sc.label}</span>
                    </button>
                  );
                })}
              </div>
            )}

            <div className="flex h-7 w-44 shrink-0 items-center gap-1.5 rounded-lg border border-border bg-input px-2">
              <Search className="size-3 shrink-0 text-foreground-subtlest" />
              <input
                value={filterQuery}
                onChange={(e) => setFilterQuery(e.target.value)}
                placeholder={formatMsg(locale, 'dirBrowser.filterPlaceholder')}
                className="min-w-0 flex-1 bg-transparent text-ui-xs text-foreground placeholder-foreground-subtlest"
              />
              {filterQuery && (
                <button
                  type="button"
                  onClick={() => setFilterQuery('')}
                  className="text-foreground-subtlest hover:text-foreground"
                >
                  <X className="size-3" />
                </button>
              )}
            </div>
          </div>
        </div>

        {/* Directory Listing */}
        <div ref={listRef} className="min-h-[240px] flex-1 overflow-y-auto p-2">
          {error && (
            <div className="mb-2 rounded-xl border border-destructive/20 bg-destructive-surface px-3 py-2 text-ui-sm text-destructive-surface-fg">
              {error}
            </div>
          )}

          {parentPath && !filterQuery && (
            <button
              type="button"
              onClick={() => void loadDir(activeRunnerId, parentPath, showHidden)}
              className="flex h-9 w-full items-center gap-2.5 rounded-lg px-3 text-left text-ui-base text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
            >
              <Folder className="size-4 shrink-0 text-foreground-subtlest" />
              <span className="font-mono">..</span>
            </button>
          )}

          {loading ? (
            <div className="flex h-40 items-center justify-center gap-2 text-ui-sm text-foreground-subtle">
              <LoaderCircle className="size-4 animate-spin" />
              <span>{formatMsg(locale, 'dirBrowser.loading')}</span>
            </div>
          ) : filteredEntries.length === 0 ? (
            <div className="flex h-40 items-center justify-center text-ui-sm text-foreground-subtlest">
              {formatMsg(locale, 'dirBrowser.empty')}
            </div>
          ) : (
            <div className="space-y-0.5">
              {filteredEntries.map((entry) => (
                <div
                  key={entry.path}
                  data-testid={`dir-entry-${entry.name}`}
                  className="group flex h-9 items-center justify-between gap-2 rounded-lg px-3 transition-colors hover:bg-hover"
                >
                  <button
                    type="button"
                    onClick={() => void loadDir(activeRunnerId, entry.path, showHidden)}
                    className="flex min-w-0 flex-1 items-center gap-2.5 text-left text-ui-base text-foreground"
                  >
                    {entry.is_symlink ? (
                      <FolderSymlink className="size-4 shrink-0 text-foreground-subtle" />
                    ) : (
                      <Folder className="size-4 shrink-0 text-foreground-subtle" />
                    )}
                    <span className="truncate">{entry.name}</span>
                  </button>
                  <button
                    type="button"
                    data-testid={`dir-pick-${entry.name}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      onSelect(activeRunnerId, entry.path);
                    }}
                    className="flex h-6 shrink-0 items-center gap-1 rounded-md border border-border bg-card px-2 text-ui-xs font-medium text-foreground-subtle opacity-0 transition-all hover:border-border-hover hover:bg-selected hover:text-foreground group-hover:opacity-100 focus-visible:opacity-100"
                  >
                    <Check className="size-3" />
                    <span>{formatMsg(locale, 'dirBrowser.select')}</span>
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between gap-3 border-t border-border bg-popover-header/60 px-5 py-3">
          <div className="min-w-0 flex-1">
            <div className="truncate font-mono text-ui-xs text-foreground-subtle" title={currentPath}>
              {currentPath || '—'}
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <button
              type="button"
              onClick={onClose}
              className="h-8 rounded-lg border border-border px-3 text-ui-sm text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
            >
              {formatMsg(locale, 'common.cancel')}
            </button>
            <button
              type="button"
              data-testid="dir-browser-confirm"
              disabled={!currentPath || !activeRunnerId || loading}
              onClick={() => currentPath && activeRunnerId && onSelect(activeRunnerId, currentPath)}
              className="h-8 rounded-lg bg-primary px-3.5 text-ui-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-40"
            >
              {formatMsg(locale, 'dirBrowser.selectCurrent')}
            </button>
          </div>
        </div>
      </div>
    </div>
  );

  return typeof document !== 'undefined' ? createPortal(modalNode, document.body) : modalNode;
}
