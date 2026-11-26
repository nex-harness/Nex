package store

const mainSchemaSQL = `
CREATE TABLE IF NOT EXISTS kv_settings (
    key         TEXT PRIMARY KEY,
    value_json  TEXT NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS blob_refs (
    hash        TEXT PRIMARY KEY,
    size_bytes  INTEGER NOT NULL,
    mime        TEXT NOT NULL,
    ref_count   INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS providers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    driver      TEXT NOT NULL,
    base_url    TEXT NOT NULL,
    api_key     TEXT NOT NULL DEFAULT '',
    proxy_mode  TEXT NOT NULL DEFAULT 'direct',
    enabled     INTEGER NOT NULL DEFAULT 1,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS models (
    id                      TEXT NOT NULL,
    provider_id             TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    display_name            TEXT NOT NULL,
    source                  TEXT NOT NULL DEFAULT 'models.dev',
    context_window          INTEGER NOT NULL DEFAULT 200000,
    max_output_tokens       INTEGER NOT NULL DEFAULT 16384,
    supports_vision         INTEGER NOT NULL DEFAULT 0,
    supports_pdf            INTEGER NOT NULL DEFAULT 0,
    supports_audio          INTEGER NOT NULL DEFAULT 0,
    supports_video          INTEGER NOT NULL DEFAULT 0,
    supports_tools          INTEGER NOT NULL DEFAULT 1,
    supports_reasoning      INTEGER NOT NULL DEFAULT 0,
    supports_caching        INTEGER NOT NULL DEFAULT 0,
    cost_input_per_1m       REAL NOT NULL DEFAULT 0,
    cost_output_per_1m      REAL NOT NULL DEFAULT 0,
    cost_cache_read_per_1m  REAL NOT NULL DEFAULT 0,
    cost_cache_write_per_1m REAL NOT NULL DEFAULT 0,
    enabled                 INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (provider_id, id)
);

CREATE TABLE IF NOT EXISTS runners (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    name_customized     INTEGER NOT NULL DEFAULT 0,
    hostname            TEXT NOT NULL DEFAULT '',
    os                  TEXT NOT NULL DEFAULT '',
    arch                TEXT NOT NULL DEFAULT '',
    shell               TEXT NOT NULL DEFAULT '',
    version             TEXT NOT NULL DEFAULT '',
    tags_json           TEXT NOT NULL DEFAULT '[]',
    status              TEXT NOT NULL DEFAULT 'offline',
    last_seen_at        INTEGER NOT NULL DEFAULT 0,
    synced_config_rev   INTEGER NOT NULL DEFAULT 0,
    auth_token          TEXT NOT NULL DEFAULT '',
    workspace_default   TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_profiles (
    id              TEXT PRIMARY KEY,
    slug            TEXT UNIQUE NOT NULL,
    display_name    TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    system_prompt   TEXT NOT NULL DEFAULT '',
    model_ref       TEXT NOT NULL DEFAULT '',
    thinking_effort TEXT NOT NULL DEFAULT '',
    tools_allow_json TEXT NOT NULL DEFAULT '[]',
    max_turns       INTEGER NOT NULL DEFAULT 40,
    isolation_mode  TEXT NOT NULL DEFAULT 'shared',
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS skills_registry (
    id                  TEXT PRIMARY KEY,
    name                TEXT UNIQUE NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    content_md          TEXT NOT NULL DEFAULT '',
    bundle_hash         TEXT NOT NULL DEFAULT '',
    scope               TEXT NOT NULL DEFAULT 'global',
    target_runners_json TEXT NOT NULL DEFAULT '[]',
    enabled             INTEGER NOT NULL DEFAULT 1,
    updated_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS plugins_registry (
    id                  TEXT PRIMARY KEY,
    name                TEXT UNIQUE NOT NULL,
    source_ts           TEXT NOT NULL DEFAULT '',
    compiled_js         TEXT NOT NULL DEFAULT '',
    compiled_js_hash    TEXT NOT NULL DEFAULT '',
    scope               TEXT NOT NULL DEFAULT 'global',
    target_runners_json TEXT NOT NULL DEFAULT '[]',
    enabled             INTEGER NOT NULL DEFAULT 1,
    updated_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS mcp_servers (
    id                       TEXT PRIMARY KEY,
    name                     TEXT UNIQUE NOT NULL,
    description              TEXT NOT NULL DEFAULT '',
    host_mode                TEXT NOT NULL DEFAULT 'runner_local',
    transport                TEXT NOT NULL DEFAULT 'stdio',
    command                  TEXT NOT NULL DEFAULT '',
    args_json                TEXT NOT NULL DEFAULT '[]',
    env_json                 TEXT NOT NULL DEFAULT '{}',
    url                      TEXT NOT NULL DEFAULT '',
    headers_json             TEXT NOT NULL DEFAULT '{}',
    exposure                 TEXT NOT NULL DEFAULT 'codemode',
    scope                    TEXT NOT NULL DEFAULT 'global',
    workspace_path           TEXT NOT NULL DEFAULT '',
    workspace_overrides_json TEXT NOT NULL DEFAULT '{}',
    target_runners_json      TEXT NOT NULL DEFAULT '[]',
    idle_ttl_sec             INTEGER NOT NULL DEFAULT 300,
    enabled                  INTEGER NOT NULL DEFAULT 1,
    catalog_cache_json       TEXT NOT NULL DEFAULT '[]',
    last_status              TEXT NOT NULL DEFAULT '',
    last_error               TEXT NOT NULL DEFAULT '',
    last_checked_at          INTEGER NOT NULL DEFAULT 0,
    updated_at               INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS cron_jobs (
    id                  TEXT PRIMARY KEY,
    runner_id           TEXT NOT NULL,
    title               TEXT NOT NULL,
    cron_expr           TEXT NOT NULL,
    timezone            TEXT NOT NULL DEFAULT 'Local',
    prompt              TEXT NOT NULL,
    workspace_path      TEXT NOT NULL DEFAULT '',
    permission_mode     TEXT NOT NULL DEFAULT 'autonomous',
    credential_ids_json TEXT NOT NULL DEFAULT '[]',
    recurring           INTEGER NOT NULL DEFAULT 1,
    max_runs            INTEGER NOT NULL DEFAULT 0,
    run_count           INTEGER NOT NULL DEFAULT 0,
    enabled             INTEGER NOT NULL DEFAULT 1,
    last_run_at         INTEGER NOT NULL DEFAULT 0,
    next_run_at         INTEGER NOT NULL DEFAULT 0,
    updated_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credentials (
    id                    TEXT PRIMARY KEY,
    name                  TEXT UNIQUE NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    preset                TEXT NOT NULL DEFAULT '',
    storage_mode          TEXT NOT NULL DEFAULT 'server_synced',
    usage_policy          TEXT NOT NULL DEFAULT 'all',
    scope                 TEXT NOT NULL DEFAULT 'global',
    workspace_path        TEXT NOT NULL DEFAULT '',
    target_runners_json   TEXT NOT NULL DEFAULT '[]',
    entries_json          TEXT NOT NULL DEFAULT '[]',
    runner_overrides_json TEXT NOT NULL DEFAULT '{}',
    probe_command         TEXT NOT NULL DEFAULT '',
    enabled               INTEGER NOT NULL DEFAULT 1,
    updated_at            INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS credential_local_values (
    credential_id TEXT NOT NULL,
    env_key       TEXT NOT NULL,
    encrypted_val TEXT NOT NULL,
    value_hint    TEXT NOT NULL DEFAULT '',
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (credential_id, env_key)
);

CREATE TABLE IF NOT EXISTS sessions (
    id                      TEXT PRIMARY KEY,
    runner_id               TEXT NOT NULL,
    parent_session_id       TEXT NOT NULL DEFAULT '',
    parent_tool_call_id     TEXT NOT NULL DEFAULT '',
    depth                   INTEGER NOT NULL DEFAULT 0,
    title                   TEXT NOT NULL,
    workspace_path          TEXT NOT NULL DEFAULT '',
    agent_profile_slug      TEXT NOT NULL DEFAULT 'main',
    model_id                TEXT NOT NULL DEFAULT '',
    provider_id             TEXT NOT NULL DEFAULT '',
    thinking_effort         TEXT NOT NULL DEFAULT '',
    permission_mode         TEXT NOT NULL DEFAULT 'interactive',
    status                  TEXT NOT NULL DEFAULT 'idle',
    latest_context_tokens   INTEGER NOT NULL DEFAULT 0,
    context_window_limit    INTEGER NOT NULL DEFAULT 200000,
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    last_active_at          INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_runner_active ON sessions(runner_id, last_active_at DESC);

CREATE TABLE IF NOT EXISTS session_events (
    session_id       TEXT NOT NULL,
    seq              INTEGER NOT NULL,
    turn_idx         INTEGER NOT NULL DEFAULT 0,
    event_type       TEXT NOT NULL,
    payload_json     TEXT NOT NULL,
    blob_hashes_json TEXT NOT NULL DEFAULT '[]',
    created_at       INTEGER NOT NULL,
    PRIMARY KEY (session_id, seq)
);

CREATE TABLE IF NOT EXISTS turn_telemetry (
    id                      TEXT PRIMARY KEY,
    session_id              TEXT NOT NULL,
    turn_idx                INTEGER NOT NULL,
    runner_id               TEXT NOT NULL,
    provider_id             TEXT NOT NULL,
    model_id                TEXT NOT NULL,
    trigger_source          TEXT NOT NULL DEFAULT 'chat',
    context_window          INTEGER NOT NULL,
    total_context_tokens    INTEGER NOT NULL,
    window_utilization_pct  REAL NOT NULL,
    input_tokens            INTEGER NOT NULL,
    output_tokens           INTEGER NOT NULL,
    reasoning_tokens        INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_hit_rate_pct      REAL NOT NULL DEFAULT 0,
    estimated_cost_usd      REAL NOT NULL DEFAULT 0,
    cache_saved_usd         REAL NOT NULL DEFAULT 0,
    breakdown_json          TEXT NOT NULL,
    system_prompt_blob_hash TEXT NOT NULL DEFAULT '',
    raw_trace_blob_hash     TEXT NOT NULL DEFAULT '',
    ttft_ms                 INTEGER NOT NULL DEFAULT 0,
    duration_ms             INTEGER NOT NULL DEFAULT 0,
    recorded_at             INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_telemetry_session ON turn_telemetry(session_id, turn_idx);

CREATE TABLE IF NOT EXISTS outbox_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    frame_type  TEXT NOT NULL,
    frame_json  TEXT NOT NULL,
    acked       INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_outbox_unacked ON outbox_events(acked, id);

CREATE TABLE IF NOT EXISTS usage_rollups_hourly (
    hour_bucket         INTEGER NOT NULL,
    runner_id           TEXT NOT NULL,
    provider_id         TEXT NOT NULL,
    model_id            TEXT NOT NULL,
    trigger_source      TEXT NOT NULL,
    turn_count          INTEGER NOT NULL DEFAULT 0,
    input_tokens        INTEGER NOT NULL DEFAULT 0,
    output_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens  INTEGER NOT NULL DEFAULT 0,
    estimated_cost_usd  REAL NOT NULL DEFAULT 0,
    cache_saved_usd     REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (hour_bucket, runner_id, provider_id, model_id, trigger_source)
);

CREATE INDEX IF NOT EXISTS idx_rollups_runner_hour ON usage_rollups_hourly(runner_id, hour_bucket);

CREATE TABLE IF NOT EXISTS memories (
    id             TEXT PRIMARY KEY,
    scope          TEXT NOT NULL DEFAULT 'project',
    workspace_path TEXT NOT NULL DEFAULT '',
    category       TEXT NOT NULL DEFAULT 'fact',
    title          TEXT NOT NULL,
    content        TEXT NOT NULL,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memories_scope_ws ON memories(scope, workspace_path, updated_at DESC);

CREATE TABLE IF NOT EXISTS file_checkpoints (
    id             TEXT PRIMARY KEY,
    session_id     TEXT NOT NULL,
    event_seq      INTEGER NOT NULL DEFAULT 0,
    tool_call_id   TEXT NOT NULL DEFAULT '',
    file_path      TEXT NOT NULL,
    existed        INTEGER NOT NULL DEFAULT 1,
    blob_hash      TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    UNIQUE(session_id, event_seq, file_path)
);

CREATE INDEX IF NOT EXISTS idx_file_checkpoints_sess ON file_checkpoints(session_id, event_seq);

CREATE TABLE IF NOT EXISTS ssh_targets (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    ssh_alias        TEXT NOT NULL DEFAULT '',
    host             TEXT NOT NULL,
    port             INTEGER NOT NULL DEFAULT 22,
    username         TEXT NOT NULL DEFAULT '',
    auth_method      TEXT NOT NULL DEFAULT 'key',
    key_path         TEXT NOT NULL DEFAULT '',
    password         TEXT NOT NULL DEFAULT '',
    workspace_path   TEXT NOT NULL DEFAULT '',
    install_dir      TEXT NOT NULL DEFAULT '~/.nex-runner',
    server_ws_url    TEXT NOT NULL DEFAULT '',
    install_mode     TEXT NOT NULL DEFAULT 'scp',
    download_url     TEXT NOT NULL DEFAULT '',
    last_status      TEXT NOT NULL DEFAULT 'idle',
    last_error       TEXT NOT NULL DEFAULT '',
    last_log         TEXT NOT NULL DEFAULT '',
    last_deployed_at INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS paired_devices (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    platform        TEXT NOT NULL DEFAULT '',
    pubkey_fp       TEXT NOT NULL UNIQUE,
    pubkey_pem      TEXT NOT NULL DEFAULT '',
    token_hash      TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    last_seen_at    INTEGER NOT NULL DEFAULT 0,
    last_transport  TEXT NOT NULL DEFAULT '',
    revoked_at      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_paired_devices_token ON paired_devices(token_hash);
`

