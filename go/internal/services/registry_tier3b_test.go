// registry_tier3b_test.go (step 3.6): the providers-and-models channels
// register into the step-3.1 Registry under their ServiceChannels names; the
// connection-scoped provider-provisioning-target flows through a
// providers.ConnectionScope (the desktop-continuous/web-remote gate), never a
// global registration.
package services

import (
	"testing"

	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/services/modelprovider"
	"github.com/tylinux/nex/go/internal/services/providers"
)

// TestRegistryTier3bRegistration: the two channel-backed step-3.6 services
// register globally; the provisioning target does NOT (it is one of the three
// connection-scoped channels the registry refuses globally).
func TestRegistryTier3bRegistration(t *testing.T) {
	registry := NewRegistry()

	// provider-settings / model-selection need a live runtime; build one on a
	// temp dir through the modelprovider test helper equivalents.
	runtime, err := modelprovider.NewProviderRuntime(modelprovider.RuntimeOptions{
		PersonalOptions: modelprovider.PersonalRepoOptions{
			FilePath: t.TempDir() + "/provider_config.json",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	registry.MustRegister(modelprovider.ChannelProviderSettings, &modelprovider.SettingsChannel{Service: runtime.ProviderSettings})
	registry.MustRegister(modelprovider.ChannelModelSelection, &modelprovider.SelectionChannel{Service: runtime.ModelSelection})

	server := &fakeServer{channels: map[string]rpc.Channel{}}
	dispose := registry.ExposeOnChannelServer(server, nil)
	defer dispose()

	for _, name := range []string{"provider-settings", "model-selection"} {
		if server.channels[name] == nil {
			t.Errorf("channel %q not exposed", name)
		}
	}

	// The connection-scoped channel: a global registration is refused.
	if err := registry.Register(ChannelProviderProvisioningTarget, &providers.Channel{}); err == nil {
		t.Errorf("provider-provisioning-target global registration must be refused")
	}

	// Through a scope: web-remote gets the rejecting stub, desktop-continuous
	// the real target.
	target := providers.NewTarget(providers.TargetOptions{Runtime: runtime})
	scopedServer := &fakeServer{channels: map[string]rpc.Channel{}}
	disposeScoped := registry.ExposeOnChannelServer(scopedServer, providers.NewConnectionScope("web-remote-replayable", target))
	defer disposeScoped()
	if scopedServer.channels[ChannelProviderProvisioningTarget] == nil {
		t.Fatalf("web-remote connection must see the (rejecting) provisioning channel")
	}
	// The stub rejects with the exact error.
	_, err = scopedServer.channels[ChannelProviderProvisioningTarget].Call(nil, "apply", []rpc.Value{nil})
	if err == nil || err.Error() != providers.ProvisioningOnlyTrustedHostError {
		t.Fatalf("web-remote apply must be rejected with the exact error, got %v", err)
	}
}

// TestTier3bErrorContractStrings pins the security-sensitive and gate error
// strings (the recipe's error-contract rule).
func TestTier3bErrorContractStrings(t *testing.T) {
	cases := []struct{ got, want string }{
		{providers.ProvisioningOnlyTrustedHostError, "Provider Provisioning 仅支持受信 Desktop Host"},
		{modelprovider.ChannelProviderSettings, "provider-settings"},
		{modelprovider.ChannelModelSelection, "model-selection"},
		{providers.ChannelProviderProvisioningTarget, "provider-provisioning-target"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("error contract: got %q want %q", tc.got, tc.want)
		}
	}
}
