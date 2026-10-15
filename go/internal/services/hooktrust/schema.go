// schema.go: the strict workspace-hooks config schema (the zod
// workspaceHooksConfigSchema port) — unknown keys rejected, positive
// numbers, non-empty matchers, discriminated command/process hooks.
package hooktrust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ParseHooksConfigValue applies the strict hooks schema to a decoded JSON
// value (workspaceHooksConfigSchema). nil means "no hooks key" (skip the
// file); a validation error distinguishes corrupt files (recorded as errors).
func ParseHooksConfigValue(value any) (*HooksConfig, error) {
	if value == nil {
		return nil, nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("hooks must be an object")
	}
	// strict(): unknown keys rejected.
	known := map[string]bool{"enabled": true, "timeoutMs": true, "maxOutputBytes": true, "events": true}
	for key := range record {
		if !known[key] {
			return nil, fmt.Errorf("unknown hooks key: %s", key)
		}
	}
	hooks := &HooksConfig{}
	if enabled, ok := record["enabled"]; ok && enabled != nil {
		value, ok := enabled.(bool)
		if !ok {
			return nil, fmt.Errorf("enabled must be boolean")
		}
		hooks.Enabled = &value
	}
	if timeoutMs, ok := record["timeoutMs"]; ok && timeoutMs != nil {
		value, err := positiveNumber(timeoutMs)
		if err != nil {
			return nil, fmt.Errorf("timeoutMs: %w", err)
		}
		hooks.TimeoutMs = &value
	}
	if maxOutputBytes, ok := record["maxOutputBytes"]; ok && maxOutputBytes != nil {
		value, err := positiveNumber(maxOutputBytes)
		if err != nil {
			return nil, fmt.Errorf("maxOutputBytes: %w", err)
		}
		hooks.MaxOutputBytes = &value
	}
	if events, ok := record["events"]; ok && events != nil {
		eventsRecord, ok := events.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("events must be an object")
		}
		hooks.Events = map[string][]MatcherConfig{}
		for eventName, matchers := range eventsRecord {
			if !IsEventName(eventName) {
				return nil, fmt.Errorf("unknown event: %s", eventName)
			}
			matcherList, ok := matchers.([]any)
			if !ok {
				return nil, fmt.Errorf("event %s must be an array", eventName)
			}
			parsedMatchers, err := parseMatcherConfigs(matcherList)
			if err != nil {
				return nil, fmt.Errorf("event %s: %w", eventName, err)
			}
			if len(parsedMatchers) == 0 {
				return nil, fmt.Errorf("event %s: matchers must be non-empty", eventName)
			}
			hooks.Events[eventName] = parsedMatchers
		}
	}
	return hooks, nil
}

func parseMatcherConfigs(list []any) ([]MatcherConfig, error) {
	var out []MatcherConfig
	for _, item := range list {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("matcher must be an object")
		}
		matcher := MatcherConfig{}
		for key := range record {
			if key != "matcher" && key != "hooks" {
				return nil, fmt.Errorf("unknown matcher key: %s", key)
			}
		}
		if value, ok := record["matcher"]; ok && value != nil {
			s, ok := value.(string)
			if !ok || s == "" {
				return nil, fmt.Errorf("matcher must be a non-empty string")
			}
			matcher.Matcher = s
		}
		hooksList, ok := record["hooks"].([]any)
		if !ok || len(hooksList) == 0 {
			return nil, fmt.Errorf("hooks must be a non-empty array")
		}
		for _, hookItem := range hooksList {
			hookRecord, ok := hookItem.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("hook must be an object")
			}
			hook, err := parseHookDefinition(hookRecord)
			if err != nil {
				return nil, err
			}
			matcher.Hooks = append(matcher.Hooks, *hook)
		}
		out = append(out, matcher)
	}
	return out, nil
}

