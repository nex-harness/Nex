#!/usr/bin/env bash
# Mutation check for go/internal/emitters: applies each mutation, runs the
# emitters tests, expects a failure, then restores the file. Prints
# caught / NOT-CAUGHT per mutation.
set -u
cd "$(dirname "$0")/../../go" || exit 2
PKG=./internal/emitters/

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
  if go test "$PKG" >/tmp/mut-emitters.out 2>&1; then
    echo "NOT-CAUGHT: $desc"
  else
    echo "caught:     $desc"
  fi
  mv "$file.bak" "$file"
}

# ── telemetry fact construction ────────────────────────────────────────
mutate "base: put turnId before sourceCommandId" \
  internal/emitters/facts.go 'o.setStr(hasSourceCommand, "sourceCommandId", sourceCommandID)
	if hasTurn {
		o.set("turnId", v4pub.Str(turnID))
	}' 'if hasTurn {
		o.set("turnId", v4pub.Str(turnID))
	}
	o.setStr(hasSourceCommand, "sourceCommandId", sourceCommandID)'

mutate "turn.started: drop the sourceCommandId slot" \
  internal/emitters/facts.go 'o := t.base(ev, inputID, hasInput, turnID, hasTurn)' \
  'o := t.base(ev, "", false, turnID, hasTurn)'

mutate "stream.chunk: use byte length instead of UTF-16 length" \
  internal/emitters/facts_model.go 'o.set("chunkLength", v4pub.Int(int64(jsLen(delta))))' \
  'o.set("chunkLength", v4pub.Int(int64(len(delta))))'

mutate "stream.chunk: firstChunk always true" \
  internal/emitters/facts_model.go 'o.set("firstChunk", v4pub.Bool(t.firstStreamChunks.add(streamKey)))' \
  'o.set("firstChunk", v4pub.Bool(true))'

mutate "network: keep the queued status instead of returning nil" \
  internal/emitters/facts_model.go 'if status == "model_request_queued" || status == "model_request_admitted" {
		return nil
	}' 'if false {
		return nil
	}'

mutate "network: retry_scheduled emits delayMs before reason" \
  internal/emitters/facts_model.go 'o.set("reason", v4pub.Str(mustString(m, "reason")))
		o.setIf(hasNum(m, "statusCode"), "statusCode", v4pub.Int(mustInt(m, "statusCode")))
		o.number(true, "delayMs", mustNumber(m, "delayMs"))' \
  'o.number(true, "delayMs", mustNumber(m, "delayMs"))
		o.set("reason", v4pub.Str(mustString(m, "reason")))
		o.setIf(hasNum(m, "statusCode"), "statusCode", v4pub.Int(mustInt(m, "statusCode")))'

mutate "usage: prefer input+output over totalTokens" \
  internal/emitters/usage_helpers.go 'if total != nil {
		return *total
	}' 'if false {
		return *total
	}'

mutate "tool: automationId emitted before performance" \
  internal/emitters/facts_tools.go 'o.setIf(performance != nil, "performance", v4pub.RawMessageOf(performance))
	o.setStr(automationID != "", "automationId", automationID)' \
  'o.setStr(automationID != "", "automationId", automationID)
	o.setIf(performance != nil, "performance", v4pub.RawMessageOf(performance))'

mutate "tool: keep toolName after completion (skip delete)" \
  internal/emitters/facts_tools.go 'if phase == "completed" || phase == "failed" {
		t.toolNameByCall.delete(key)
	}' 'if false {
		t.toolNameByCall.delete(key)
	}'

mutate "performance: fsReadMs after patchMatchMs" \
  internal/emitters/performance.go 'o.setIf(hasNum(filesystem, "readMs"), "fsReadMs", v4pub.Int(mustInt(filesystem, "readMs")))
		o.setIf(hasNum(filesystem, "writeMs"), "fsWriteMs", v4pub.Int(mustInt(filesystem, "writeMs")))
		if patch != nil {
			o.setIf(hasNum(patch, "matchMs"), "patchMatchMs", v4pub.Int(mustInt(patch, "matchMs")))
		}' 'if patch != nil {
			o.setIf(hasNum(patch, "matchMs"), "patchMatchMs", v4pub.Int(mustInt(patch, "matchMs")))
		}
		o.setIf(hasNum(filesystem, "readMs"), "fsReadMs", v4pub.Int(mustInt(filesystem, "readMs")))
		o.setIf(hasNum(filesystem, "writeMs"), "fsWriteMs", v4pub.Int(mustInt(filesystem, "writeMs")))'

