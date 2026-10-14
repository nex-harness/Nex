package setting

// validate_test.go: parity with the real zod schemas. The expected values were
// probed from packages/shared (appSettingsSchema.safeParse) — see the step
// notes; the differential harness re-proves them against the live Node code.

import (
	"encoding/json"
	"testing"
)

func TestDefaultSettingsJSON(t *testing.T) {
	// JSON.stringify(appSettingsSchema.parse({})) bytes (probed). The compact
	// comparison is structural: Go maps iterate randomly, so nested objects
	// go through the canonicalizer (key order is covered by
	// TestDefaultSettingsKeyOrder below).
	const expected = `{"recentProjects":[],"locale":"zh-CN","localePreference":"system","terminalInheritSystemProfile":true,"embeddedBrowserAllowInsecureCertificates":false,"embeddedBrowserViewportPreference":{"mode":"normal","viewport":{"width":393,"height":852},"zoom":"fit"},"computerUseComposerEntryHidden":true,"taskAutoArchiveEnabled":false,"taskAutoArchiveOlderThanDays":7,"closeToTrayOnWindows":true,"showDockIcon":true,"closeToTrayOnWindowsMigrationInitialized":true,"keepAwakeWhileRunning":false,"desktopChromiumHardwareAccelerationEnabled":true,"messageStreamShowReasoning":true,"messageStreamShowReasoningMigrationInitialized":true,"messageStreamShowTodos":false,"toolGroupingExploreEnabled":true,"toolGroupingTerminalEnabled":true,"toolGroupingChangesEnabled":false,"nexInteractionBehavior":"queue","askUserQuestionAutoResolutionEnabled":true,"modelIoFullRetentionEnabled":false,"startPlanRecommendationDismissed":false,"providerFamilyConnectionSelections":{},"providerFamilyDomainMigrated":false,"nativeSearchEnhancementsEnabled":true,"memoryEnabled":false,"toolSearchEnabled":false,"codemodeEnabled":false,"lastWorkspaceSession":[],"lastActiveTabIndex":0,"receivePreviewUpdates":false,"autoDownloadAndInstallUpdates":false,"skippedElectronUpdateVersions":{}}`
	if got, err := DefaultSettings().MarshalJSON(); err != nil {
		t.Fatalf("render: %v", err)
	} else if string(got) != expected {
		t.Fatalf("default settings JSON mismatch:\ngot:  %s\nwant: %s", got, expected)
	}
}

