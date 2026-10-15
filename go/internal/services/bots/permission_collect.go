package bots

import "strings"

// permission_collect.go holds the recursive collectors of
// permission-request-preview.ts. They walk arbitrary JSON produced by a model or
// a provider, so each one carries its own cycle guard, its own cap and no
// assumption that a key exists.
// collectFilePaths ports collectFilePaths: only path-like KEYS are followed; a
// bare string value is never treated as a path, which is why the scanner is
// key-driven rather than value-driven.
func collectFilePaths(value any, paths []string, seen visitedSet) []string {
	if len(paths) >= maxPermissionFilePaths {
		return paths
	}
	if items, ok := value.([]any); ok {
		if markVisited(seen, items) {
			return paths
		}
		for _, item := range items {
			paths = collectFilePaths(item, paths, seen)
			if len(paths) >= maxPermissionFilePaths {
				return paths
			}
		}
		return paths
	}
	record, ok := value.(map[string]any)
	if !ok || markVisited(seen, record) {
		return paths
	}
	for key, candidate := range record {
		normalizedKey := strings.ToLower(key)
		if ignoredDirectoryKeys[normalizedKey] {
			continue
		}
		if filePathKeys[normalizedKey] {
			paths = extractPathsFromCandidate(candidate, paths, seen)
			if len(paths) >= maxPermissionFilePaths {
				return paths
			}
			continue
		}
		if isJSONRecord(candidate) {
			paths = collectFilePaths(candidate, paths, seen)
			if len(paths) >= maxPermissionFilePaths {
				return paths
			}
			continue
		}
		if _, ok := candidate.([]any); ok {
			paths = collectFilePaths(candidate, paths, seen)
			if len(paths) >= maxPermissionFilePaths {
				return paths
			}
		}
	}
	return paths
}

// collectFileChanges ports collectFileChanges: a `changes` map whose values
// carry a type of add/update, collected recursively.
func collectFileChanges(value any, changes []PermissionFileChange, seen visitedSet) []PermissionFileChange {
	if items, ok := value.([]any); ok {
		if markVisited(seen, items) {
			return changes
		}
		for _, item := range items {
			changes = collectFileChanges(item, changes, seen)
		}
		return changes
	}
	record, ok := value.(map[string]any)
	if !ok || markVisited(seen, record) {
		return changes
	}

	if changesMap, ok := record["changes"].(map[string]any); ok {
		for path, changeValue := range changesMap {
			change, ok := changeValue.(map[string]any)
			if !ok {
				continue
			}
			changeType, _ := change["type"].(string)
			if changeType != "add" && changeType != "update" {
				continue
			}
			duplicate := false
			for _, existing := range changes {
				if existing.Path == path && existing.Type == changeType {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			changes = append(changes, PermissionFileChange{Path: path, Type: changeType})
		}
	}
	for _, nested := range record {
		if isJSONRecord(nested) {
			changes = collectFileChanges(nested, changes, seen)
			continue
		}
		if _, ok := nested.([]any); ok {
			changes = collectFileChanges(nested, changes, seen)
		}
	}
	return changes
}