const archiveSchemaSQL = `
CREATE TABLE IF NOT EXISTS sessions (
    id                      TEXT PRIMARY KEY,
    runner_id               TEXT NOT NULL,
    parent_session_id       TEXT NOT NULL DEFAULT '',
    parent_tool_call_id     TEXT NOT NULL DEFAULT '',
    depth                   INTEGER NOT NULL DEFAULT 0,
    title                   TEXT NOT NULL,
    workspace_path          TEXT NOT NULL DEFAULT '',
    agent_profile_slug      TEXT NOT NULL DEFAULT 'main',
    model_id                TEXT NOT NULL DEFAULT '',
    provider_id             TEXT NOT NULL DEFAULT '',
    thinking_effort         TEXT NOT NULL DEFAULT '',
    permission_mode         TEXT NOT NULL DEFAULT 'interactive',
    status                  TEXT NOT NULL DEFAULT 'archived',
    latest_context_tokens   INTEGER NOT NULL DEFAULT 0,
    context_window_limit    INTEGER NOT NULL DEFAULT 200000,
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    last_active_at          INTEGER NOT NULL,
    archived_at             INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_arch_sessions_parent ON sessions(parent_session_id);
CREATE INDEX IF NOT EXISTS idx_arch_sessions_time ON sessions(archived_at DESC);

CREATE TABLE IF NOT EXISTS session_events (
    session_id       TEXT NOT NULL,
    seq              INTEGER NOT NULL,
    turn_idx         INTEGER NOT NULL DEFAULT 0,
    event_type       TEXT NOT NULL,
    payload_json     TEXT NOT NULL,
    blob_hashes_json TEXT NOT NULL DEFAULT '[]',
    created_at       INTEGER NOT NULL,
    PRIMARY KEY (session_id, seq)
);

CREATE TABLE IF NOT EXISTS turn_telemetry (
    id                      TEXT PRIMARY KEY,
    session_id              TEXT NOT NULL,
    turn_idx                INTEGER NOT NULL,
    runner_id               TEXT NOT NULL,
    provider_id             TEXT NOT NULL,
    model_id                TEXT NOT NULL,
    trigger_source          TEXT NOT NULL DEFAULT 'chat',
    context_window          INTEGER NOT NULL,
    total_context_tokens    INTEGER NOT NULL,
    window_utilization_pct  REAL NOT NULL,
    input_tokens            INTEGER NOT NULL,
    output_tokens           INTEGER NOT NULL,
    reasoning_tokens        INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_hit_rate_pct      REAL NOT NULL DEFAULT 0,
    estimated_cost_usd      REAL NOT NULL DEFAULT 0,
    cache_saved_usd         REAL NOT NULL DEFAULT 0,
    breakdown_json          TEXT NOT NULL,
    system_prompt_blob_hash TEXT NOT NULL DEFAULT '',
    raw_trace_blob_hash     TEXT NOT NULL DEFAULT '',
    ttft_ms                 INTEGER NOT NULL DEFAULT 0,
    duration_ms             INTEGER NOT NULL DEFAULT 0,
    recorded_at             INTEGER NOT NULL
);
`
