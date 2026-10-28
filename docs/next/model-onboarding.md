# Model onboarding for new machines — spec

Status: batch 5 P0-2 on `feature/next`. Builds on `config-mcp-skills.md` (decision 2: a
runner uses its local `~/.pi/agent` catalog until the server config owns providers) and
`runner-install.md` (SSH install wizard).

## Problem

A machine installed over SSH starts with an empty `~/.pi/agent`. While the server config
has no providers, that runner stays in fallback mode with an empty catalog. The composer
shows "No model available", and nothing tells the user what to do. The server can already
import a runner's local pi config (`POST /api/config/import-runner`), but no UI calls it.

## Decisions

1. **The install wizard ends with a model check.** After the install succeeds, the dialog
   stays open on a "Models" step instead of closing. The step polls the new machine's model
   status, about once a second for up to 15 s. A config push is applied shortly after
   `hello`, so the first answers can still be `unknown`.
   - If the machine has executable models, the step shows "Ready, N models" and a Done
     button.
   - If it has none, the step offers:
     - **Import from another machine.** Lists connected machines that have models; each
       entry imports that machine's pi config into the shared scope (decision 3).
     - **Open model settings.** Closes the dialog and opens Settings → Models.
     - **Later.** Closes the dialog.
   - After an import, the step re-checks the new machine and shows the import summary:
     the providers imported, and the providers skipped with their reasons.
2. **Settings → Models gets an "Import from machine" action.** It is a menu listing
   connected machines with their mode and model count, and each item imports into the
   shared scope. A toast reports the summary, and the provider list refreshes through the
   existing `provider-settings` change event. This is the migration path for an existing
   machine's pi config (the "导入本机" button).
3. **UI imports write the shared (global) scope.** The settings page reads and writes the
   global layer (provider edits and `storeProviderSecret` only touch
   `scopes.global`). A provider imported into one runner's scope could not be edited
   there, and other machines would not see it. The HTTP endpoint keeps
   `scope=runner` as its default for compatibility, and the UI always sends
   `scope=global`. Keys become server secrets under the same `sec_prov_<id>_apikey` ids.
   Existing providers with the same id are overwritten, and others stay. Importing is
   explicit and one-shot: nothing is imported silently.
4. **Server-owned mode is decided per runner, everywhere.** The runner takes server-owned
   mode only when its own effective layer (global + its runner scope) has an enabled
   provider (`ownedMode` in the push). `model-selection` previously also required the
   _server-wide_ `OwnsProviders()` to be false before it used fallback facts. Once any
   other runner's scope owned providers, a fallback machine was shown no models while its
   runtime still had them. Model selection now uses the same per-runner predicate as the
   push.
5. **Keys come from `auth.json` and `models.json`.** A provider-level `apiKey` in
   `models.json` is exported too. This is the usual place for a custom provider's key.
   Without it, an imported provider would have no credential. A literal key is exported
   as-is, and a `$NAME` / `${NAME}` reference is resolved on the source machine, because
   the server pushes only `sec_` secrets. A `!command` is not exported, and neither is an
   unset variable; both are reported in `errors[]`. An `auth.json` api key wins over
   `models.json`, which matches pi's credential order.
6. **Skipped providers are surfaced, not hidden.** The runner export reports unsupported
   providers in `errors[]`, for example an api the doc cannot express. A subscription
   login that lives only in `auth.json` is also skipped. The UI lists them so a user knows
   which of the source machine's models did not come across.

## API (session cookie auth)

- `GET /api/config/runners` returns
  `{runners: [{runnerId, name, connected, mode, modelCount, lastAppliedVersion}]}`.
  - `mode` is `"server" | "fallback" | "unknown"`. It is `"unknown"` until the runner
    has reported facts.
  - `modelCount` is the number of models the composer would offer for that runner, the
    same list as `model-selection.getView` with `{runnerId}`.
  - `name` is the runner's hello name, falling back to the id.
  - Runners are known runners (a live link or recorded facts), sorted by name.
- `POST /api/config/import-runner?runnerId=<id>&scope=global|runner` (default `runner`)
  returns `{runnerId, scope, version, providers[], keysStored[], errors?}`. An unknown
  runner is 404, and an unknown scope is 400. Keys are never returned.

Platform methods (web only; desktop leaves them undefined and the UI hides the entries):
`listRunnerModelStatus?(): Promise<RunnerModelStatus[]>` and
`importRunnerModels?(runnerId, scope): Promise<RunnerImportSummary>`. Both types are in
`@nex/shared`.

## Event order

```
wizard: install succeeded (runnerId) ──▶ phase "models"
  poll GET /api/config/runners ──▶ runner hello → server pushes config → configApplied{facts}
                                   └▶ mode/modelCount known
  modelCount > 0 ─▶ "Ready" ─▶ Done
  modelCount = 0 ─▶ user picks source machine
      POST import-runner?runnerId=<source>&scope=global
        ──▶ store.Update (global providers + secrets) ──▶ push to every runner
        ──▶ runners apply (server-owned) ──▶ facts ──▶ re-poll shows modelCount > 0
```

Owners: the server config store owns the providers and keys. Runners own the applied
runtime and facts. The wizard only reads status and triggers the one-shot import; it holds
no model state.

## Acceptance

- Go: `GET /api/config/runners` reports mode, count and name. A global import writes
  `scopes.global` and stores the secrets without echoing keys. An unknown scope returns 400. Model selection for a fallback runner keeps its local models while another runner's
  scope owns providers.
- UI unit: the wizard step's state resolution (unknown, then ready or needs-models, plus
  the source candidates, which exclude the new runner and machines with no models).
- Browser E2E `next/e2e/model-onboarding.mjs` (`--e2e=onboarding`). It runs **last** in
  `all`, because a global import switches every runner to server-owned mode. The suite
  adds an importable `openai-completions` provider to the runner's agent `models.json`.
  That provider points at the faux server's `/v1/chat/completions`.
  - The E2E faux provider's custom api is reported as skipped.
  - Settings → Models → Import from machine → the imported provider is listed, and the
    summary toast names it.
  - A new chat with the imported model gets the scripted reply.
- The install wizard's Models step on a real SSH install is verified manually on the test
  server (no SSH target in CI).
