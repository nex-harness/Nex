// validate_settings.go: the nested settings validators — workspace session
// entries, remote targets, viewport preference, pet settings, release-notes
// payloads (validationAppSettings.ts).

package setting

import (
	"net/url"
	"strings"
)

// validateWorkspaceSessionArray ports z.array(appWorkspaceSessionEntrySchema).
func validateWorkspaceSessionArray(v any) error {
	arr, ok := v.([]any)
	if !ok {
		return errType
	}
	for _, item := range arr {
		if err := ValidateWorkspaceSessionEntry(item); err != nil {
			return err
		}
	}
	return nil
}

// ValidateWorkspaceSessionEntry ports appWorkspaceSessionEntrySchema
// (non-strict objects; local entries default workspacePurpose to "project",
// which migration already materialized).
func ValidateWorkspaceSessionEntry(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	kind, _ := obj["kind"].(string)
	switch kind {
	case "local":
		if !nonEmptyStringField(obj, "workspacePath") {
			return errRange
		}
		if purpose, present := obj["workspacePurpose"]; present {
			p, _ := purpose.(string)
			if p != "project" && p != "conversation" {
				return errEnum
			}
		}
		return nil
	case "remote":
		if !nonEmptyStringField(obj, "workspacePath") {
			return errRange
		}
		if value, ok := obj["localWorkspacePath"]; ok && !isString(value) {
			return errType
		}
		if value, ok := obj["workspaceIdentity"]; ok && !nonEmptyString(value) {
			return errType
		}
		if err := validateRemoteWorkspaceTarget(obj["target"]); err != nil {
			return err
		}
		lastOpenedAt, err := intField(obj, "lastOpenedAt")
		if err != nil || lastOpenedAt < 0 {
			return errType
		}
		status, _ := obj["lastConnectionStatus"].(string)
		if status != "connected" && status != "failed" {
			return errEnum
		}
		if value, ok := obj["lastConnectionError"]; ok && !isString(value) {
			return errType
		}
		return nil
	default:
		return errEnum
	}
}

// validateRemoteWorkspaceTarget ports remoteWorkspaceTargetSchema.
func validateRemoteWorkspaceTarget(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	kind, _ := obj["kind"].(string)
	switch kind {
	case "ssh":
		if !nonEmptyStringField(obj, "host") || !nonEmptyStringField(obj, "username") {
			return errRange
		}
		if value, ok := obj["port"]; ok {
			n, err := toInt(value)
			if err != nil || n <= 0 || n > 65535 {
				return errRange
			}
		}
		if value, ok := obj["sshConfigAlias"]; ok && !nonEmptyString(value) {
			return errRange
		}
		if value, ok := obj["privateKeyPath"]; ok && !isString(value) {
			return errType
		}
		if value, ok := obj["assetInstallMode"]; ok {
			s, _ := value.(string)
			if s != "local-download-upload" && s != "remote-download" {
				return errEnum
			}
		}
		if value, ok := obj["resourcePackages"]; ok {
			if err := validateRemoteResourcePackages(value); err != nil {
				return err
			}
		}
		if value, ok := obj["passwordCredentialKey"]; ok && !nonEmptyString(value) {
			return errRange
		}
		if value, ok := obj["privateKeyPassphraseCredentialKey"]; ok && !nonEmptyString(value) {
			return errRange
		}
		return nil
	case "wsl":
		if value, ok := obj["distro"]; ok && !isString(value) {
			return errType
		}
		if value, ok := obj["user"]; ok {
			if err := validateWslUser(value); err != nil {
				return err
			}
		}
		return nil
	case "docker":
		if !nonEmptyStringField(obj, "container") {
			return errRange
		}
		return nil
	default:
		return errEnum
	}
}

func validateRemoteResourcePackages(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	selected, ok := obj["selectedPackageIds"]
	if !ok {
		return nil
	}
	arr, ok := selected.([]any)
	if !ok {
		return errType
	}
	for _, item := range arr {
		s, _ := item.(string)
		if !remoteResourcePackageSet[s] {
			return errEnum
		}
	}
	return nil
}

