# Spec: Memory file preview in settings

Status: accepted
Owner: packages/ui (settings memory section)
Related: `docs/desktop-vs-web-capability-gap.md` (A-class gap: memory detail visibility)

## Behavior

- Settings → Memory file list rows are clickable on both desktop and web.
  Clicking a row opens an in-settings preview dialog rendered as a
  right-anchored panel over the settings layer.
- The preview is self-contained inside the settings page. It does NOT use the
  workspace side pane: the settings page is a full-screen overlay above the
  workspace shell, so a side pane opened underneath would stay hidden until
  the user manually exits settings.
- Desktop keeps the existing "open with editor / file manager" buttons
  (`WorkspaceEditorButtonGroup`); the preview is additive. On web those
  buttons are already absent, so the preview is the primary read surface.
- Clicking a different file while the dialog is open switches the preview
  content to that file.

## Ownership & data flow

- Single data owner: `IMemoryService` on the local Host (desktop host process
  or `nex serve` server process). The renderer never reads memory files
  directly.
- Fetch path: `MemoryPreviewDialog` calls
  `memoryService.readProjectMemoryFile({ workspaceId, fileName })` when the
  target changes; the result is render-local state. A stale response after
  switching files is discarded by request id.
- Render path: `MessageResponse` (same markdown stack as conversation
  messages), theme/code-preview settings come from the shared UI store.

## Invariants

- Read-only: no UI in the preview writes memory content.
- Backend contract unchanged: size limit (5MB) and
  `PROJECT_MEMORY_PREVIEW_LIMIT_EXCEEDED` error surface as a user-visible
  message (no retry for oversized; retry button for other errors).
- The dialog is controlled by `MemorySettingsViewer` local state
  (`previewTarget`); closing resets it to null. No global store, no side pane
  tab, no navigation side effects.

## Failure semantics

- Load error (including oversized file): inline error state inside the
  dialog; dialog stays open. Retry button re-reads; oversized shows only a
  close affordance (via dialog close).
- File deleted between listing and preview: `ENOENT` renders the same inline
  error state.

## Acceptance scenarios

1. Desktop: click a memory file row in settings → right-side preview panel
   opens with rendered markdown; editor buttons still work.
2. Web: same behavior (preview panel is the primary read surface).
3. With the preview open, clicking another file row switches the preview
   content without closing the dialog.
4. Oversized file (> 5MB) → inline error message, no retry button.
5. Search filter still applies to the list; preview is unaffected.
