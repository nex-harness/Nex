package runners

import (
	"strings"
	"unicode"
)

// IdentityPrefix starts every server-built workspace identity. The format is
// `runner:<runnerId>:<runner-local absolute path>` (docs/next/multi-runner.md).
const IdentityPrefix = "runner:"

// MachineScopePath is the pseudo path of the machine-level browse scope
// (`runner:<runnerId>:~`, docs/next/runner-folders.md decision 2). It is a
// routing identity only: the runner accepts it for readdir/stat/info and for
// runner-workspaces, and it is never a catalogued workspace.
const MachineScopePath = "~"

// BuildIdentity returns the server-owned identity for one runner-local path.
// The server builds identities from runner hello frames; runners never parse
// them for IO.
func BuildIdentity(runnerID, path string) string {
	return IdentityPrefix + runnerID + ":" + path
}

// ParseIdentity splits `runner:<runnerId>:<path>` into its parts.
//
// Only the first two ':' separators are structural: runner-local paths may
// contain ':' themselves (Windows `C:\Users\me`, UNC shares, POSIX names with
// a colon). The identity is invalid when the prefix, the runner id or the path
// is missing, or when the runner id contains ':' or whitespace.
//
// The machine-level browse scope `runner:<runnerId>:~` parses as well
// (docs/next/runner-folders.md decision 2): the server only routes it, it never
// comes from a runner catalogue.
func ParseIdentity(identity string) (runnerID, path string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(identity), IdentityPrefix)
	if !found {
		return "", "", false
	}
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	runnerID, path = parts[0], parts[1]
	if !ValidRunnerID(runnerID) || path == "" {
		return "", "", false
	}
	return runnerID, path, true
}

// ValidRunnerID reports whether id can be embedded in a workspace identity.
// Runner ids are `runner_<hex>` by default (packages/next-runner/src/config.ts)
// and must not contain ':' (the identity separator) or whitespace.
func ValidRunnerID(id string) bool {
	if id == "" {
		return false
	}
	for _, char := range id {
		if char == ':' || unicode.IsSpace(char) {
			return false
		}
	}
	return true
}
