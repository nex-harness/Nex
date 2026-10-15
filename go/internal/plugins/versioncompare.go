// versioncompare.go: plugin version comparison, porting
// adapters/src/plugins/version-compare.ts. semver.valid/coerce/gt are
// re-implemented here (no new dependency rule): coerce takes the first
// dot-separated numeric run, valid requires three numeric parts.
package plugins

import (
	"strconv"
	"strings"
)

// PluginUpdateStatus classifies an installed version against the latest.
type PluginUpdateStatus string

const (
	UpdateNone           PluginUpdateStatus = "none"
	UpdateAvailable      PluginUpdateStatus = "update-available"
	UpdateVersionChanged PluginUpdateStatus = "version-changed"
)

// ComparePluginVersions ports comparePluginVersions:
//   - latest > installed (semver)      -> update-available
//   - parseable + not greater           -> none
//   - unparseable + differing           -> version-changed
//   - unparseable + equal, or missing   -> none
func ComparePluginVersions(installed, latest string) PluginUpdateStatus {
	if installed == "" || latest == "" {
		return UpdateNone
	}
	installedValid, installedOK := semverCoerceValid(installed)
	latestValid, latestOK := semverCoerceValid(latest)
	if installedOK && latestOK {
		if semverGT(latestValid, installedValid) {
			return UpdateAvailable
		}
		return UpdateNone
	}
	if installed == latest {
		return UpdateNone
	}
	return UpdateVersionChanged
}

// ComparePluginUpdate ports comparePluginUpdate: prefer the version axis;
// fall back to the source identity pin; never report an update that cannot
// be proven newer.
func ComparePluginUpdate(installedVersion, installedSha, latestVersion, latestSha string) PluginUpdateStatus {
	if latestVersion != "" {
		return ComparePluginVersions(installedVersion, latestVersion)
	}
	if latestSha != "" {
		if installedSha == "" {
			return UpdateVersionChanged
		}
		if installedSha == latestSha {
			return UpdateNone
		}
		return UpdateAvailable
	}
	return UpdateNone
}

type semver struct{ major, minor, patch int }

// semverCoerceValid applies semver.coerce then semver.valid: coerce takes the
// first numeric run of at least one digit as major, optionally followed by
// `.minor` and `.patch` numeric parts. Anything that still lacks three parts
// after coercion is invalid (semver.valid of a coerced value is nil then).
func semverCoerceValid(value string) (semver, bool) {
	coerced, ok := semverCoerce(value)
	if !ok {
		return semver{}, false
	}
	return coerced, true
}

func semverCoerce(value string) (semver, bool) {
	// Strip a leading 'v'/'V' the way semver.coerce's regex allows.
	start := 0
	if len(value) > 0 && (value[0] == 'v' || value[0] == 'V') {
		start = 1
	}
	// Take the first dot-separated numeric run (major[.minor[.patch]]).
	digits, rest := takeNumericRun(value[start:])
	if digits == "" {
		return semver{}, false
	}
	major, _ := strconv.Atoi(digits)
	out := semver{major: major}
	rest = strings.TrimPrefix(rest, ".")
	digits, rest = takeNumericRun(rest)
	if digits != "" {
		out.minor, _ = strconv.Atoi(digits)
		rest = strings.TrimPrefix(rest, ".")
		digits, _ = takeNumericRun(rest)
		if digits != "" {
			out.patch, _ = strconv.Atoi(digits)
		}
	}
	return out, true
}

func takeNumericRun(value string) (digits, rest string) {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	return value[:end], value[end:]
}

func semverGT(left, right semver) bool {
	if left.major != right.major {
		return left.major > right.major
	}
	if left.minor != right.minor {
		return left.minor > right.minor
	}
	return left.patch > right.patch
}
