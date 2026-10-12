package emitters

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/v4pub"
)

// toolPerformance ports toToolPerformanceFact: a strict whitelist flattening of
// ToolExecutionTelemetry into the telemetry fact's `performance` block, in the
// exact field order of the TypeScript literal. Returns nil when perf is absent
// or produces no fields.
func toolPerformance(perf map[string]json.RawMessage) json.RawMessage {
	if perf == nil {
		return nil
	}
	detail := record(perf, "detail")
	kind := mustString(detail, "kind")

	var command map[string]json.RawMessage
	if kind == "command" {
		command = record(detail, "command")
	}
	var filesystem map[string]json.RawMessage
	if kind == "filesystem" || kind == "patch" {
		filesystem = record(detail, "filesystem")
	}
	var patch map[string]json.RawMessage
	if kind == "patch" {
		patch = record(detail, "patch")
	}

	o := newObj()
	o.setIf(hasNum(perf, "totalMs"), "totalMs", v4pub.Int(mustInt(perf, "totalMs")))
	o.setIf(hasNum(perf, "permissionWaitMs"), "permissionWaitMs", v4pub.Int(mustInt(perf, "permissionWaitMs")))
	if command != nil {
		o.setIf(hasNum(command, "runMs"), "commandRunMs", v4pub.Int(mustInt(command, "runMs")))
		o.setIf(hasNum(command, "firstOutputMs"), "firstOutputMs", v4pub.Int(mustInt(command, "firstOutputMs")))
		o.setIf(hasNum(command, "noOutputMs"), "noOutputMs", v4pub.Int(mustInt(command, "noOutputMs")))
		o.setIf(hasNum(command, "exitCode"), "exitCode", v4pub.Int(mustInt(command, "exitCode")))
		o.setIf(hasBool(command, "timedOut"), "timedOut", v4pub.Bool(mustBool(command, "timedOut")))
		o.setIf(hasNum(command, "outputBytes"), "outputBytes", v4pub.Int(mustInt(command, "outputBytes")))
		o.setStr(mustNonEmpty(command, "category"), "commandCategory", mustString(command, "category"))
		o.setStr(mustNonEmpty(command, "name"), "commandName", mustString(command, "name"))
		o.setIf(hasNum(command, "count"), "commandCount", v4pub.Int(mustInt(command, "count")))
		o.setStr(mustNonEmpty(command, "status"), "commandStatus", mustString(command, "status"))
	}
	if filesystem != nil {
		o.setIf(hasNum(filesystem, "readMs"), "fsReadMs", v4pub.Int(mustInt(filesystem, "readMs")))
		o.setIf(hasNum(filesystem, "writeMs"), "fsWriteMs", v4pub.Int(mustInt(filesystem, "writeMs")))
		if patch != nil {
			o.setIf(hasNum(patch, "matchMs"), "patchMatchMs", v4pub.Int(mustInt(patch, "matchMs")))
		}
		o.setIf(hasNum(filesystem, "fileCount"), "fileCount", v4pub.Int(mustInt(filesystem, "fileCount")))
		o.setIf(hasNum(filesystem, "totalBytes"), "totalBytes", v4pub.Int(mustInt(filesystem, "totalBytes")))
		o.setIf(hasNum(filesystem, "maxFileBytes"), "maxFileBytes", v4pub.Int(mustInt(filesystem, "maxFileBytes")))
		if patch != nil {
			o.setIf(hasNum(patch, "hunkCount"), "hunkCount", v4pub.Int(mustInt(patch, "hunkCount")))
			o.setIf(hasNum(patch, "matchAttempts"), "matchAttempts", v4pub.Int(mustInt(patch, "matchAttempts")))
		}
		o.setStr(mustNonEmpty(filesystem, "workspaceKind"), "workspaceKind", mustString(filesystem, "workspaceKind"))
	}
	if len(o.keys) == 0 {
		return nil
	}
	return o.raw()
}

func hasBool(m map[string]json.RawMessage, key string) bool {
	_, ok := boolField(m, key)
	return ok
}
