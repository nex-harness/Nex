// validate.go: the Go port of appSettingsSchema / appSettingsPatchSchema
// (validationAppSettings.ts). The zod behaviour was probed at the boundaries
// that matter for the wire contract: trims (nonEmptyStringSchema), integer
// ranges, enum sets, discriminated unions, partialRecord key sets, unknown-key
// stripping (z.object is non-strict — parse DROPS unknown keys), nullish
// optionals, and the preprocess chain order (workspace session → endpoint
// sanitize → locale preference → tray/reasoning defaults → window-size
// sanitize → viewport sanitize).

package setting

import (
	"errors"
	"fmt"
)

var (
	errType  = errors.New("invalid_type")
	errEnum  = errors.New("invalid_enum_value")
	errRange = errors.New("too_small")
	errKeys  = errors.New("unrecognized_keys")
)

// occupationSet is appSettingsOccupationSchema's enum.
var occupationSet = map[string]bool{
	"office": true, "developer": true, "independent": true, "infrastructure": true,
	"product": true, "design": true, "student": true, "creator": true,
	"operations": true, "marketing": true, "finance": true, "accounting": true,
	"legal": true, "other": true,
}

var releaseChannelSet = map[string]bool{"stable": true, "preview": true}

// remoteResourcePackageSet is isKnownRemoteResourcePackageId's set.
var remoteResourcePackageSet = map[string]bool{
	"server-bundle": true, "node-runtime": true, "node-pty": true, "glm": true,
	"bfs": true, "ripgrep": true, "ugrep": true,
}

// ValidateSettings runs the preprocess chain then the object schema on a
// decoded JSON value. It returns the SANITIZED+MIGRATED raw map (with
// defaults NOT applied — ApplyDefaults does that) or an error.
func ValidateSettings(value any) (map[string]any, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		if value == nil {
			raw = map[string]any{}
		} else {
			return nil, fmt.Errorf("settings: expected object, got %T", value)
		}
	}
	sanitized := cloneMap(raw)
	// Preprocess chain, innermost first (TS nesting order).
	MigrateLegacyWorkspaceSession(sanitized)
	sanitized = MigrateLegacyAccountConnectionSettings(sanitized)
	SanitizeNexEndpointOrigin(sanitized)
	MigrateLegacyLocalePreference(sanitized)
	MigrateCloseToTrayOnWindowsDefault(sanitized)
	MigrateMessageStreamShowReasoningDefault(sanitized)
	SanitizeDesktopWindowSize(sanitized)
	SanitizeEmbeddedBrowserViewportPreference(sanitized)
	if err := validateSettingsObject(sanitized); err != nil {
		return nil, err
	}
	return sanitized, nil
}

// validateSettingsObject ports appSettingsObjectSchema. Extra keys are
// stripped by the caller via NormalizeSettings (z.object non-strict drops
// them); this function only CHECKS the known fields.
func validateSettingsObject(raw map[string]any) error {
	for _, entry := range settingsValidators {
		value, present := raw[entry.key]
		if !present || value == nil {
			// Fields with defaults and required fields are handled by
			// NormalizeSettings; optionals accept null per .nullish().
			if entry.optional || entry.nullish {
				continue
			}
			continue
		}
		if err := entry.validate(value); err != nil {
			return fmt.Errorf("settings %s: %w", entry.key, err)
		}
	}
	return nil
}

type fieldValidator struct {
	key      string
	validate func(any) error
	optional bool
	nullish  bool
}

