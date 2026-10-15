# Spec: Side pane file browser tab

Status: accepted
Owner: packages/ui (app-shell side pane)

## Behavior

- The side pane "Open tab" launcher (and the "+" dropdown) gets a **Files** entry on desktop and web.
- Clicking it opens a singleton `files` tab. The tab renders the existing `WorkspaceFileTree` for the
  current workspace: lazy directory expansion, search, changed-only filter, git status, context menu.
- Clicking a file opens it through the existing `onOpenCodeViewer` path, i.e. a `code-viewer` tab.
  Supported formats are exactly what the code viewer already supports (text/code, markdown, images,
  audio/video, PDF, PPTX). Unsupported binaries keep the code viewer's existing fallback state.
- The `files` tab does not embed its own preview. Preview stays in `code-viewer` tabs so tab
  reuse, close, reopen and memory restore keep one implementation.
- The file tree inside the tab has no "Back to tasks" button (that button belongs to the sidebar
  overlay, where the tree replaces the task list).
- The row of the file shown by the active `code-viewer` tab is highlighted/revealed in the tree
  (`activePreviewPath`).

## Ownership & data flow

- Tab state owner: `WorkspaceSidePaneState` in `lib/workspaceSidePane.ts`, mutated only via
  `activateFilesSidePane` from `useAppPanels.handleOpenFilesTab`. Same path as `developer-tools`.
- Data owner: `IFileService` on the workspace host (local Host, desktop Host, or `nex serve`). The tab
  reads through `useWorkspaceFileTreeData`; no new service, protocol, or persistence.
- `files` is a **workspace-global** tab type (like `git`, `developer-tools`, `treemapping`): it is
  visible from every conversation of the same workspace, id is fixed (`"files"`), so a second open
  activates the existing tab.

## Invariants

- At most one `files` tab per workspace pane state.
- Opening a file from the tab never changes the active workspace and never mutates the tree.
- Tab is hidden in office mode only if the sidebar file tree is (office mode has no terminal/review
  either); otherwise available everywhere the side pane is.

## Failure semantics

- Directory read failure, remote disconnect, etc. are handled inside `WorkspaceFileTree` (existing
  notice row + retry). The tab itself has no extra error state.

## Acceptance scenarios

1. Open side pane → launcher lists Files → click → tab "Files" with the workspace tree.
2. Click `README.md` → a code-viewer tab with rendered preview opens; the Files tab stays.
3. Click an image / PDF → corresponding preview opens in a code-viewer tab.
4. Click Files again (launcher or dropdown) → activates the existing tab, no duplicate.
5. Switch conversation in the same workspace → Files tab is still there.
6. Web (no desktop file manager): tree works; "reveal in file manager" absent.
7. Search box filters via workspace file index; changed-only toggle still works.
