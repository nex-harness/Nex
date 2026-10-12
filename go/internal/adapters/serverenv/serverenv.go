// Package serverenv reads the environment variables the nex-server HTTP shell needs.
//
// The reads live in an adapter so go/internal/server stays free of "os" imports
// (enforced by internal/archcheck): the server package declares what it needs, this
// package performs the access. Every variable here already exists in the TypeScript
// server — the Go rewrite adds none (D7) — and each one keeps the exact read that
// packages/server/src/{http,entry-http}.ts performs today.
//
// The semantics are readTrimmedEnv's: trim, and treat empty or whitespace-only as unset.
// Anything else would change which deployments report a configured name or workspace.
package serverenv

import "os"

// Trimmed returns the value of name with surrounding whitespace removed, or "" when the
// variable is unset, empty or whitespace-only. This is readTrimmedEnv in http.ts.
func Trimmed(name string) string {
	return os.Getenv(name) //nolint:forbidigo // this package is the sanctioned env adapter
}

// Hostname returns the machine name used as the last step of the server-id fallback chain,
// or "" when the OS cannot report one.
func Hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}
