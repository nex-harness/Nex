package channels

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// The channel-name list is duplicated from packages/shared/src/channels.ts on purpose (see the
// package doc), which means it CAN drift. This test is the drift alarm: it re-reads the
// TypeScript source and fails when the two disagree.
//
// Drift is not cosmetic. The gateway uses this list to reject a routing-table entry naming a
// channel that does not exist; a channel added to Node and missing here would be refused by a
// table that tries to route it, and an operator would have no way to move it to Go at all.

// repoRoot walks up to the directory holding pnpm-workspace.yaml.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "pnpm-workspace.yaml")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("pnpm-workspace.yaml not found above %s", dir)
	return ""
}

// TestChannelListMatchesTheTypeScriptSource re-reads ServiceChannels and requires the Go list to
// be exactly the same set.
func TestChannelListMatchesTheTypeScriptSource(t *testing.T) {
	source := filepath.Join(repoRoot(t), "packages", "shared", "src", "channels.ts")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Skipf("read %s: %v", source, err)
	}
	declared := parseServiceChannels(t, string(contents))
	if len(declared) == 0 {
		t.Fatalf("no channels parsed out of %s: the extractor needs updating", source)
	}

	var missing, extra []string
	for name := range declared {
		if !Known(name) {
			missing = append(missing, name)
		}
	}
	for _, name := range Names {
		if _, ok := declared[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("channels in %s but missing from the Go list: %v\n"+
			"add them to Names — a routing table naming one of these would be rejected at startup",
			source, missing)
	}
	if len(extra) > 0 {
		t.Errorf("channels in the Go list but no longer in %s: %v\n"+
			"remove them — a routing table naming one of these would be accepted and then route to "+
			"a channel nothing serves", source, extra)
	}
}

// serviceChannelsEntry locates the ServiceChannels object literal in the source.
var serviceChannelsEntry = regexp.MustCompile(`(?s)export const ServiceChannels = \{(.*?)\} as const`)

// channelValue matches `Key: "value",` inside that object.
var channelValue = regexp.MustCompile(`(?m)^\s*[A-Za-z0-9_]+:\s*"([a-z0-9-]+)",`)

// parseServiceChannels extracts the channel names from the ServiceChannels literal.
func parseServiceChannels(t *testing.T, source string) map[string]struct{} {
	t.Helper()
	block := serviceChannelsEntry.FindStringSubmatch(source)
	if len(block) < 2 {
		t.Fatalf("ServiceChannels not found in channels.ts: the extractor needs updating")
	}
	out := map[string]struct{}{}
	for _, match := range channelValue.FindAllStringSubmatch(block[1], -1) {
		out[match[1]] = struct{}{}
	}
	return out
}

// TestKnownRejectsTypos is the behavior the list exists for: a name that is not a service channel
// must not be treated as one.
func TestKnownRejectsTypos(t *testing.T) {
	for _, name := range []string{"", "fil", "File", "nex-task ", "nope"} {
		if Known(name) {
			t.Errorf("Known(%q) = true, want false", name)
		}
	}
	if !Known("file") || !Known("nex-agent") || !Known("window-controller") {
		t.Error("a real channel name was reported as unknown")
	}
}

// TestUnknownPreservesOrderAndFilters: the router's error message lists what it found, in the
// order the table named it, so the message is stable and reviewable.
func TestUnknownPreservesOrderAndFilters(t *testing.T) {
	got := Unknown([]string{"file", "fil", "git", "terminal2", "  ", "git"})
	want := []string{"fil", "terminal2", "  "}
	if len(got) != len(want) {
		t.Fatalf("Unknown = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Unknown[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