// settingsValidators ports each member of appSettingsObjectSchema. All
// members with defaults are effectively required after parse; validation
// treats absence as OK (defaults fill in).
var settingsValidators = []fieldValidator{
	{"recentProjects", validateStringArray, false, false},
	{"locale", validateLocale, false, false},
	{"shortcutBindings", validateShortcutBindings, true, false},
	{"localePreference", validateLocalePreference, false, false},
	{"terminalInheritSystemProfile", validateBool, false, false},
	{"terminalFontFamily", validateNonEmptyTrimmedString, true, false},
	{"integratedTerminalShell", validateIntegratedTerminalShell, true, false},
	{"httpProxy", validateNonEmptyTrimmedString, true, false},
	{"httpProxyNoProxy", validateNonEmptyTrimmedString, true, false},
	{"httpProxyCaCertPath", validateNonEmptyTrimmedString, true, false},
	{"embeddedBrowserAllowInsecureCertificates", validateBool, false, false},
	{"embeddedBrowserViewportPreference", ValidateEmbeddedBrowserViewportPreference, false, false},
	{"computerUseComposerEntryHidden", validateBool, false, false},
	{"taskAutoArchiveEnabled", validateBool, false, false},
	{"taskAutoArchiveOlderThanDays", intRangeValidator(1, 365), false, false},
	{"closeToTrayOnWindows", validateBool, false, false},
	{"showDockIcon", validateBool, false, false},
	{"closeToTrayOnWindowsMigrationInitialized", validateBool, false, false},
	{"keepAwakeWhileRunning", validateBool, false, false},
	{"desktopZoomLevel", intRangeValidator(-3, 5), true, false},
	{"desktopWindowSize", validateDesktopWindowSize, true, false},
	{"desktopChromiumHardwareAccelerationEnabled", validateBool, false, false},
	{"messageStreamShowReasoning", validateBool, false, false},
	{"messageStreamShowReasoningMigrationInitialized", validateBool, false, false},
	{"messageStreamShowTodos", validateBool, false, false},
	{"toolGroupingExploreEnabled", validateBool, false, false},
	{"toolGroupingTerminalEnabled", validateBool, false, false},
	{"toolGroupingChangesEnabled", validateBool, false, false},
	{"nexInteractionBehavior", enumValidator("queue", "guide"), false, false},
	{"askUserQuestionAutoResolutionEnabled", validateBool, false, false},
	{"modelIoFullRetentionEnabled", validateBool, false, false},
	{"startPlanRecommendationDismissed", validateBool, false, false},
	{"providerFamilyConnectionSelections", validateProviderFamilyConnectionSelections, false, false},
	{"providerFamilyDomain", enumValidator("zai", "bigmodel"), true, false},
	{"providerFamilyDomainUpdatedAt", nonNegativeInt, true, false},
	{"providerFamilyDomainMigrated", validateBool, false, false},
	{"nativeSearchEnhancementsEnabled", validateBool, false, false},
	{"onboardingOccupation", validateOccupation, true, true},
	{"proactiveSuggestionsEnabled", validateBool, true, false},
	{"memoryEnabled", validateBool, false, false},
	{"toolSearchEnabled", validateBool, false, false},
	{"codemodeEnabled", validateBool, false, false},
	{"lastWorkspaceSession", validateWorkspaceSessionArray, false, false},
	{"lastActiveTabIndex", nonNegativeInt, false, false},
	{"lastActiveTaskByWorkspace", validateStringRecord, true, false},
	{"dataBaseDir", validateNonEmptyTrimmedString, true, false},
	{"pendingPostUpdateReleaseNotes", validatePostUpdateReleaseNotes, true, false},
	{"receivePreviewUpdates", validateBool, false, false},
	{"autoDownloadAndInstallUpdates", validateBool, false, false},
	{"skippedElectronUpdateVersions", validateSkippedElectronUpdateVersions, false, false},
	{"settingsSyncFirstRunPromptHandled", validateBool, true, false},
	{"nexEndpointOrigin", validateEndpointOrigin, true, false},
	{"pet", validatePetSettings, true, false},
}

// ValidatePatch ports appSettingsPatchSchema: same members, all optional, with
// providerFamilyDomain additionally allowing "" (clearing the domain), and
// onboardingOccupation nullish.
func ValidatePatch(patch map[string]any) error {
	for _, entry := range settingsValidators {
		value, present := patch[entry.key]
		if !present || value == nil {
			continue
		}
		if entry.key == "providerFamilyDomain" {
			if s, ok := value.(string); ok && s == "" {
				continue
			}
		}
		if err := entry.validate(value); err != nil {
			return fmt.Errorf("settings patch %s: %w", entry.key, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// field validators
// ---------------------------------------------------------------------------
func intField(m map[string]any, key string) (int64, error) {
	value, ok := m[key]
	if !ok {
		return 0, errType
	}
	return toInt(value)
}

// toInt accepts the JSON number shapes Go decodes (float64) and the internal
// int64 spelling, rejecting non-integers (z.number().int()).
func toInt(v any) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case float64:
		if t != float64(int64(t)) {
			return 0, errType
		}
		return int64(t), nil
	default:
		return 0, errType
	}
}

func intRangeValidator(min, max int64) func(any) error {
	return func(v any) error {
		n, err := toInt(v)
		if err != nil {
			return err
		}
		if n < min || n > max {
			return errRange
		}
		return nil
	}
}

func nonNegativeInt(v any) error {
	n, err := toInt(v)
	if err != nil {
		return err
	}
	if n < 0 {
		return errRange
	}
	return nil
}

func validateIntegratedTerminalShell(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	mode, _ := obj["mode"].(string)
	switch mode {
	case "auto":
		if len(obj) != 1 {
			return errKeys
		}
		return nil
	case "shell":
		dialect, _ := obj["dialect"].(string)
		if dialect != "cmd" && dialect != "git-bash" {
			return errEnum
		}
		for _, key := range []string{"id", "label", "path"} {
			if !nonEmptyStringField(obj, key) {
				return errRange
			}
		}
		if len(obj) != 4 {
			return errKeys
		}
		return nil
	default:
		return errEnum
	}
}

// validateProviderFamilyConnectionSelections ports
// providerFamilyConnectionSelectionSettingsSchema: only zai/bigmodel keys,
// each a strict discriminated union on kind.
func validateProviderFamilyConnectionSelections(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	for family, selection := range obj {
		if family != "zai" && family != "bigmodel" {
			return errKeys
		}
		if err := validateProviderFamilyConnectionSelection(selection); err != nil {
			return err
		}
	}
	return nil
}

func validateProviderFamilyConnectionSelection(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	kind, _ := obj["kind"].(string)
	switch kind {
	case "start-plan", "individual-coding-plan":
		if len(obj) != 1 {
			return errKeys
		}
		return nil
	case "team-coding-plan":
		for _, key := range []string{"productId", "organizationId", "projectId"} {
			if !nonEmptyStringField(obj, key) {
				return errRange
			}
		}
		if len(obj) != 4 {
			return errKeys
		}
		return nil
	default:
		return errEnum
	}
}
