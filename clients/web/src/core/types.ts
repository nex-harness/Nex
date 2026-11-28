export interface RunnerRecord {
  id: string;
  name: string;
  hostname: string;
  os: string;
  arch: string;
  shell: string;
  version: string;
  tags: string[];
  status: 'online' | 'offline';
  last_seen_at: number;
  synced_config_rev: number;
  workspace_default: string;
  auto_update?: boolean;
  update_mode?: 'self_update' | 'scp';
  build_sha?: string;
  binary_sha256?: string;
  protocol_version?: number;
  capabilities?: string[];
  is_dev?: boolean;
  elevated?: boolean;
  full_disk_access?: boolean;
  app_path?: string;
  update_status?: 'up_to_date' | 'update_available' | 'update_required' | string;
  update_reason?: string;
  target_version?: string;
  target_build_sha?: string;
  update_phase?: string;
  update_progress?: number;
  update_message?: string;
  update_error?: string;
  incompatible?: boolean;
  latency_ms?: number;
  latency_updated_at?: number;
}

export interface ProviderRecord {
  id: string;
  name: string;
  driver: 'openai_responses' | 'openai_chat' | 'anthropic';
  base_url: string;
  api_key: string;
  proxy_mode: 'direct' | 'server_proxy';
  enabled: boolean;
}

export interface ModelPricing {
  input_per_1m: number;
  output_per_1m: number;
  cache_read_per_1m: number;
  cache_write_per_1m: number;
}

export type ThinkingEffort = '' | 'minimal' | 'low' | 'medium' | 'on' | 'high' | 'xhigh' | 'max' | (string & {});

export interface ModelMeta {
  id: string;
  provider_id: string;
  display_name: string;
  source: string;
  context_window: number;
  max_output_tokens: number;
  supports_vision: boolean;
  supports_pdf: boolean;
  supports_audio?: boolean;
  supports_video?: boolean;
  supports_tools: boolean;
  supports_reasoning: boolean;
  supports_caching: boolean;
  reasoning_levels?: string[];
  reasoning_map?: Record<string, string>;
  pricing: ModelPricing;
}

export interface MultimodalProviderProfile {
  id: string;
  name: string;
  driver: string;
  base_url: string;
  api_key?: string;
  models?: string[];
}

export interface UserPreferences {
  theme: 'dark' | 'light' | 'system';
  code_theme: string;
  font_family: string;
  mono_font_family: string;
  expand_thinking: boolean;
  runner_badge_style?: 'tinted' | 'neutral';
  auto_archive_after_days: number;
  memory_auto_extract?: boolean;
  memory_provider_id?: string;
  memory_model_id?: string;
  browser_enabled?: boolean;
  browser_mode?: 'managed_cdp' | 'attach_cdp' | string;
  browser_headless?: boolean;
  browser_profile_mode?: 'persistent' | 'ephemeral' | string;
  browser_attach_url?: string;
  browser_executable_path?: string;
  browser_download_mirror?: string;
  notify_enabled?: boolean;
  notify_on_complete?: boolean;
  notify_on_input?: boolean;
  notify_only_background?: boolean;
  image_mode?: 'provider' | 'standalone' | string;
  image_driver?: 'openai_images' | 'openrouter_images' | 'xai_images' | 'minimax_images' | string;
  image_base_url?: string;
  image_api_key?: string;
  image_provider_id?: string;
  image_model_id?: string;
  classifier_mode?: 'provider' | 'standalone' | string;
  classifier_driver?: 'typesafe_system_one' | 'openrouter_system_one' | string;
  classifier_provider_id?: string;
  classifier_model_id?: string;
  classifier_base_url?: string;
  classifier_api_key?: string;
  jev_auto_approve_enabled?: boolean;
  jev_auto_router_enabled?: boolean;
  image_providers?: MultimodalProviderProfile[];
  active_image_provider_id?: string;
  active_image_model_id?: string;
  classifier_providers?: MultimodalProviderProfile[];
  active_classifier_provider_id?: string;
  active_classifier_model_id?: string;
  goal_max_rounds?: number;
  goal_max_turns_per_round?: number;
  goal_max_consecutive_no_progress?: number;
  goal_verifier_provider_id?: string;
  goal_verifier_model_id?: string;
  auto_check_updates?: boolean;
  auto_download_updates?: boolean;
  skipped_update_version?: string;
  stt_enabled?: boolean;
  stt_engine?: 'auto' | 'api' | 'browser' | string;
  stt_base_url?: string;
  stt_api_key?: string;
  stt_model?: string;
  stt_language?: string;
  stt_auto_send?: boolean;
}