// validateWslUser ports wslUserSchema: trim, ≤64, empty allowed, then the
// isValidWslUser refine (no control chars, no : / \).
func validateWslUser(v any) error {
	s, ok := v.(string)
	if !ok {
		return errType
	}
	trimmed := trimSpace(s)
	if len([]rune(trimmed)) > 64 {
		return errRange
	}
	if trimmed == "" {
		return nil
	}
	for _, r := range trimmed {
		if r < 32 || r == 127 {
			return errRange
		}
	}
	if strings.ContainsAny(trimmed, `:/\`) {
		return errRange
	}
	return nil
}

func validatePostUpdateReleaseNotes(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	for _, key := range []string{"version", "title", "markdown"} {
		if !nonEmptyStringField(obj, key) {
			return errRange
		}
	}
	if value, ok := obj["releaseDate"]; ok && !nonEmptyString(value) {
		return errRange
	}
	if value, ok := obj["releaseNotesByLocale"]; ok && value != nil {
		locales, ok := value.(map[string]any)
		if !ok {
			return errType
		}
		for locale, entry := range locales {
			if locale != "zh-CN" && locale != "en-US" {
				return errKeys
			}
			entryObj, ok := entry.(map[string]any)
			if !ok {
				return errType
			}
			if !nonEmptyStringField(entryObj, "title") || !nonEmptyStringField(entryObj, "markdown") {
				return errRange
			}
		}
	}
	return nil
}

// validateSkippedElectronUpdateVersions ports the partialRecord(release
// channel, nonEmptyString) — keys restricted to stable/preview, values
// trimmed non-empty.
func validateSkippedElectronUpdateVersions(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	for channel, value := range obj {
		if !releaseChannelSet[channel] {
			return errKeys
		}
		if !nonEmptyString(value) {
			return errRange
		}
	}
	return nil
}

func validateEndpointOrigin(v any) error {
	if v == nil {
		return nil
	}
	if _, ok := normalizeEndpointOriginValue(v); !ok {
		return errType
	}
	return nil
}

func validatePetSettings(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	if err := validateBool(obj["enabled"]); err != nil {
		return err
	}
	// petId: string | null
	if petID, present := obj["petId"]; present {
		if petID != nil && !isString(petID) {
			return errType
		}
	}
	if anchor, ok := obj["anchor"]; ok {
		s, _ := anchor.(string)
		switch s {
		case "bottom-right", "bottom-left", "top-right", "top-left":
		default:
			return errEnum
		}
	}
	if pos, ok := obj["windowPosition"]; ok {
		posObj, ok := pos.(map[string]any)
		if !ok {
			return errType
		}
		for _, key := range []string{"x", "y"} {
			if _, err := intField(posObj, key); err != nil {
				return err
			}
		}
	}
	if displayID, ok := obj["windowDisplayId"]; ok {
		if _, err := toInt(displayID); err != nil {
			return err
		}
	}
	if zone, ok := obj["windowSnapZone"]; ok {
		s, _ := zone.(string)
		switch s {
		case "top-left", "top-center", "top-right", "bottom-left", "bottom-center", "bottom-right":
		default:
			return errEnum
		}
	}
	if size, ok := obj["size"]; ok {
		n, err := toInt(size)
		if err != nil || n < 80 || n > 224 {
			return errRange
		}
	}
	if visibility, ok := obj["visibility"]; ok {
		s, _ := visibility.(string)
		if s != "always" && s != "on-demand" {
			return errEnum
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func trimSpace(s string) string { return strings.TrimSpace(s) }

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func asArray(v any) []any {
	arr, _ := v.([]any)
	return arr
}

// parseURLOrigin ports `new URL(s).origin` for http/https. Returns false for
// other schemes (the TS schema rejects them via preprocess → undefined).
func parseURLOrigin(s string) (string, bool) {
	parsed, err := url.Parse(s)
	if err != nil {
		return "", false
	}
	switch parsed.Scheme {
	case "http", "https":
		origin := parsed.Scheme + "://" + parsed.Host
		return origin, true
	default:
		return "", false
	}
}