// parseHookDefinition parses one process/command hook (passthrough fields
// preserved for round-trip writes; unknown keys allowed by passthrough).
func parseHookDefinition(record map[string]any) (*Definition, error) {
	hookType, _ := record["type"].(string)
	if hookType != "command" && hookType != "process" {
		return nil, fmt.Errorf("hook type must be command or process")
	}
	command, _ := record["command"].(string)
	if command == "" {
		return nil, fmt.Errorf("hook command must be a non-empty string")
	}
	definition := &Definition{Type: hookType, Command: command}
	if enabled, ok := record["enabled"]; ok && enabled != nil {
		value, ok := enabled.(bool)
		if !ok {
			return nil, fmt.Errorf("enabled must be boolean")
		}
		definition.Enabled = &value
	}
	if hookType == "process" {
		if args, ok := record["args"]; ok && args != nil {
			list, ok := args.([]any)
			if !ok {
				return nil, fmt.Errorf("args must be an array of strings")
			}
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("args must be an array of strings")
				}
				definition.Args = append(definition.Args, s)
			}
		}
	} else {
		if async, ok := record["async"]; ok && async != nil {
			value, ok := async.(bool)
			if !ok {
				return nil, fmt.Errorf("async must be boolean")
			}
			definition.Async = &value
		}
		if shell, ok := record["shell"]; ok && shell != nil {
			switch value := shell.(type) {
			case bool:
				if !value {
					return nil, fmt.Errorf("shell must be true or a string")
				}
				definition.Shell = value
			case string:
				if value == "" {
					return nil, fmt.Errorf("shell must be true or a non-empty string")
				}
				definition.Shell = value
			default:
				return nil, fmt.Errorf("shell must be true or a string")
			}
		}
	}
	if timeoutMs, ok := record["timeoutMs"]; ok && timeoutMs != nil {
		value, err := positiveNumber(timeoutMs)
		if err != nil {
			return nil, fmt.Errorf("timeoutMs: %w", err)
		}
		definition.TimeoutMs = &value
	}
	if timeout, ok := record["timeout"]; ok && timeout != nil {
		value, err := positiveNumber(timeout)
		if err != nil {
			return nil, fmt.Errorf("timeout: %w", err)
		}
		definition.Timeout = &value
	}
	if statusMessage, ok := record["statusMessage"]; ok && statusMessage != nil {
		value, ok := statusMessage.(string)
		if !ok || value == "" {
			return nil, fmt.Errorf("statusMessage must be a non-empty string")
		}
		definition.StatusMessage = value
	}
	return definition, nil
}

func positiveNumber(value any) (float64, error) {
	number, ok := value.(float64)
	if !ok || number <= 0 || number != float64(int64(number)) && number != number {
		return 0, fmt.Errorf("must be a positive number")
	}
	if number <= 0 {
		return 0, fmt.Errorf("must be a positive number")
	}
	return number, nil
}

// ReadProjectSources ports readWorkspaceHookProjectSources: parse every
// discovered config, keep the ones with a hooks key, collect errors.
func ReadProjectSources(workingDirectory, explicitProjectConfigPath string) (sources []SourceInput, errors []SourceReadError) {
	refs := DiscoverConfigPaths(workingDirectory, explicitProjectConfigPath)
	for discoveryOrder, ref := range refs {
		raw, err := os.ReadFile(ref.Path)
		if err != nil {
			errors = append(errors, SourceReadError{Path: ref.Path, Message: err.Error()})
			continue
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			errors = append(errors, SourceReadError{Path: ref.Path, Message: err.Error()})
			continue
		}
		if value == nil {
			continue
		}
		hooksValue, ok := value["hooks"]
		if !ok || hooksValue == nil {
			continue
		}
		hooks, err := ParseHooksConfigValue(hooksValue)
		if err != nil {
			errors = append(errors, SourceReadError{Path: ref.Path, Message: err.Error()})
			continue
		}
		if hooks == nil {
			continue
		}
		sources = append(sources, CreateSourceInput(ref.Path, workingDirectory, hooks, discoveryOrder, ref.ExplicitProjectConfig))
	}
	return sources, errors
}

// SourceReadError ports WorkspaceHookSourceReadError.
type SourceReadError struct {
	Path    string
	Message string
}

// AtomicWriteConfig ports atomicWriteWorkspaceHookConfig: temp file in the
// destination directory, written, synced, closed, renamed — Runtime
// watchers never observe a partial config.
func AtomicWriteConfig(filePath string, value map[string]any) error {
	directory := filepath.Dir(filePath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tempPath := filepath.Join(directory, fmt.Sprintf(".%s.%d.tmp", filepath.Base(filePath), os.Getpid()))
	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(tempPath)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tempPath)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}

// StorageRootFromUserConfig resolves the trust-store storage root from the
// user nex config's storage.dir (~ expansion, absolute vs home-relative).
func StorageRootFromUserConfig(homeDir, configuredStorageDir string) string {
	configured := strings.TrimSpace(configuredStorageDir)
	if configured == "" {
		return filepath.Join(homeDir, ".nex")
	}
	if strings.HasPrefix(configured, "~/") {
		return filepath.Join(homeDir, configured[2:])
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	if absolute, err := filepath.Abs(filepath.Join(homeDir, configured)); err == nil {
		return absolute
	}
	return filepath.Join(homeDir, configured)
}
