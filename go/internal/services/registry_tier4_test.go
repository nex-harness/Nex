// registry_tier4_test.go (step 3.7): the nine knowledge-extension channels
// register into the step-3.1 Registry under their ServiceChannels names.
package services

import (
	"testing"

	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/services/commands"
	"github.com/tylinux/nex/go/internal/services/hooktrust"
	"github.com/tylinux/nex/go/internal/services/mcpsync"
	"github.com/tylinux/nex/go/internal/services/memory"
	"github.com/tylinux/nex/go/internal/services/paths"
	"github.com/tylinux/nex/go/internal/services/pluginmgmt"
	pluginsvc "github.com/tylinux/nex/go/internal/services/plugins"
	"github.com/tylinux/nex/go/internal/services/pluginsync"
	skillsvc "github.com/tylinux/nex/go/internal/services/skills"
	"github.com/tylinux/nex/go/internal/services/skillsync"
)

// TestRegistryTier4Registration proves every step-3.7 channel is exposed on
// a channel server under its ServiceChannels name.
func TestRegistryTier4Registration(t *testing.T) {
	registry := NewRegistry()
	home := t.TempDir()
	pathsInstance := paths.New("", home)

	skillsService := skillsvc.New(skillsvc.Options{HomeDir: home, IsDesktopRuntime: true})
	registry.MustRegister(skillsvc.ChannelName, &skillsvc.Channel{Service: skillsService})
	registry.MustRegister(skillsync.ChannelName, &skillsync.Channel{Service: skillsync.New(skillsync.Options{HomeDir: home})})
	registry.MustRegister(commands.ChannelName, &commands.Channel{Service: commands.New(commands.Options{HomeDir: home})})
	registry.MustRegister(memory.ChannelName, &memory.Channel{Service: memory.New(memory.Options{Paths: pathsInstance})})
	registry.MustRegister(hooktrust.ChannelName, &hooktrust.Channel{Service: hooktrust.New(hooktrust.Options{HomeDir: home})})
	registry.MustRegister(pluginsvc.ChannelName, &pluginsvc.Channel{Service: pluginsvc.New()})
	registry.MustRegister(pluginmgmt.ChannelName, &pluginmgmt.Channel{Service: pluginmgmt.New(pluginmgmt.Options{HomeDir: home})})
	registry.MustRegister(pluginsync.ChannelName, &pluginsync.Channel{Service: pluginsync.New(pluginsync.Options{HomeDir: home})})
	registry.MustRegister(mcpsync.ChannelName, &mcpsync.Channel{Service: mcpsync.New(mcpsync.Options{HomeDir: home})})

	server := &fakeServer{channels: map[string]rpc.Channel{}}
	dispose := registry.ExposeOnChannelServer(server, nil)
	defer dispose()

	for _, name := range []string{
		"skills", "skill-sync", "commands", "memory", "hooks",
		"plugins", "plugin-management", "plugin-sync", "mcp-sync",
	} {
		if server.channels[name] == nil {
			t.Errorf("channel %q not exposed", name)
		}
	}
}

// TestTier4ChannelNames pins the ServiceChannels contract names.
func TestTier4ChannelNames(t *testing.T) {
	cases := []struct{ got, want string }{
		{skillsvc.ChannelName, "skills"},
		{skillsync.ChannelName, "skill-sync"},
		{commands.ChannelName, "commands"},
		{memory.ChannelName, "memory"},
		{hooktrust.ChannelName, "hooks"},
		{pluginsvc.ChannelName, "plugins"},
		{pluginmgmt.ChannelName, "plugin-management"},
		{pluginsync.ChannelName, "plugin-sync"},
		{mcpsync.ChannelName, "mcp-sync"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("channel name contract: got %q want %q", tc.got, tc.want)
		}
	}
}

// TestTier4ErrorContractStrings pins the error strings the UI branches on.
func TestTier4ErrorContractStrings(t *testing.T) {
	cases := []struct{ got, want string }{
		{pluginsvc.RetiredError, "Legacy plugin management has been retired in Nex Agent mode"},
		{mcpsync.StatusesUnavailableError, "MCP server status listing is unavailable in this runtime"},
		{memory.PreviewLimitExceededErrorCode, "PROJECT_MEMORY_PREVIEW_LIMIT_EXCEEDED"},
		{memory.FileChangedErrorCode, "PROJECT_MEMORY_FILE_CHANGED"},
		{skillsvc.CapabilityDesktopOnly, "desktop_only"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("error contract: got %q want %q", tc.got, tc.want)
		}
	}
}
