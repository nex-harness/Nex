// defaults.go: the default values appSettingsSchema.parse({}) produces and the
// field-level sanitizers the TS preprocess chain applies before validation
// (validationAppSettings.ts). Sanitizers only run on READ (whole-file parse);
// patch writes keep strict validation.

package setting

// DefaultEmbeddedBrowserViewport is DEFAULT_EMBEDDED_BROWSER_VIEWPORT_PREFERENCE
// with the TS key order (mode, viewport{width, height}, zoom — JS object
// insertion order is part of the setting.json byte contract).
func DefaultEmbeddedBrowserViewport() *Ordered {
	viewport := NewOrdered()
	viewport.Set("width", int64(393))
	viewport.Set("height", int64(852))
	preference := NewOrdered()
	preference.Set("mode", "normal")
	preference.Set("viewport", viewport)
	preference.Set("zoom", "fit")
	return preference
}

// DefaultSettings returns the value of appSettingsSchema.parse({}), as an
// ordered map matching the schema field order (probed against the real zod).
func DefaultSettings() *Ordered {
	o := NewOrdered()
	set := func(key string, value any) { o.Set(key, value) }
	set("recentProjects", []any{})
	set("locale", "zh-CN")
	set("localePreference", "system")
	set("terminalInheritSystemProfile", true)
	set("embeddedBrowserAllowInsecureCertificates", false)
	set("embeddedBrowserViewportPreference", DefaultEmbeddedBrowserViewport())
	set("computerUseComposerEntryHidden", true)
	set("taskAutoArchiveEnabled", false)
	set("taskAutoArchiveOlderThanDays", int64(7))
	set("closeToTrayOnWindows", true)
	set("showDockIcon", true)
	set("closeToTrayOnWindowsMigrationInitialized", true)
	set("keepAwakeWhileRunning", false)
	set("desktopChromiumHardwareAccelerationEnabled", true)
	set("messageStreamShowReasoning", true)
	set("messageStreamShowReasoningMigrationInitialized", true)
	set("messageStreamShowTodos", false)
	set("toolGroupingExploreEnabled", true)
	set("toolGroupingTerminalEnabled", true)
	set("toolGroupingChangesEnabled", false)
	set("nexInteractionBehavior", "queue")
	set("askUserQuestionAutoResolutionEnabled", true)
	set("modelIoFullRetentionEnabled", false)
	set("startPlanRecommendationDismissed", false)
	set("providerFamilyConnectionSelections", map[string]any{})
	set("providerFamilyDomainMigrated", false)
	set("nativeSearchEnhancementsEnabled", true)
	set("memoryEnabled", false)
	set("toolSearchEnabled", false)
	set("codemodeEnabled", false)
	set("lastWorkspaceSession", []any{})
	set("lastActiveTabIndex", int64(0))
	set("receivePreviewUpdates", false)
	set("autoDownloadAndInstallUpdates", false)
	set("skippedElectronUpdateVersions", map[string]any{})
	return o
}

// SanitizeNexEndpointOrigin ports sanitizeNexEndpointOrigin: on read, a bad
// override value drops ONLY that field rather than failing the whole file.
// A valid value is normalized (normalized origin, e.g. trailing slash removed).
func SanitizeNexEndpointOrigin(raw map[string]any) {
	value, present := raw["nexEndpointOrigin"]
	if !present {
		return
	}
	if normalized, ok := normalizeEndpointOriginValue(value); ok {
		raw["nexEndpointOrigin"] = normalized
		return
	}
	delete(raw, "nexEndpointOrigin")
}

// normalizeEndpointOriginValue ports the nexEndpointOriginSchema preprocess:
// non-string / empty / unparseable → undefined; otherwise URL origin.
func normalizeEndpointOriginValue(value any) (string, bool) {
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	trimmed := trimSpace(s)
	if trimmed == "" {
		return "", false
	}
	origin, ok := parseURLOrigin(trimmed)
	if !ok {
		return "", false
	}
	return origin, true
}

// SanitizeDesktopWindowSize ports sanitizeDesktopWindowSize: a bad
// desktopWindowSize field is dropped on read (defaults apply), not fatal.
func SanitizeDesktopWindowSize(raw map[string]any) {
	value, present := raw["desktopWindowSize"]
	if !present {
		return
	}
	if validateDesktopWindowSize(value) == nil {
		return
	}
	delete(raw, "desktopWindowSize")
}

func validateDesktopWindowSize(value any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return errType
	}
	width, err := intField(obj, "width")
	if err != nil {
		return err
	}
	height, err := intField(obj, "height")
	if err != nil {
		return err
	}
	if _, ok := obj["maximized"].(bool); !ok {
		return errType
	}
	if width < 480 || height < 640 {
		return errRange
	}
	return nil
}

// SanitizeEmbeddedBrowserViewportPreference ports the same-named TS
// sanitizer: single-field drop on read.
func SanitizeEmbeddedBrowserViewportPreference(raw map[string]any) {
	value, present := raw["embeddedBrowserViewportPreference"]
	if !present {
		return
	}
	if ValidateEmbeddedBrowserViewportPreference(value) == nil {
		return
	}
	delete(raw, "embeddedBrowserViewportPreference")
}

// ValidateEmbeddedBrowserViewportPreference ports
// embeddedBrowserViewportPreferenceSchema (.strict()).
func ValidateEmbeddedBrowserViewportPreference(value any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return errType
	}
	if len(obj) != 3 {
		return errKeys
	}
	mode, _ := obj["mode"].(string)
	if mode != "normal" && mode != "responsive" {
		return errEnum
	}
	viewport, ok := obj["viewport"].(map[string]any)
	if !ok || len(viewport) != 2 {
		return errType
	}
	width, err := intField(viewport, "width")
	if err != nil {
		return err
	}
	height, err := intField(viewport, "height")
	if err != nil {
		return err
	}
	if width < 320 || width > 3840 || height < 320 || height > 2160 {
		return errRange
	}
	zoom, _ := obj["zoom"].(string)
	switch zoom {
	case "fit", "50", "75", "100", "125", "150", "200":
	default:
		return errEnum
	}
	return nil
}

// MigrateCloseToTrayOnWindowsDefault ports
// migrateCloseToTrayOnWindowsDefault: one-time flip to true with the marker.
func MigrateCloseToTrayOnWindowsDefault(raw map[string]any) {
	if initialized, ok := raw["closeToTrayOnWindowsMigrationInitialized"].(bool); ok && initialized {
		return
	}
	raw["closeToTrayOnWindows"] = true
	raw["closeToTrayOnWindowsMigrationInitialized"] = true
}

// MigrateMessageStreamShowReasoningDefault ports
// migrateMessageStreamShowReasoningDefault.
func MigrateMessageStreamShowReasoningDefault(raw map[string]any) {
	if initialized, ok := raw["messageStreamShowReasoningMigrationInitialized"].(bool); ok && initialized {
		return
	}
	raw["messageStreamShowReasoning"] = true
	raw["messageStreamShowReasoningMigrationInitialized"] = true
}

// MigrateLegacyLocalePreference ports migrateLegacyLocalePreference: when only
// `locale` exists (no localePreference) and it is valid, keep it as the
// explicit preference.
func MigrateLegacyLocalePreference(raw map[string]any) {
	if _, hasPreference := raw["localePreference"]; hasPreference {
		return
	}
	locale, hasLocale := raw["locale"]
	if !hasLocale {
		return
	}
	if s, ok := locale.(string); ok && (s == "zh-CN" || s == "en-US") {
		raw["localePreference"] = s
	}
}
