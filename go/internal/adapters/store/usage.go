package store

import (
	"github.com/tylinux/nex/go/internal/sessionstore"
)

// RecordModelUsage mirrors recordModelUsage. computed_total_tokens is derived
// when absent (input side tokens + output), and every write prunes rows older
// than the 30-day retention window.
func (s *SessionStore) RecordModelUsage(u sessionstore.ModelUsage) error {
	computed := computedTotalTokens(u)
	_, err := s.db.Exec(`
		insert into model_usage (
			id, logical_request_id, attempt_index, session_id, turn_id, trace_id, span_id,
			assistant_message_id, parent_user_message_id, query_source, provider_id, model_id,
			variant, agent, mode, task_type, status, started_at, first_token_at, completed_at,
			duration_ms, time_to_first_token_ms, finish_reason, tool_call_count, input_tokens,
			output_tokens, reasoning_tokens, cache_creation_input_tokens, cache_read_input_tokens,
			provider_total_tokens, computed_total_tokens, retry_count, retryable, cancelled_by_user,
			context_exceeded, error_type, error_code, error_message, raw_usage_json, provider_metadata_json
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			logical_request_id = excluded.logical_request_id,
			attempt_index = excluded.attempt_index,
			session_id = excluded.session_id,
			turn_id = excluded.turn_id,
			trace_id = excluded.trace_id,
			span_id = excluded.span_id,
			assistant_message_id = excluded.assistant_message_id,
			parent_user_message_id = excluded.parent_user_message_id,
			query_source = excluded.query_source,
			provider_id = excluded.provider_id,
			model_id = excluded.model_id,
			variant = excluded.variant,
			agent = excluded.agent,
			mode = excluded.mode,
			task_type = excluded.task_type,
			status = excluded.status,
			started_at = excluded.started_at,
			first_token_at = excluded.first_token_at,
			completed_at = excluded.completed_at,
			duration_ms = excluded.duration_ms,
			time_to_first_token_ms = excluded.time_to_first_token_ms,
			finish_reason = excluded.finish_reason,
			tool_call_count = excluded.tool_call_count,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			reasoning_tokens = excluded.reasoning_tokens,
			cache_creation_input_tokens = excluded.cache_creation_input_tokens,
			cache_read_input_tokens = excluded.cache_read_input_tokens,
			provider_total_tokens = excluded.provider_total_tokens,
			computed_total_tokens = excluded.computed_total_tokens,
			retry_count = excluded.retry_count,
			retryable = excluded.retryable,
			cancelled_by_user = excluded.cancelled_by_user,
			context_exceeded = excluded.context_exceeded,
			error_type = excluded.error_type,
			error_code = excluded.error_code,
			error_message = excluded.error_message,
			raw_usage_json = excluded.raw_usage_json,
			provider_metadata_json = excluded.provider_metadata_json`,
		u.ID, u.LogicalRequestID, clampNonNegative(u.AttemptIndex), u.SessionID, nullString(u.TurnID),
		nullString(u.TraceID), nullString(u.SpanID), nullString(u.AssistantMessageID),
		nullString(u.ParentUserMessageID), u.QuerySource, u.ProviderID, u.ModelID, nullString(u.Variant),
		nullString(u.Agent), nullString(u.Mode), nullString(u.TaskType), u.Status, u.StartedAt,
		nullInt(u.FirstTokenAt), nullInt(u.CompletedAt), nullInt(u.DurationMs), nullInt(u.TimeToFirstTokenMs),
		nullString(u.FinishReason), clampNonNegative(u.ToolCallCount), clampNonNegative(u.InputTokens),
		clampNonNegative(u.OutputTokens), clampNonNegative(u.ReasoningTokens),
		clampNonNegative(u.CacheCreationInputTokens), clampNonNegative(u.CacheReadInputTokens),
		nullInt(u.ProviderTotalTokens), computed, clampNonNegative(u.RetryCount), boolInt(u.Retryable),
		boolInt(u.CancelledByUser), boolInt(u.ContextExceeded), nullString(u.ErrorType),
		nullString(u.ErrorCode), nullString(u.ErrorMessage), nullableBlob(u.RawUsage), nullableBlob(u.ProviderMetadata),
	)
	if err != nil {
		return err
	}
	return s.pruneNow()
}

