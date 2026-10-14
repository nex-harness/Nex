package services

// registry_tier1b_test.go (step 3.3): the git and git-checkpoint channels
// register into the step-3.1 Registry under their ServiceChannels names. The
// production gateway-routes default stays "node" for both — G3.git /
// G3.gitcheckpoint run the package race suites; routing flips are step f of
// the recipe and not part of this step.

import (
	"testing"

	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/services/git"
	"github.com/tylinux/nex/go/internal/services/gitcheckpoint"
)

// TestRegistryTier1bRegistration: both step-3.3 services register under their
// channel names and are exposed on a connection's server.
func TestRegistryTier1bRegistration(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(git.ChannelName, &git.Channel{Service: git.NewService(nil, nil)})
	registry.MustRegister(gitcheckpoint.ChannelName, &gitcheckpoint.Channel{Service: gitcheckpoint.NewService(gitcheckpoint.ServiceOptions{})})

	server := &fakeServer{channels: map[string]rpc.Channel{}}
	dispose := registry.ExposeOnChannelServer(server, nil)
	defer dispose()

	for _, name := range []string{"git", "git-checkpoint"} {
		if server.channels[name] == nil {
			t.Errorf("channel %q not exposed", name)
		}
	}
}