// TestDefaultSettingsKeyOrder pins the schema field order of the parse output
// (probed: appSettingsSchema.parse key order, independent of input order).
func TestDefaultSettingsKeyOrder(t *testing.T) {
	got := DefaultSettings().Keys()
	want := []string{
		"recentProjects", "locale", "localePreference", "terminalInheritSystemProfile",
		"embeddedBrowserAllowInsecureCertificates", "embeddedBrowserViewportPreference",
		"computerUseComposerEntryHidden", "taskAutoArchiveEnabled", "taskAutoArchiveOlderThanDays",
		"closeToTrayOnWindows", "showDockIcon", "closeToTrayOnWindowsMigrationInitialized",
		"keepAwakeWhileRunning", "desktopChromiumHardwareAccelerationEnabled",
		"messageStreamShowReasoning", "messageStreamShowReasoningMigrationInitialized",
		"messageStreamShowTodos", "toolGroupingExploreEnabled", "toolGroupingTerminalEnabled",
		"toolGroupingChangesEnabled", "nexInteractionBehavior", "askUserQuestionAutoResolutionEnabled",
		"modelIoFullRetentionEnabled", "startPlanRecommendationDismissed",
		"providerFamilyConnectionSelections", "providerFamilyDomainMigrated",
		"nativeSearchEnhancementsEnabled", "memoryEnabled", "toolSearchEnabled", "codemodeEnabled",
		"lastWorkspaceSession", "lastActiveTabIndex", "receivePreviewUpdates",
		"autoDownloadAndInstallUpdates", "skippedElectronUpdateVersions",
	}
	if len(got) != len(want) {
		t.Fatalf("key count %d != %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestValidateSettingsAcceptReject(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty object", `{}`, false},
		{"full valid", `{"locale":"en-US","recentProjects":["/a"],"dataBaseDir":"/data"}`, false},
		{"bad locale", `{"locale":"xx"}`, true},
		{"float tabIndex", `{"lastActiveTabIndex":1.5}`, true},
		{"archive days over", `{"taskAutoArchiveOlderThanDays":366}`, true},
		{"archive days zero", `{"taskAutoArchiveOlderThanDays":0}`, true},
		{"zoom under", `{"desktopZoomLevel":-4}`, true},
		{"unknown key dropped-not-error", `{"unknownKeyX":1}`, false},
		{"bad domain", `{"providerFamilyDomain":"zhipu"}`, true},
		{"bad selection family", `{"providerFamilyConnectionSelections":{"zhipu":{"kind":"start-plan"}}}`, true},
		{"team selection missing org", `{"providerFamilyConnectionSelections":{"zai":{"kind":"team-coding-plan","productId":"p","projectId":"j"}}}`, true},
		{"bad skipped channel", `{"skippedElectronUpdateVersions":{"beta":"1"}}`, true},
		{"empty skipped value", `{"skippedElectronUpdateVersions":{"stable":""}}`, true},
		{"bad occupation", `{"onboardingOccupation":"astronaut"}`, true},
		{"null occupation", `{"onboardingOccupation":null}`, false},
		{"bad pet size", `{"pet":{"enabled":true,"petId":null,"size":64}}`, true},
		{"valid pet", `{"pet":{"enabled":true,"petId":"cat","anchor":"bottom-left","size":120}}`, false},
		{"wsl user colon", sessionJSON(`{"kind":"wsl","user":"a:b"}`), true},
		{"wsl user empty", sessionJSON(`{"kind":"wsl","user":""}`), false},
		{"wsl user 65", sessionJSON(`{"kind":"wsl","user":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), true},
		{"ssh port over", sessionJSON(`{"kind":"ssh","host":"h","username":"u","port":65536}`), true},
		// NOTE (quirk, probed): zod ACCEPTS an unknown package id inside a LIVE
		// lastWorkspaceSession target — only the remoteWorkspaceHistory
		// migration strips the field. Reproduced verbatim.
		{"unknown remote package (accepted, TS quirk)", sessionJSON(`{"kind":"ssh","host":"h","username":"u","resourcePackages":{"selectedPackageIds":["nope"]}}`), false},
		{"known remote package", sessionJSON(`{"kind":"ssh","host":"h","username":"u","resourcePackages":{"selectedPackageIds":["glm"]}}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.input), &value); err != nil {
				t.Fatal(err)
			}
			_, err := ValidateSettings(value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateSettings err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func sessionJSON(target string) string {
	return `{"lastWorkspaceSession":[{"kind":"remote","workspacePath":"/r","target":` + target + `,"lastOpenedAt":1,"lastConnectionStatus":"connected"}]}`
}

func TestValidateSettingsSanitizers(t *testing.T) {
	// nexEndpointOrigin: bad value dropped on read, good value normalized.
	sanitized, err := ValidateSettings(mustJSON(t, `{"nexEndpointOrigin":"bad url "}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := sanitized["nexEndpointOrigin"]; has {
		t.Fatal("bad endpoint origin should be dropped on read")
	}
	sanitized, err = ValidateSettings(mustJSON(t, `{"nexEndpointOrigin":" http://x.example.com/path "}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := sanitized["nexEndpointOrigin"]; got != "http://x.example.com" {
		t.Fatalf("endpoint origin not normalized: %v", got)
	}

	// desktopWindowSize: bad dropped.
	sanitized, err = ValidateSettings(mustJSON(t, `{"desktopWindowSize":{"width":100,"height":800,"maximized":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := sanitized["desktopWindowSize"]; has {
		t.Fatal("bad window size should be dropped on read")
	}

	// viewport preference: bad dropped.
	sanitized, err = ValidateSettings(mustJSON(t, `{"embeddedBrowserViewportPreference":{"mode":"normal","viewport":{"width":100,"height":800},"zoom":"fit"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := sanitized["embeddedBrowserViewportPreference"]; has {
		t.Fatal("bad viewport preference should be dropped on read")
	}

	// tray migration one-time flip.
	sanitized, err = ValidateSettings(mustJSON(t, `{"closeToTrayOnWindows":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if sanitized["closeToTrayOnWindows"] != true {
		t.Fatalf("uninitialized tray flag should flip to true, got %v", sanitized["closeToTrayOnWindows"])
	}
	sanitized, err = ValidateSettings(mustJSON(t, `{"closeToTrayOnWindows":false,"closeToTrayOnWindowsMigrationInitialized":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if sanitized["closeToTrayOnWindows"] != false {
		t.Fatal("initialized tray flag must stay false")
	}
}

func TestMigrateLegacyWorkspaceSession(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string // JSON of lastWorkspaceSession after NormalizeSettings
	}{
		{
			"lastOpenTabs only",
			`{"lastOpenTabs":["/a","/b"]}`,
			`[{"kind":"local","workspacePath":"/a","workspacePurpose":"project"},{"kind":"local","workspacePath":"/b","workspacePurpose":"project"}]`,
		},
		{
			"historyId resolved from remoteWorkspaceHistory",
			`{"lastWorkspaceSession":[{"kind":"local","workspacePath":"/a"},{"historyId":"h1"}],"remoteWorkspaceHistory":[{"id":"h1","workspacePath":"/r","target":{"kind":"ssh","host":"h","username":"u"},"lastOpenedAt":1,"lastConnectionStatus":"connected"}]}`,
			`[{"kind":"local","workspacePath":"/a","workspacePurpose":"project"}]`,
		},
		{
			"remote entry keeps target, strips resourcePackages",
			`{"lastWorkspaceSession":[{"kind":"remote","workspacePath":"/r","target":{"kind":"ssh","host":"h","username":"u","resourcePackages":{"selectedPackageIds":["glm"]}},"lastOpenedAt":2,"lastConnectionStatus":"connected"}]}`,
			`[{"kind":"remote","workspacePath":"/r","target":{"kind":"ssh","host":"h","username":"u"},"lastOpenedAt":2,"lastConnectionStatus":"connected"}]`,
		},
		{
			"duplicate local paths not re-added",
			`{"lastWorkspaceSession":[{"kind":"local","workspacePath":"/a"}],"lastOpenTabs":["/a","/b"]}`,
			`[{"kind":"local","workspacePath":"/a","workspacePurpose":"project"},{"kind":"local","workspacePath":"/b","workspacePurpose":"project"}]`,
		},
		{
			"workspacePurpose conversation kept",
			`{"lastWorkspaceSession":[{"kind":"local","workspacePath":"/a","workspacePurpose":"conversation"}]}`,
			`[{"kind":"local","workspacePath":"/a","workspacePurpose":"conversation"}]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sanitized, err := ValidateSettings(mustJSON(t, tc.input))
			if err != nil {
				t.Fatal(err)
			}
			normalized := NormalizeSettings(sanitized)
			value, _ := normalized.Get("lastWorkspaceSession")
			if !jsonEqualJSON(t, value, tc.want) {
				t.Fatalf("lastWorkspaceSession:\ngot:  %s\nwant: %s", mustEncode(t, value), tc.want)
			}
			if _, has := sanitized["lastOpenTabs"]; has {
				t.Fatal("lastOpenTabs must be removed")
			}
			if _, has := sanitized["remoteWorkspaceHistory"]; has {
				t.Fatal("remoteWorkspaceHistory must be removed")
			}
		})
	}
}

func TestNormalizePatchParity(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"locale alone adds preference", `{"locale":"en-US"}`, `{"locale":"en-US","localePreference":"en-US"}`},
		{"locale with preference untouched", `{"locale":"en-US","localePreference":"system"}`, `{"locale":"en-US","localePreference":"system"}`},
		{"font trimmed", `{"terminalFontFamily":"  x  "}`, `{"terminalFontFamily":"x"}`},
		{"font empty removed", `{"terminalFontFamily":"   "}`, `{}`},
		{"shell auto removed", `{"integratedTerminalShell":{"mode":"auto"}}`, `{}`},
		{"shell fields trimmed", `{"integratedTerminalShell":{"mode":"shell","dialect":"cmd","id":" a ","label":" b ","path":" c "}}`, `{"integratedTerminalShell":{"mode":"shell","dialect":"cmd","id":"a","label":"b","path":"c"}}`},
		{"proxy trimmed", `{"httpProxy":" p "}`, `{"httpProxy":"p"}`},
		{"proxy empty removed", `{"httpProxy":""}`, `{}`},
		{"noProxy empty removed", `{"httpProxyNoProxy":"  "}`, `{}`},
		{"caCert empty removed", `{"httpProxyCaCertPath":""}`, `{}`},
		{"endpoint empty removed", `{"nexEndpointOrigin":"  "}`, `{}`},
		{"domain empty removed", `{"providerFamilyDomain":" "}`, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var patch map[string]any
			if err := json.Unmarshal([]byte(tc.input), &patch); err != nil {
				t.Fatal(err)
			}
			got := NormalizePatch(patch)
			if !jsonEqualJSON(t, got, tc.want) {
				t.Fatalf("NormalizePatch:\ngot:  %s\nwant: %s", mustEncode(t, got), tc.want)
			}
		})
	}
}

// jsonEqualJSON compares a decoded value against a JSON string structurally
// (Go map iteration order must not affect the comparison).
func jsonEqualJSON(t *testing.T, value any, wantJSON string) bool {
	t.Helper()
	var want any
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(canonicalize(value))
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := json.Marshal(canonicalize(want))
	if err != nil {
		t.Fatal(err)
	}
	return string(got) == string(wantBytes)
}

func canonicalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, item := range t {
			out[k] = canonicalize(item)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = canonicalize(item)
		}
		return out
	default:
		return v
	}
}

func mustEncode(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLegacyAccountConnectionMigration(t *testing.T) {
	legacy := mustJSON(t, `{"modelProviderFamilyModes":{"zai":"team","bigmodel":"apiKey"},"modelProviderFamilySelectedKeys":{"zai":"team-plan:builtin:zai-coding-plan:pid:org:proj","bigmodel":"coding-plan:builtin:bigmodel-start-plan"},"locale":"en-US"}`)
	if !NeedsLegacyAccountConnectionMigration(legacy) {
		t.Fatal("legacy keys should need migration")
	}
	migrated := MigrateLegacyAccountConnectionSettings(legacy)
	selections, ok := migrated["providerFamilyConnectionSelections"].(map[string]any)
	if !ok {
		t.Fatal("selections missing")
	}
	zai, _ := selections["zai"].(map[string]any)
	if zai["kind"] != "team-coding-plan" || zai["organizationId"] != "org" {
		t.Fatalf("zai selection wrong: %v", zai)
	}
	// bigmodel mode is apiKey → skipped by BOTH engines (probed).
	if bigmodel, has := selections["bigmodel"]; has {
		t.Fatalf("bigmodel apiKey mode must be skipped, got %v", bigmodel)
	}

	incomplete := ReadIncompleteLegacyTeamConnections(mustJSON(t, `{"modelProviderFamilyModes":{"zai":"team"},"modelProviderFamilySelectedKeys":{"zai":"team-plan:builtin:zai-coding-plan:pid:proj"}}`))
	if len(incomplete) != 1 || incomplete[0].ProductID != "pid" || incomplete[0].ProjectID != "proj" {
		t.Fatalf("incomplete connections wrong: %+v", incomplete)
	}

	retained := RetainLegacyAccountConnectionFields(legacy)
	if keys := retained.Keys(); len(keys) != 2 || keys[0] != "modelProviderFamilyModes" {
		t.Fatalf("retained fields wrong: %v", keys)
	}
}

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
