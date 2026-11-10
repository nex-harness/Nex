# Restore Legacy Sessions Plugin

Nex official plugin for restoring old ACP-era Nex session snapshots into the new task index and CLI session DB.

This plugin is bundled as `restore-legacy-sessions@zcode-plugins-official` and is disabled by default. Enable it only when you need to inspect or restore old session data.

## Enable

```sh
nex plugins enable restore-legacy-sessions
```

Or inside a Nex session:

```text
/plugins enable restore-legacy-sessions
```

Changes apply to new sessions. After enabling, use:

```text
/restore-legacy-sessions
```

## Data Boundaries

- Default source: `~/.nex/v2/sessions`
- Task index destination: `~/.nex/v2/tasks-index.sqlite`
- CLI session DB destination: `~/.nex/cli/db/db.sqlite`

The restore scripts create timestamped DB backups before writing. Restored ACP-era conversations are persisted as normal `glm` Nex Agent history and do not write `migration_source`.

## Development

This is a skill-only / command-only plugin. It has no MCP server and no build step.