export type ClientNotificationKind =
  | 'task_completed'
  | 'question'
  | 'approval'
  | 'plan_approval'
  | 'browser_handoff';

export interface ClientNotificationPayload {
  session_id: string;
  runner_id?: string;
  kind: ClientNotificationKind;
  session_title?: string;
  title?: string;
  body?: string;
  tool_name?: string;
  request_id?: string;
}

export interface InAppNotificationToast {
  id: string;
  kind: ClientNotificationKind;
  sessionId: string;
  title: string;
  body: string;
  createdAt: number;
}

export interface AgentProfile {
  id?: string;
  slug: string;
  display_name: string;
  description: string;
  system_prompt: string;
  model_ref: string;
  thinking_effort: string;
  tools_allow: string[];
  max_turns: number;
  isolation_mode: 'shared' | 'worktree';
}

export interface SkillRecord {
  id?: string;
  name: string;
  description: string;
  content_md: string;
  scope: string;
  target_runners: string[];
  enabled: boolean;
}

export interface DiscoveredSkill {
  name: string;
  description: string;
  scope: 'workspace' | 'user' | 'registry' | string;
  source_kind: string;
  source_dir: string;
  file_path: string;
  workspace_path?: string;
  content_md?: string;
  license?: string;
  compatibility?: string;
  disable_model_invocation?: boolean;
  has_scripts?: boolean;
  has_references?: boolean;
  has_assets?: boolean;
  shadowed_by?: string;
  global_enabled: boolean;
  workspace_enabled: boolean;
  workspace_overridden: boolean;
  workspace_overrides?: Record<string, boolean>;
}

export interface PluginRecord {
  id?: string;
  name: string;
  source_ts: string;
  compiled_js?: string;
  compiled_js_hash?: string;
  scope: string;
  target_runners: string[];
  enabled: boolean;
}

export type MCPExposure = 'codemode' | 'deferred' | 'direct' | 'hidden';

export interface MCPToolEntry {
  namespace: string;
  name: string;
  full_name: string;
  description?: string;
  read_only?: boolean;
  host_mode?: string;
}

export interface MCPProbeResult {
  name: string;
  status: 'connected' | 'error' | 'disabled' | string;
  tool_count: number;
  tools?: MCPToolEntry[];
  error?: string;
  stderr_tail?: string;
  latency_ms: number;
  checked_at: number;
}

export interface MCPServerRecord {
  id?: string;
  name: string;
  description?: string;
  host_mode: 'runner_local' | 'server_hosted';
  transport: 'stdio' | 'http';
  command: string;
  args: string[];
  env: Record<string, string>;
  url: string;
  headers?: Record<string, string>;
  exposure?: MCPExposure;
  scope: 'global' | 'workspace' | 'runner' | string;
  workspace_path?: string;
  workspace_overrides?: Record<string, boolean>;
  target_runners: string[];
  idle_ttl_sec: number;
  enabled: boolean;
  catalog_cache?: MCPToolEntry[];
  last_status?: 'connected' | 'disconnected' | 'error' | 'disabled' | string;
  last_error?: string;
  last_checked_at?: number;
}

export interface CredentialEntry {
  key: string;
  value?: string;
  value_hint?: string;
  has_value?: boolean;
  clear_value?: boolean;
  note?: string;
}

export type CredentialStorageMode = 'server_synced' | 'runner_local';
export type CredentialUsagePolicy = 'all' | 'chat_only' | 'cron_explicit';
export type CredentialScope = 'global' | 'workspace' | 'runner';

export interface CredentialRecord {
  id: string;
  name: string;
  description?: string;
  preset?: string;
  storage_mode: CredentialStorageMode;
  usage_policy: CredentialUsagePolicy;
  scope: CredentialScope;
  workspace_path?: string;
  target_runners?: string[];
  entries: CredentialEntry[];
  runner_overrides?: Record<string, CredentialEntry[]>;
  probe_command?: string;
  enabled: boolean;
  updated_at?: number;
}

export interface CredentialProbeResult {
  ok: boolean;
  exit_code: number;
  output?: string;
  error?: string;
  latency_ms: number;
}

export interface CronJobRecord {
  id?: string;
  runner_id: string;
  title: string;
  cron_expr: string;
  timezone: string;
  prompt: string;
  workspace_path: string;
  provider_id?: string;
  model_id?: string;
  thinking_effort?: string;
  permission_mode: string;
  credential_ids?: string[];
  recurring: boolean;
  max_runs: number;
  run_count?: number;
  enabled: boolean;
  last_run_at?: number;
}

