# Choosing folders on a machine — spec

Status: fixes two bugs on `feature/next`, found after the SSH-install deploy.

## Problems

1. **A new machine never shows up in the sidebar.** The sidebar lists workspace tabs
   (folders the user opened), not machines. When the install wizard finished, it reloaded
   the machine catalog (`/api/server-info`) and closed without opening anything. If the
   new machine already had models, the wizard's Models step skipped to "Ready", so the
   install looked like "the dialog closed and nothing happened".
2. **A machine only works in its home directory.** The runner's served folders come from
   `NEX_RUNNER_WORKSPACES` (the SSH installer writes `$HOME`), fixed at startup in three
   places:
   - the `hello` frame,
   - the channel guard (`workspace not served by this runner`),
   - the file-tools root check (`path is outside this runner's workspaces`).

   The directory browser cannot browse a chosen machine either. It sends a made-up
   `runner:<id>:/` identity, which routes to the machine, but the runner then rejects the
   path.

## Decisions

1. **The runner owns its folder list, persisted on the machine.**
   `<dataDir>/workspaces.json` holds `{version: 1, folders: [{path, addedAt}]}`. The
   effective served set is the env roots (`NEX_RUNNER_WORKSPACES`, never removable from
   the UI) plus the persisted folders, de-duplicated by the resolved path. The file is
   written atomically (temp file + rename). There is one owner: the runner's
   `WorkspaceRoots` object. The channel guard, the file-tools root check, `nex-task`'s
   workspace-less lists and the hello all read from it.
2. **A machine-level browse scope.** Users must see folders outside the served roots in
   order to add them. The runner accepts a `runner:<id>:~` machine identity
   (`workspacePath: "~"`) on `file.readdir`, `file.stat` and `system.info` only:
   - absolute paths are allowed anywhere the runner's OS user can read;
   - only directories are listed;
   - no other method accepts the machine scope.

   Everything else (sessions, terminals, git, file contents, writes) still requires a
   served folder. The server routes the machine identity to that runner, as it does for
   any `runner:` identity.

3. **Adding a folder** is a runner call, `runner-workspaces.add({path})`, made with the
   machine target. It validates the path: absolute, exists, is a directory, readable,
   resolved through realpath. It persists the folder, updates `WorkspaceRoots`, and sends
   a `workspaces` frame on the link: `{t: "workspaces", workspaces: [{path, label}]}`. It
   returns `{path, label, workspaceIdentity}`.
   - The server replaces the runner's catalogued workspaces from the frame. The identity
     and path tables are updated under the same lock, and `/api/server-info` subscribers
     are notified.
   - Adding a folder that is already served is a no-op that returns the existing entry.
   - `runner-workspaces.remove({path})` removes a persisted folder (env roots are
     refused). It is not exposed in the UI in this batch.
   - `runner-workspaces.list()` returns the served folders with `{path, label,
removable}`.

   Unknown frames are ignored by old servers, so a new runner on an old server keeps its
   startup folders.

4. **Machine-first open flow (web).** "Open folder" opens the machine picker.
   - Each machine group lists its served folders, plus a **"Choose folder…"** action
     that opens the directory browser on that machine's machine scope, starting at the
     machine's home.
   - Selecting a folder in the browser calls `add`, then opens it like a picked
     workspace (`handleSelectProject(path, workspaceIdentity)`). The folder becomes a
     workspace tab in the sidebar.
   - Opening an already-served folder from the list behaves as before.
   - The directory browser's machine selector now uses the machine scope rather than the
     fake `/` identity.
5. **The install wizard ends by opening the new machine.** After the Models step (Done,
   Later, or Open model settings), the wizard opens the new machine's home folder as a
   workspace tab. The installer serves home, and the sidebar then shows the machine.
   - The Models step no longer skips itself when models are ready. It shows "Ready, N
     models" with **Open workspace** (the default action) and **Choose folder…**, which
     opens the directory browser on the new machine.
   - Closing the dialog with the X still opens nothing (the user cancelled).
6. **Labels**: a folder's label is its basename. The server keeps the existing
   `"<runnerName>: <basename>"` format for `/api/server-info`.

## Security boundary

Being allowed to use a runner already grants a shell (terminals). The machine scope only
adds directory listing and stat, which the user's shell can already do, so it does not
widen access. Adding a folder is an explicit user action. The runner remains the
enforcement point: every non-browse call is checked against the live `WorkspaceRoots`.

## Event order

```
UI picker "Choose folder…" (machine M)
  └▶ DirectoryBrowser(scope runner:M:~) ── file.readdir / system.info ─▶ server ─▶ runner M (browse-only)
UI selects /path
  └▶ runner-workspaces.add({path}) ─▶ server (identity runner:M:~) ─▶ runner M
        runner: validate → workspaces.json (atomic) → WorkspaceRoots.add
        runner ─▶ link frame {t:"workspaces"} ─▶ server registry.UpdateWorkspaces(M)
        server: identities/paths swapped under lock → notify → /api/server-info
        runner ─▶ result {path, label, workspaceIdentity}
UI handleSelectProject(path, runner:M:/path) ─▶ workspace tab → sidebar
```

Owners:

- The runner owns which folders it serves; it persists them and enforces the list.
- The server only catalogues what the runner reports, for routing and
  `/api/server-info`.
- The UI owns tabs only.

## Acceptance

- Runner unit:
  - `WorkspaceRoots` merges env and persisted folders and survives a restart.
  - `add` validates the path: it rejects relative paths, files and missing paths, and
    resolves symlinks with realpath.
  - The guard accepts a newly added folder without a restart.
  - The machine scope allows `readdir`, `stat` and `info` only, and only lists
    directories.
  - The `workspaces` frame is sent on add.
- Server Go:
  - A `workspaces` frame replaces the runner's catalogue: the identity and path tables
    change, and `/api/server-info` reflects it.
  - `runner:<id>:~` routes to the runner.
  - The router allows `runner-workspaces.*` with a machine target.
- Browser E2E `next/e2e/runner-folders.mjs` (`--e2e=folders`, in `all`):
  1. Open folder → picker → Choose folder… on the runner.
  2. Browse into a directory outside the env workspace (the suite creates
     `<data>/e2e-extra-folder`).
  3. Select it → a new sidebar project appears, and a chat in it gets the scripted
     reply.
  4. Reload: the folder is still offered in the picker.
- The install wizard (bug 1) is covered by a UI unit test of the post-install action and
  a manual check on the test server.
