#!/usr/bin/env bash
# Mutation check for go/internal/projection: applies each mutation, runs the
# projection tests, expects a failure, then restores the file. Prints
# CAUGHT / NOT-CAUGHT per mutation.
set -u
cd "$(dirname "$0")/../../go" || exit 2
PKG=./internal/projection/

mutate() {
  local desc="$1" file="$2" from="$3" to="$4"
  cp "$file" "$file.bak"
  python3 - "$file" "$from" "$to" <<'PY'
import sys
path, frm, to = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(path).read()
if frm not in s:
    print("MUTATION-PATTERN-MISSING", file=sys.stderr)
    sys.exit(3)
open(path, "w").write(s.replace(frm, to, 1))
PY
  if [ $? -ne 0 ]; then
    mv "$file.bak" "$file"
    echo "SKIP (pattern missing): $desc"
    return
  fi
  if go test "$PKG" >/tmp/mut.out 2>&1; then
    echo "NOT-CAUGHT: $desc"
  else
    echo "caught:     $desc"
  fi
  mv "$file.bak" "$file"
}

mutate "coalesce: drop the row.upserted-swallows-row.delta rule" \
  internal/projection/coalesce.go 'if opOf(prev) == "row.delta" && int64Of(prev.get("rowId")) == rowIDOf(row) {' \
  'if false && opOf(prev) == "row.delta" && int64Of(prev.get("rowId")) == rowIDOf(row) {'

mutate "coalesce: drop adjacent state.updated merge" \
  internal/projection/coalesce.go 'if opOf(delta) == "state.updated" && last != nil && opOf(last) == "state.updated" {' \
  'if false && opOf(delta) == "state.updated" && last != nil && opOf(last) == "state.updated" {'

mutate "apply: row.appended does not bump totalCount" \
  internal/projection/apply.go 'nextRows.put("totalCount", int64Of(rows.get("totalCount"))+1)' \
  'nextRows.put("totalCount", int64Of(rows.get("totalCount")))'

mutate "apply: row.removed keeps firstRowId instead of clearing on full trim" \
  internal/projection/apply.go 'nextRows.put("firstRowId", nil)' \
  'nextRows.put("firstRowId", firstRowID)'

mutate "apply: appendToRow ignores the kind guard for text" \
  internal/projection/apply.go 'if kindOf(row) == "assistantText" || kindOf(row) == "reasoning" {' \
  'if true {'

mutate "revision: drop meta from revision-bearing keys" \
  internal/projection/state.go '"control", "availability", "inputRouting", "meta", "config",' \
  '"control", "availability", "inputRouting", "config",'

mutate "availability: draft reports compact allowed" \
  internal/projection/state.go 'compact = denied("idleCannotCompact")' \
  'compact = allowed()'

mutate "inputRouting: running in queue mode reports startNow" \
  internal/projection/state.go 'mode := "enqueue"' \
  'mode := "startNow"'

mutate "turn: entityId defaults are swapped (toolCall uses assistant id)" \
  internal/projection/handlers_stream.go 'row := p.rowBase(event, turnID, stream.toolCallID).' \
  'row := p.rowBase(event, turnID, stream.entityID).'

mutate "turn: header rowId registered under the wrong turn key" \
  internal/projection/handlers_turn.go 'p.turnHeaderRowIDByTurnID[turnID] = rowIDOf(header)' \
  'p.turnHeaderRowIDByTurnID[event.ID] = rowIDOf(header)'

mutate "model: skip usage cumulative accumulation" \
  internal/projection/handlers_model.go 'set("inputTokens", int64Of(cumulative.get("inputTokens"))+usageObj.InputTokens).' \
  'set("inputTokens", int64Of(cumulative.get("inputTokens"))).'

mutate "model: file-tool preview throttle interval too small" \
  internal/projection/handlers_stream.go 'const fileStreamingInputPreviewMinIntervalMs = 1000' \
  'const fileStreamingInputPreviewMinIntervalMs = 0'

mutate "tools: permissionResolved deny maps to running instead of cancelled" \
  internal/projection/handlers_tools.go 'status := "running"
	if decision == "deny" {
		status = "cancelled"
	}' \
  'status := "running"'

mutate "tools: toolCallResult always success" \
  internal/projection/handlers_tools.go 'put("status", map[bool]string{true: "success", false: "error"}[result.Success]).' \
  'put("status", "success").'

mutate "actions: userInput never gets canEdit" \
  internal/projection/actions.go 'if latestEditableRowID != nil && rowIDOf(row) == *latestEditableRowID {' \
  'if false && latestEditableRowID != nil && rowIDOf(row) == *latestEditableRowID {'

mutate "json: encoder emits the first two keys swapped (order-broken)" \
  internal/projection/json.go 'func (o *obj) MarshalJSON() ([]byte, error) {' \
  'func (o *obj) MarshalJSON() ([]byte, error) {
	o.keys = append([]string{o.keys[0]}, o.keys...)'

echo "mutation check complete"