export interface SessionRecord {
  id: string;
  runner_id: string;
  parent_session_id?: string;
  depth: number;
  title: string;
  workspace_path: string;
  agent_profile_slug: string;
  model_id: string;
  provider_id: string;
  thinking_effort?: string;
  permission_mode: 'full_access' | 'interactive' | 'autonomous' | 'read_only' | 'plan';
  status: string;
  latest_context_tokens: number;
  context_window_limit: number;
  created_at: number;
  updated_at: number;
  last_active_at: number;
  archived_at?: number;
}

export interface ContextCategoryItem {
  category: 'messages' | 'system_tools' | 'system_prompt' | 'mcp_tools' | 'skills' | 'meta_context';
  bytes: number;
  tokens: number;
  pct: number;
  blob_hash?: string;
  inline_content?: string;
}

export interface TurnTelemetry {
  id: string;
  session_id: string;
  turn_idx: number;
  runner_id: string;
  provider_id: string;
  model_id: string;
  trigger_source: string;
  context_window: number;
  total_context_tokens: number;
  window_utilization_pct: number;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cache_hit_rate_pct: number;
  estimated_cost_usd: number;
  cache_saved_usd: number;
  breakdown: ContextCategoryItem[];
  system_prompt_blob_hash?: string;
  raw_trace_blob_hash?: string;
  ttft_ms: number;
  duration_ms: number;
  recorded_at: number;
}

export interface TodoItem {
  content: string;
  status: 'pending' | 'in_progress' | 'completed';
  priority?: 'high' | 'medium' | 'low';
}

export type GoalPhase =
  | 'started'
  | 'round_start'
  | 'verifying'
  | 'round_verified'
  | 'completed'
  | 'failed'
  | 'aborted'
  | 'max_rounds'
  | 'blocked';

export interface GoalEntry {
  id: string;
  index: number;
  title: string;
  status: 'running' | 'completed' | 'failed' | 'aborted' | 'blocked';
  phase?: GoalPhase;
  round?: number;
  maxRounds?: number;
  lastVerdict?: string;
  missingItems?: string[];
  error?: string;
  startedAt: number;
  completedAt?: number;
  completedTodos: number;
  totalTodos: number;
}

export interface GoalDividerCard {
  goalId: string;
  phase: GoalPhase;
  round?: number;
  maxRounds?: number;
  achieved?: boolean;
  verdict?: string;
  missingItems?: string[];
  nextStep?: string;
  error?: string;
  objective?: string;
}

export interface ToolCallCard {
  id: string;
  name: string;
  argsText: string;
  status: 'running' | 'completed' | 'error';
  streamOutput: string;
  finalOutput?: string;
  images?: ChatImageBlock[];
  diff?: string;
  additions?: number;
  deletions?: number;
}

export interface ChatImageBlock {
  mime_type: string;
  data_b64?: string;
  image_url?: string;
}

export interface ComposerAttachment {
  id: string;
  name: string;
  mimeType: string;
  dataB64: string;
  previewUrl: string;
  size: number;
}

export interface CompactionCard {
  status: 'compacting' | 'completed' | 'skipped' | 'failed';
  reason?: string;
  beforeMessages?: number;
  afterMessages?: number;
  beforeTokens?: number;
  afterTokens?: number;
  summary?: string;
  error?: string;
}

export interface ChatTurnItem {
  id: string;
  seq?: number;
  startSeq?: number;
  role: 'user' | 'assistant' | 'compaction' | 'goal';
  text: string;
  thinking: string;
  images?: ChatImageBlock[];
  toolCalls: ToolCallCard[];
  compaction?: CompactionCard;
  goalDivider?: GoalDividerCard;
  streaming?: boolean;
  optimistic?: boolean;
}

export interface PendingApproval {
  approvalId: string;
  sessionId: string;
  runnerId: string;
  toolCallId: string;
  toolName: string;
  argsJson: string;
}

export interface UIPromptOption {
  label: string;
  description?: string;
}

export interface UIPromptQuestion {
  id?: string;
  header?: string;
  question: string;
  message?: string;
  kind?: 'select' | 'confirm' | 'input';
  options?: UIPromptOption[];
  multiSelect?: boolean;
  allowCustom?: boolean;
  placeholder?: string;
  defaultValue?: string;
}

