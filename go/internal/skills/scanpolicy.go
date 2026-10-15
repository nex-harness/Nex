// Package skills is the single shared implementation of skill discovery
// parsing (step 3.7's "single implementation rule"): the frontmatter reader,
// the bounded scan policy, the SKILL.md walk, the root resolution, and the
// skill-sync tar.gz archive codec. Both the server services
// (internal/services/skills, skillsync, commands) and the Phase 4 agent
// loading consume this package; nothing skill-shaped may be re-implemented
// beside it.
//
// Sources: packages/shared/src/skill-scan-policy.ts (policy),
// packages/services/src/skills/skillDiscoveryWalk.ts (walk),
// packages/services/src/skills/skillsService.ts (frontmatter),
// packages/services/src/skill-sync/skillSync{Path,Archive}.ts (archive).
package skills

import "strings"

// Scan policy constants (skill-scan-policy.ts).
const (
	// FileName is the skill definition file name (SKILL_FILE_NAME).
	FileName = "SKILL.md"
	// MaxScanDepth is the recursive scan depth relative to the scan root
	// (MAX_SKILL_SCAN_DEPTH) — the backstop brake for symlink/junction loops.
	MaxScanDepth = 8
	// MaxDescriptionLength caps the description (aligned with
	// apps/nex-cli/packages/adapters/src/skills/index.ts:19).
	MaxDescriptionLength = 1024
)

// scanExcludedDirectoryNames ports SKILL_SCAN_EXCLUDED_DIRECTORY_NAMES:
// content directories that would inflate a single scan to minutes.
var scanExcludedDirectoryNames = map[string]bool{
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"out":          true,
	"target":       true,
	"vendor":       true,
	"coverage":     true,
	".cache":       true,
	".next":        true,
	".turbo":       true,
	".venv":        true,
	"__pycache__":  true,
}

// discoveryDotDirAllowlist ports SKILL_DISCOVERY_DOT_DIR_ALLOWLIST.
var discoveryDotDirAllowlist = map[string]bool{".system": true}

// ShouldWalkDirectoryEntry ports shouldWalkSkillDirectoryEntry: whether the
// recursive scan enters a child directory.
func ShouldWalkDirectoryEntry(entryName string) bool {
	if scanExcludedDirectoryNames[entryName] {
		return false
	}
	if !strings.HasPrefix(entryName, ".") {
		return true
	}
	return discoveryDotDirAllowlist[entryName]
}
