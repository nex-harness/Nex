// servermap.go: the mcp.servers / mcpServers map codecs — read/write into
// the surrounding config object, the enabled semantics, and the legacy
// enable → enabled migration.
package mcpsync

// readServerEnabled ports readServerEnabled.
func readServerEnabled(config map[string]any) bool {
	return config[enabledKey] != false
}

// setServerEnabled ports setServerEnabled: enabled is the default (the
// field is dropped); disabled writes enabled:false and clears any legacy
// enable residue.
func setServerEnabled(config map[string]any, enabled bool) map[string]any {
	next := map[string]any{}
	for key, value := range config {
		if key == legacyEnableKey || key == enabledKey {
			continue
		}
		next[key] = value
	}
	if !enabled {
		next[enabledKey] = false
	}
	return next
}

// readServerMapWithLegacyMigration ports readServerMapWithLegacyMigration:
// collapse legacy enable into enabled in memory, persist when changed
// (idempotent; a write failure does not block the read).
func readServerMapWithLegacyMigration(parsed map[string]any, configKeyName string) (map[string]any, bool) {
	serverMap := readServerMapFromJson(parsed, configKeyName)
	changed := false
	migrated := map[string]any{}
	for name, value := range serverMap {
		config, ok := value.(map[string]any)
		if !ok {
			migrated[name] = value
			continue
		}
		if _, hasLegacy := config[legacyEnableKey]; !hasLegacy {
			migrated[name] = config
			continue
		}
		changed = true
		// Conflicting fields resolve to disabled: the desktop wrote
		// enable:false without cleaning an imported enabled:true, and
		// reading enabled first would restart a server the user turned off.
		disabled := config[legacyEnableKey] == false || config[enabledKey] == false
		migrated[name] = setServerEnabled(config, !disabled)
	}
	return migrated, changed
}

func readServerMapFromJson(parsed map[string]any, configKeyName string) map[string]any {
	if configKeyName == "mcp.servers" {
		mcp, _ := parsed["mcp"].(map[string]any)
		if mcp == nil {
			return map[string]any{}
		}
		servers, _ := mcp["servers"].(map[string]any)
		if servers == nil {
			return map[string]any{}
		}
		return servers
	}
	servers, _ := parsed[configKeyName].(map[string]any)
	if servers == nil {
		return map[string]any{}
	}
	return servers
}

func writeServerMapToJson(current map[string]any, configKeyName string, servers map[string]any) map[string]any {
	if configKeyName != "mcp.servers" {
		next := map[string]any{}
		for key, value := range current {
			next[key] = value
		}
		next[configKeyName] = servers
		return next
	}
	next := map[string]any{}
	for key, value := range current {
		next[key] = value
	}
	currentMcp, _ := current["mcp"].(map[string]any)
	if currentMcp == nil {
		currentMcp = map[string]any{}
	}
	nextMcp := map[string]any{}
	for key, value := range currentMcp {
		nextMcp[key] = value
	}
	nextMcp["servers"] = servers
	next["mcp"] = nextMcp
	return next
}