// UpsertTurnUsage mirrors upsertTurnUsage (keyed by session_id + turn_id).
func (s *SessionStore) UpsertTurnUsage(u sessionstore.TurnUsage) error {
	_, err := s.db.Exec(`
		insert into turn_usage (
			session_id, turn_id, trace_id, user_message_id, status, started_at, first_model_start_at,
			first_token_at, completed_at, duration_ms, time_to_first_token_ms, model_request_count,
			model_retry_count, tool_call_count, tool_error_count, input_tokens, output_tokens,
			reasoning_tokens, cache_creation_input_tokens, cache_read_input_tokens, computed_total_tokens,
			retryable, cancelled_by_user, context_exceeded, error_type, error_code
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(session_id, turn_id) do update set
			trace_id = coalesce(excluded.trace_id, turn_usage.trace_id),
			user_message_id = coalesce(excluded.user_message_id, turn_usage.user_message_id),
			status = excluded.status,
			started_at = min(turn_usage.started_at, excluded.started_at),
			first_model_start_at = coalesce(turn_usage.first_model_start_at, excluded.first_model_start_at),
			first_token_at = coalesce(turn_usage.first_token_at, excluded.first_token_at),
			completed_at = coalesce(excluded.completed_at, turn_usage.completed_at),
			duration_ms = coalesce(excluded.duration_ms, turn_usage.duration_ms),
			time_to_first_token_ms = coalesce(excluded.time_to_first_token_ms, turn_usage.time_to_first_token_ms),
			model_request_count = excluded.model_request_count,
			model_retry_count = excluded.model_retry_count,
			tool_call_count = excluded.tool_call_count,
			tool_error_count = excluded.tool_error_count,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			reasoning_tokens = excluded.reasoning_tokens,
			cache_creation_input_tokens = excluded.cache_creation_input_tokens,
			cache_read_input_tokens = excluded.cache_read_input_tokens,
			computed_total_tokens = excluded.computed_total_tokens,
			retryable = excluded.retryable,
			cancelled_by_user = excluded.cancelled_by_user,
			context_exceeded = excluded.context_exceeded,
			error_type = coalesce(excluded.error_type, turn_usage.error_type),
			error_code = coalesce(excluded.error_code, turn_usage.error_code)`,
		u.SessionID, u.TurnID, nullString(u.TraceID), nullString(u.UserMessageID), u.Status, u.StartedAt,
		nullInt(u.FirstModelStartAt), nullInt(u.FirstTokenAt), nullInt(u.CompletedAt), nullInt(u.DurationMs),
		nullInt(u.TimeToFirstTokenMs), clampNonNegative(u.ModelRequestCount), clampNonNegative(u.ModelRetryCount),
		clampNonNegative(u.ToolCallCount), clampNonNegative(u.ToolErrorCount), clampNonNegative(u.InputTokens),
		clampNonNegative(u.OutputTokens), clampNonNegative(u.ReasoningTokens),
		clampNonNegative(u.CacheCreationInputTokens), clampNonNegative(u.CacheReadInputTokens),
		clampNonNegative(u.ComputedTotalTokens), boolInt(u.Retryable), boolInt(u.CancelledByUser),
		boolInt(u.ContextExceeded), nullString(u.ErrorType), nullString(u.ErrorCode),
	)
	if err != nil {
		return err
	}
	return s.pruneNow()
}

