// Package channels is the gateway's copy of the service channel name set.
//
// The routing table must reject a name that is not a real channel (typo protection: a table that
// silently ignored "fil" would leave that channel on Node forever while the operator believed it
// had moved). That check needs the authoritative list, which lives in the TypeScript
// ServiceChannels object (packages/shared/src/channels.ts) and in docs/specs/go-rewrite/inventory.md.
//
// It is duplicated here rather than generated because the Go tree has no codegen step for this and
// because a check against a stale copy fails SAFE in the right direction: a channel added to Node
// after this list was written is rejected by the table until the list is updated, which is an
// operator-visible error at startup, not a silent misroute. The test TestChannelListMatchesTheTypeScriptSource
// re-reads the TypeScript source and fails when the two drift.
package channels

import "strings"

// Names is every service channel the server registers on a connection. It is ServiceChannels
// (packages/shared/src/channels.ts) plus the three channels http.ts overrides per connection —
// INexAgentService, IProviderProvisioningTargetService and IWindowControllerService — which are
// already in ServiceChannels but are called out in the plan because their per-connection behaviour
// is what forces a dedicated upstream per client connection.
var Names = []string{
	"bots",
	"broadcast",
	"client-config",
	"client-scenes",
	"coding-plan-subscription",
	"commands",
	"conversation-share",
	"credential",
	"cua-permission",
	"cua-pip-session",
	"feedback",
	"file",
	"file-watcher",
	"git",
	"git-checkpoint",
	"hooks",
	"mcp-sync",
	"media-preview",
	"memory",
	"model-selection",
	"nex-agent",
	"nex-session",
	"nex-task",
	"oauth",
	"off-peak-task",
	"onboarding-record",
	"pets",
	"plugin-management",
	"plugin-sync",
	"plugins",
	"prompt-attachment-transfer",
	"provider-provisioning-target",
	"provider-settings",
	"setting",
	"settings-sync",
	"skill-sync",
	"skills",
	"subagents",
	"system",
	"terminal",
	"usage-stats",
	"window-controller",
}

// lookup is the set form of Names, built once.
var lookup = func() map[string]struct{} {
	out := make(map[string]struct{}, len(Names))
	for _, name := range Names {
		out[name] = struct{}{}
	}
	return out
}()

// Known reports whether name is a real service channel.
func Known(name string) bool {
	_, ok := lookup[name]
	return ok
}

// Unknown returns the subset of candidates that are not real channels, in the order given.
func Unknown(candidates []string) []string {
	var out []string
	for _, name := range candidates {
		if strings.TrimSpace(name) == "" || !Known(name) {
			out = append(out, name)
		}
	}
	return out
}
