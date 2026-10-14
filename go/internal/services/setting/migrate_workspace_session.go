// migrate_workspace_session.go ports migrateLegacyWorkspaceSession
// (validationAppSettings.ts): lastOpenTabs + remoteWorkspaceHistory + legacy
// historyId entries all fold into lastWorkspaceSession, and the two legacy
// fields are removed so there is a single source of truth.
//
// 旧 setting.json 把本地会话、远端历史、组合会话拆在三处存。TS 在 schema 解析
// 阶段统一合并进 lastWorkspaceSession 并删除旧字段；Go 必须产生完全相同的
// 结果（golden 由行为捕获验证）。

package setting

// MigrateLegacyWorkspaceSession ports the same-named TS function. raw is
// mutated in place.
func MigrateLegacyWorkspaceSession(raw map[string]any) {
	lastWorkspaceSession := asArray(raw["lastWorkspaceSession"])

	hasLegacyRemoteEntries := false
	for _, entry := range lastWorkspaceSession {
		if entryObj, ok := entry.(map[string]any); ok {
			if _, has := entryObj["historyId"]; has {
				hasLegacyRemoteEntries = true
				break
			}
		}
	}

	legacyRemoteHistory := asArray(raw["remoteWorkspaceHistory"])
	legacyRemoteHistoryByID := map[string]map[string]any{}
	for _, entry := range legacyRemoteHistory {
		entryObj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		sanitized := cloneMap(entryObj)
		// 更老的 remoteWorkspaceHistory 可能保存了已退役资源包 ID；剥离后再校验。
		sanitized["target"] = StripHistoricalRemoteResourcePackages(sanitized["target"])
		if validateLegacyRemoteWorkspaceHistoryEntry(sanitized) != nil {
			continue
		}
		if id, ok := sanitized["id"].(string); ok {
			legacyRemoteHistoryByID[id] = sanitized
		}
	}

	var migratedEntries []map[string]any
	if len(lastWorkspaceSession) > 0 {
		for _, entry := range lastWorkspaceSession {
			entryObj, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := entryObj["kind"].(string); kind == "local" {
				if workspacePath, ok := entryObj["workspacePath"].(string); ok {
					purpose := "project"
					if p, _ := entryObj["workspacePurpose"].(string); p == "conversation" {
						purpose = "conversation"
					}
					migratedEntries = append(migratedEntries, map[string]any{
						"kind":             "local",
						"workspacePath":    workspacePath,
						"workspacePurpose": purpose,
					})
					continue
				}
			}
			if kind, _ := entryObj["kind"].(string); kind == "remote" {
				if workspacePath, wsOK := entryObj["workspacePath"].(string); wsOK {
					if target, tOK := entryObj["target"]; tOK {
						result := map[string]any{"kind": "remote", "workspacePath": workspacePath}
						// Spread order: the TS spreads rawEntry first, so the
						// original key order (kind, workspacePath, …, target,
						// …) carries over; key order is normalized at write.
						for k, v := range entryObj {
							if k != "kind" && k != "workspacePath" {
								result[k] = v
							}
						}
						result["target"] = StripHistoricalRemoteResourcePackages(target)
						migratedEntries = append(migratedEntries, result)
						continue
					}
				}
				if historyID, ok := entryObj["historyId"].(string); ok {
					if legacy, found := legacyRemoteHistoryByID[historyID]; found {
						remote := map[string]any{
							"kind":                 "remote",
							"workspacePath":        legacy["workspacePath"],
							"target":               StripHistoricalRemoteResourcePackages(legacy["target"]),
							"lastOpenedAt":         legacy["lastOpenedAt"],
							"lastConnectionStatus": legacy["lastConnectionStatus"],
						}
						if v, ok := legacy["localWorkspacePath"]; ok {
							remote["localWorkspacePath"] = v
						}
						if v, ok := legacy["workspaceIdentity"]; ok {
							remote["workspaceIdentity"] = v
						}
						if v, ok := legacy["lastConnectionError"]; ok {
							remote["lastConnectionError"] = v
						}
						migratedEntries = append(migratedEntries, remote)
					}
					continue
				}
			}
			// Anything else (unknown kind, non-record) is dropped.
		}
	}

	var migratedLegacyLocalEntries []map[string]any
	if lastOpenTabs, ok := raw["lastOpenTabs"].([]any); ok {
		for _, entry := range lastOpenTabs {
			if workspacePath, ok := entry.(string); ok {
				migratedLegacyLocalEntries = append(migratedLegacyLocalEntries, map[string]any{
					"kind":             "local",
					"workspacePath":    workspacePath,
					"workspacePurpose": "project",
				})
			}
		}
	}

	existingLocalWorkspacePaths := map[string]bool{}
	for _, entry := range migratedEntries {
		if kind, _ := entry["kind"].(string); kind == "local" {
			if p, ok := entry["workspacePath"].(string); ok {
				existingLocalWorkspacePaths[p] = true
			}
		}
	}
	nextWorkspaceSession := append([]map[string]any{}, migratedEntries...)
	for _, entry := range migratedLegacyLocalEntries {
		if !existingLocalWorkspacePaths[entry["workspacePath"].(string)] {
			nextWorkspaceSession = append(nextWorkspaceSession, entry)
		}
	}

	if len(nextWorkspaceSession) > 0 || hasLegacyRemoteEntries || raw["lastOpenTabs"] != nil {
		asAny := make([]any, len(nextWorkspaceSession))
		for i, entry := range nextWorkspaceSession {
			asAny[i] = entry
		}
		raw["lastWorkspaceSession"] = asAny
	}
	delete(raw, "lastOpenTabs")
	delete(raw, "remoteWorkspaceHistory")
}

// StripHistoricalRemoteResourcePackages ports stripHistoricalRemoteResourcePackages.
func StripHistoricalRemoteResourcePackages(target any) any {
	obj, ok := target.(map[string]any)
	if !ok {
		return target
	}
	if kind, _ := obj["kind"].(string); kind != "ssh" {
		return target
	}
	if _, has := obj["resourcePackages"]; !has {
		return target
	}
	next := cloneMap(obj)
	delete(next, "resourcePackages")
	return next
}

// validateLegacyRemoteWorkspaceHistoryEntry ports
// legacyRemoteWorkspaceHistoryEntrySchema (non-strict z.object: extra keys OK).
func validateLegacyRemoteWorkspaceHistoryEntry(v map[string]any) error {
	if !nonEmptyStringField(v, "id") {
		return errType
	}
	if !nonEmptyStringField(v, "workspacePath") {
		return errType
	}
	if value, ok := v["localWorkspacePath"]; ok && !isString(value) {
		return errType
	}
	if value, ok := v["workspaceIdentity"]; ok && !nonEmptyString(value) {
		return errType
	}
	if err := validateRemoteWorkspaceTarget(v["target"]); err != nil {
		return err
	}
	lastOpenedAt, err := intField(v, "lastOpenedAt")
	if err != nil || lastOpenedAt < 0 {
		return errType
	}
	status, _ := v["lastConnectionStatus"].(string)
	if status != "connected" && status != "failed" {
		return errType
	}
	if value, ok := v["lastConnectionError"]; ok && !isString(value) {
		return errType
	}
	return nil
}
