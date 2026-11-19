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
}

export interface ProviderRecord {
  id: string;
  name: string;
  driver: 'openai_responses' | 'anthropic';
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

export interface ModelMeta {
  id: string;
  provider_id: string;
  display_name: string;
  source: string;
  context_window: number;
  max_output_tokens: number;
  supports_vision: boolean;
  supports_pdf: boolean;
  supports_tools: boolean;
  supports_reasoning: boolean;
  supports_caching: boolean;
  pricing: ModelPricing;
}

export interface UserPreferences {
  theme: 'dark' | 'light' | 'system';
  code_theme: string;
  font_family: string;
  mono_font_family: string;
  expand_thinking: boolean;
  auto_archive_after_days: number;
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

export interface MCPServerRecord {
  id?: string;
  name: string;
  host_mode: 'runner_local' | 'server_hosted';
  transport: 'stdio' | 'http';
  command: string;
  args: string[];
  env: Record<string, string>;
  url: string;
  scope: string;
  target_runners: string[];
  idle_ttl_sec: number;
  enabled: boolean;
}

export interface CronJobRecord {
  id?: string;
  runner_id: string;
  title: string;
  cron_expr: string;
  timezone: string;
  prompt: string;
  workspace_path: string;
  permission_mode: string;
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
  permission_mode: 'full_access' | 'interactive' | 'autonomous' | 'read_only';
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

export interface GoalEntry {
  id: string;
  index: number;
  title: string;
  status: 'running' | 'completed';
  startedAt: number;
  completedAt?: number;
  completedTodos: number;
  totalTodos: number;
}

export interface ToolCallCard {
  id: string;
  name: string;
  argsText: string;
  status: 'running' | 'completed' | 'error';
  streamOutput: string;
  finalOutput?: string;
  diff?: string;
  additions?: number;
  deletions?: number;
}

export interface ChatTurnItem {
  id: string;
  role: 'user' | 'assistant';
  text: string;
  thinking: string;
  toolCalls: ToolCallCard[];
  streaming?: boolean;
}

export interface PendingApproval {
  approvalId: string;
  sessionId: string;
  runnerId: string;
  toolCallId: string;
  toolName: string;
  argsJson: string;
}

export interface PendingUIPrompt {
  requestId: string;
  sessionId: string;
  runnerId: string;
  kind: 'select' | 'confirm' | 'input';
  title: string;
  message?: string;
  placeholder?: string;
  options?: string[];
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

export type WorkbenchTabKind = 'new_tab' | 'files' | 'file_preview' | 'review' | 'terminal' | 'side_chat';

export interface WorkbenchTab {
  id: string;
  kind: WorkbenchTabKind;
  title: string;
  relPath?: string;
  preview?: FilePreview;
  ptyId?: string;
  sideSessionId?: string;
  closable: boolean;
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
}

export type SettingsSectionId =
  | 'general'
  | 'appearance'
  | 'providers'
  | 'runners'
  | 'agents'
  | 'plugins'
  | 'mcp'
  | 'skills'
  | 'automations'
  | 'usage'
  | 'archives';