export interface PendingUIPrompt {
  requestId: string;
  sessionId: string;
  runnerId: string;
  kind: 'select' | 'confirm' | 'input';
  header?: string;
  title: string;
  message?: string;
  placeholder?: string;
  options?: string[];
  optionItems?: UIPromptOption[];
  multiSelect?: boolean;
  allowCustom?: boolean;
  questions?: UIPromptQuestion[];
  defaultValue?: string;
}

export interface DirEntry {
  name: string;
  rel_path: string;
  kind: 'dir' | 'file' | 'symlink';
  size: number;
  mod_time_ms: number;
  git_ignored: boolean;
  git_status?: string;
}

export interface BrowseDirEntry {
  name: string;
  path: string;
  is_symlink?: boolean;
}

export interface BrowseDirsResult {
  current_path: string;
  parent_path: string;
  home_path: string;
  entries: BrowseDirEntry[];
}

export interface FilePreview {
  rel_path: string;
  name: string;
  preview_as: 'text' | 'markdown' | 'image' | 'binary';
  language?: string;
  mime_type?: string;
  content?: string;
  total_size: number;
  truncated: boolean;
  sha256?: string;
}

export interface GitFileChange {
  rel_path: string;
  status: string;
  staged?: boolean;
  unstaged?: boolean;
  additions: number;
  deletions: number;
  diff?: string;
  folded?: boolean;
}

export interface GitSummary {
  is_git_repo: boolean;
  branch?: string;
  changed_files: number;
  additions: number;
  deletions: number;
  files?: GitFileChange[];
}

export type WorkbenchTabKind = 'new_tab' | 'files' | 'file_preview' | 'review' | 'terminal' | 'side_chat' | 'subagent' | 'browser';

export interface BrowserTabInfo {
  tab_id: string;
  url: string;
  title: string;
  viewport: { width: number; height: number };
  active: boolean;
  can_go_back: boolean;
  can_go_forward: boolean;
  human_control?: boolean;
}

export interface BrowserHandoffState {
  session_id: string;
  tab_id: string;
  state: 'idle' | 'awaiting_human' | 'resumed' | 'cancelled' | string;
  reason?: string;
  message?: string;
}

export interface BrowserSessionState {
  session_id: string;
  runner_id?: string;
  tabs: BrowserTabInfo[];
  active_tab_id?: string;
  human_control?: boolean;
  handoff?: BrowserHandoffState | null;
}

export interface BrowserProbeResult {
  status: 'ready' | 'missing_binary' | 'missing_deps' | 'unsupported_arch' | 'error' | 'backend_unavailable' | string;
  browser_name?: string;
  version?: string;
  executable_path?: string;
  mode?: string;
  os?: string;
  arch?: string;
  missing_deps?: string[];
  suggested_command?: string;
  error?: string;
}

export interface SubagentInspectorData {
  toolCallId: string;
  title: string;
  agentName?: string;
  status: 'running' | 'done' | 'error';
  prompt: string;
  resultText: string;
}

export interface QueuedFollowUp {
  id: string;
  text: string;
  createdAt: number;
}

export interface WorkbenchTab {
  id: string;
  kind: WorkbenchTabKind;
  title: string;
  relPath?: string;
  highlightLine?: number;
  preview?: FilePreview;
  loading?: boolean;
  error?: string;
  ptyId?: string;
  sideSessionId?: string;
  subagent?: SubagentInspectorData;
  closable: boolean;
}

export interface UsageTimePoint {
  bucket_unix_sec: number;
  label: string;
  turn_count: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  total_tokens: number;
  estimated_cost_usd: number;
}

export interface UsageSummary {
  runner_id?: string;
  window: 'today' | '7d' | '30d';
  turn_count: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  total_tokens: number;
  cache_hit_rate_pct: number;
  estimated_cost_usd: number;
  cache_saved_usd: number;
  by_source_tokens: Record<string, number>;
  models: Array<{
    provider_id: string;
    model_id: string;
    turn_count: number;
    total_tokens: number;
    estimated_cost_usd: number;
    cache_saved_usd: number;
  }>;
  series?: UsageTimePoint[];
}

export interface MemoryRecord {
  id: string;
  scope: 'global' | 'project';
  workspace_path?: string;
  category: 'preference' | 'architecture' | 'convention' | 'fact' | string;
  title: string;
  content: string;
  created_at: number;
  updated_at: number;
}

export interface SessionSearchHit {
  session: SessionRecord;
  match_type: 'title' | 'workspace' | 'message';
  snippet: string;
  matched_seq?: number;
}