mutate "permission: resolved also emits toolName" \
  internal/emitters/facts_tools.go 'if resolved != nil {
		o.set("decision", v4pub.Str(mustString(resolved, "decision")))
	}' 'if resolved != nil {
		o.set("toolName", v4pub.Str("Write"))
		o.set("decision", v4pub.Str(mustString(resolved, "decision")))
	}'

mutate "turn.terminal: cancelled maps to failed" \
  internal/emitters/facts_model.go 'case "cancelled":
		return "interrupted"' 'case "cancelled":
		return "failed"'

mutate "turn.terminal: direct inputId ignored (always tracked)" \
  internal/emitters/facts_model.go 'o := t.base(ev, directSourceCommandID, hasDirect, turnID, hasTurn)
	o.set("kind", v4pub.Str("turn.terminal"))' \
  'o := t.base(ev, sourceCommandID, hasSourceCommand, turnID, hasTurn)
	o.set("kind", v4pub.Str("turn.terminal"))'

# ── operation events ───────────────────────────────────────────────────
mutate "operation: turn-started also needs no turnId" \
  internal/emitters/operation.go 'case projection.TypeTurnStarted:
		if !hasTurn {
			return nil
		}' 'case projection.TypeTurnStarted:
		if false {
			return nil
		}'

mutate "operation: tool-started drops toolName" \
  internal/emitters/operation.go 'if v, ok := nonEmpty(m, "toolName"); ok {
			o.set("toolName", v4pub.Str(v))
		}
		return o.raw()
	case "session_ended":' 'return o.raw()
	case "session_ended":'

# ── framing helpers ────────────────────────────────────────────────────
mutate "mcp: processCount emitted before platform" \
  internal/emitters/frames.go 'o.set("platform", v4pub.Str(in.Platform))
	o.set("processCount", v4pub.Int(in.ProcessCount))' \
  'o.set("processCount", v4pub.Int(in.ProcessCount))
	o.set("platform", v4pub.Str(in.Platform))'

mutate "prefs: scope emitted before sessionId" \
  internal/emitters/frames.go 'o.set("sessionId", v4pub.Str(sessionID))
	o.set("scope", v4pub.Str(string(scope)))' \
  'o.set("scope", v4pub.Str(string(scope)))
	o.set("sessionId", v4pub.Str(sessionID))'

mutate "state.updated: envelope drops the revision field" \
  internal/emitters/frames.go 'env.set("revision", v4pub.Int(revision))
	env.set("scope", v4pub.Str("session"))' 'env.set("scope", v4pub.Str("session"))'

# ── context usage ──────────────────────────────────────────────────────
mutate "context: mcp tools counted as system tools" \
  internal/emitters/contextusage.go 'if isMCPToolName(t.Name) {
			mcpToolChars += chars
			mcpToolTokens += tokens
		} else {
			sysToolChars += chars
			sysToolTokens += tokens
		}' 'if false {
			mcpToolChars += chars
			mcpToolTokens += tokens
		} else {
			sysToolChars += chars
			sysToolTokens += tokens
		}'

mutate "context: keep categories with zero chars" \
  internal/emitters/contextusage.go 'if c.tokens <= 0 || c.chars <= 0 {
			continue
		}' 'if c.tokens < 0 {
			continue
		}'

mutate "context: meta-user reminder counts into messages" \
  internal/emitters/contextusage.go 'if m.Role == "system" || m.MetaUserContext || isMetaUserContextMessage(m) {
			continue
		}' 'if m.Role == "system" {
			continue
		}'

mutate "context: tool content drops the inputSchema" \
  internal/emitters/contextusage.go 'if len(t.InputSchema) > 0 {
		o.set("inputSchema", v4pub.RawMessageOf(compactRaw(t.InputSchema)))
	}' ''

mutate "context: estimateTokens divides by 4" \
  internal/emitters/contextusage.go 'const estimateTokenCharDivisor = 3' \
  'const estimateTokenCharDivisor = 4'