// UpsertToolUsage mirrors upsertToolUsage. Tool rows key on the unique
// (session_id, tool_call_id) index, so repeated lifecycle events merge.
func (s *SessionStore) UpsertToolUsage(u sessionstore.ToolUsage) error {
	_, err := s.db.Exec(`
		insert into tool_usage (
			id, session_id, turn_id, trace_id, tool_call_id, tool_name, side_effect_scope,
			read_only, destructive, approval_status, status, started_at, first_output_at, completed_at,
			duration_ms, time_to_first_output_ms, exit_code, output_bytes, stdout_bytes, stderr_bytes,
			truncated, retry_count, retryable, cancelled_by_user, error_type, error_code, error_message
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			session_id = excluded.session_id,
			turn_id = coalesce(excluded.turn_id, tool_usage.turn_id),
			trace_id = coalesce(excluded.trace_id, tool_usage.trace_id),
			tool_call_id = excluded.tool_call_id,
			tool_name = case when excluded.tool_name = 'unknown' then tool_usage.tool_name else excluded.tool_name end,
			side_effect_scope = coalesce(excluded.side_effect_scope, tool_usage.side_effect_scope),
			read_only = coalesce(excluded.read_only, tool_usage.read_only),
			destructive = coalesce(excluded.destructive, tool_usage.destructive),
			approval_status = coalesce(excluded.approval_status, tool_usage.approval_status),
			status = case
				when tool_usage.status in ('completed', 'error', 'cancelled') and excluded.status = 'running'
					then tool_usage.status
				else excluded.status
			end,
			started_at = min(tool_usage.started_at, excluded.started_at),
			first_output_at = coalesce(tool_usage.first_output_at, excluded.first_output_at),
			completed_at = coalesce(excluded.completed_at, tool_usage.completed_at),
			duration_ms = coalesce(excluded.duration_ms, tool_usage.duration_ms),
			time_to_first_output_ms = coalesce(excluded.time_to_first_output_ms, tool_usage.time_to_first_output_ms),
			exit_code = coalesce(excluded.exit_code, tool_usage.exit_code),
			output_bytes = max(tool_usage.output_bytes, excluded.output_bytes),
			stdout_bytes = max(tool_usage.stdout_bytes, excluded.stdout_bytes),
			stderr_bytes = max(tool_usage.stderr_bytes, excluded.stderr_bytes),
			truncated = max(tool_usage.truncated, excluded.truncated),
			retry_count = excluded.retry_count,
			retryable = excluded.retryable,
			cancelled_by_user = excluded.cancelled_by_user,
			error_type = coalesce(excluded.error_type, tool_usage.error_type),
			error_code = coalesce(excluded.error_code, tool_usage.error_code),
			error_message = coalesce(excluded.error_message, tool_usage.error_message)`,
		u.ID, u.SessionID, nullString(u.TurnID), nullString(u.TraceID), u.ToolCallID, u.ToolName,
		nullString(u.SideEffectScope), nullBool(u.ReadOnly), nullBool(u.Destructive),
		nullString(u.ApprovalStatus), u.Status, u.StartedAt, nullInt(u.FirstOutputAt), nullInt(u.CompletedAt),
		nullInt(u.DurationMs), nullInt(u.TimeToFirstOutputMs), nullInt(u.ExitCode),
		clampNonNegative(u.OutputBytes), clampNonNegative(u.StdoutBytes), clampNonNegative(u.StderrBytes),
		boolInt(u.Truncated), clampNonNegative(u.RetryCount), boolInt(u.Retryable), boolInt(u.CancelledByUser),
		nullString(u.ErrorType), nullString(u.ErrorCode), nullString(u.ErrorMessage),
	)
	if err != nil {
		return err
	}
	return s.pruneNow()
}

// UsageRetentionMs is the 30-day retention window applied after every usage
// write (usage.ts USAGE_RETENTION_MS).
const UsageRetentionMs int64 = 30 * 24 * 60 * 60 * 1000

// PruneUsage deletes usage rows older than the retention window starting at
// beforeTime (inclusive lower bound), matching pruneUsage.
func (s *SessionStore) PruneUsage(beforeTime int64) error {
	tx, err := s.beginTx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"model_usage", "turn_usage", "tool_usage"} {
		if _, err := tx.Exec("delete from "+table+" where started_at < ?", beforeTime); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// pruneNow applies the default 30-day window.
func (s *SessionStore) pruneNow() error { return s.PruneUsage(s.now() - UsageRetentionMs) }

func computedTotalTokens(u sessionstore.ModelUsage) int64 {
	if u.ComputedTotalTokens != nil {
		return *u.ComputedTotalTokens
	}
	input := int64(u.InputTokens)
	if input <= 0 {
		input = int64(u.CacheCreationInputTokens) + int64(u.CacheReadInputTokens)
	}
	return input + clampNonNegative(u.OutputTokens)
}