export interface RewindResult {
  session_id: string;
  before_seq: number;
  restored_files: string[];
  deleted_files: string[];
  errors?: string[];
}

export interface SSHConfigHost {
  alias: string;
  hostname: string;
  user?: string;
  port: number;
  identity_file?: string;
}

export interface SSHTargetRecord {
  id: string;
  name: string;
  ssh_alias?: string;
  host: string;
  port: number;
  username: string;
  auth_method: 'agent' | 'key' | 'password';
  key_path?: string;
  password?: string;
  has_password?: boolean;
  workspace_path?: string;
  install_dir?: string;
  server_ws_url?: string;
  install_mode: 'scp' | 'remote_install';
  download_url?: string;
  last_status: 'idle' | 'testing' | 'deploying' | 'connected' | 'error' | string;
  last_error?: string;
  last_log?: string;
  last_deployed_at?: number;
  created_at?: number;
  updated_at?: number;
}

export interface StagedBinaryInfo {
  os: string;
  arch: string;
  size_bytes: number;
  updated_at: number;
  source?: 'staged' | 'bundled' | 'local' | string;
}

export type SettingsSectionId =
  | 'general'
  | 'appearance'
  | 'providers'
  | 'runners'
  | 'credentials'
  | 'remote'
  | 'browser'
  | 'agents'
  | 'memories'
  | 'plugins'
  | 'mcp'
  | 'skills'
  | 'automations'
  | 'usage'
  | 'archives';

export interface RemoteHostConfig {
  enabled: boolean;
  host_name: string;
  relay_url: string;
  relay_token?: string;
  direct_urls?: string[];
  prefer_lan: boolean;
  tsnet_enabled?: boolean;
  tsnet_control_url?: string;
  tsnet_hostname?: string;
  tsnet_download_mirror?: string;
}

export interface SystemTailscaleStatus {
  available: boolean;
  running: boolean;
  backend_state: string;
  dns_name?: string;
  tailscale_ips?: string[];
  suggested_urls?: string[];
  error?: string;
}

export interface TSNetSidecarStatus {
  platform_supported: boolean;
  installed: boolean;
  binary_path?: string;
  running: boolean;
  state: 'not_installed' | 'stopped' | 'starting' | 'needs_login' | 'running' | 'error' | 'unsupported_mobile' | string;
  auth_url?: string;
  dns_name?: string;
  tailscale_ips?: string[];
  listen_url?: string;
  error?: string;
}

export interface RemoteHostStatus {
  node_id: string;
  fingerprint: string;
  short_fingerprint: string;
  enabled: boolean;
  host_name: string;
  relay_url: string;
  relay_connected: boolean;
  last_relay_error?: string;
  direct_urls: string[];
  lan_urls: string[];
  active_tunnels: number;
  system_tailscale?: SystemTailscaleStatus;
  tsnet?: TSNetSidecarStatus;
  config: RemoteHostConfig;
}

export interface RemotePairingSession {
  pair_uri: string;
  token: string;
  expires_at: number;
  node_id: string;
  fingerprint: string;
  short_fingerprint: string;
  host_name: string;
  relay_url?: string;
  direct_urls?: string[];
  lan_urls?: string[];
}

export interface RemotePairedDevice {
  id: string;
  name: string;
  platform: string;
  pubkey_fp: string;
  created_at: number;
  last_seen_at: number;
  last_transport: string;
  revoked_at: number;
}

export interface BackgroundTaskItem {
  task_id: string;
  kind: 'local_bash' | 'local_agent' | string;
  title: string;
  status: 'running' | 'completed' | 'failed' | 'killed' | string;
  started_at: number;
  exit_code?: number;
  stopped_by?: string;
}

export interface AppUpdateCheckResult {
  checked_at: number;
  release_url: string;
  latest_version: string;
  published_at?: string;
  release_notes?: string;
  server_version: string;
  server_is_dev: boolean;
  server_has_update: boolean;
  client_version?: string;
  client_platform?: string;
  client_is_dev?: boolean;
  client_has_update?: boolean;
  client_download_url?: string;
  client_asset_name?: string;
  staged_server_update?: boolean;
  staged_version?: string;
  staged_build_sha?: string;
  cached?: boolean;
  error?: string;
}

export interface UpdateDownloadProgress {
  version: string;
  asset_name: string;
  downloaded_bytes: number;
  total_bytes: number;
  percent: number;
  status: 'downloading' | 'ready' | 'error' | string;
  local_path?: string;
  error?: string;
}
