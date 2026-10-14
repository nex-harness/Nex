package services

// registry_tier3_test.go (step 3.5): the settings-family channels register
// into the step-3.1 Registry under their ServiceChannels names. The
// production gateway-routes default stays "node" — G3.settings runs the
// package race suites; routing flips are step f of the recipe.

import (
	"testing"

	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/services/clientconfig"
	"github.com/tylinux/nex/go/internal/services/clientscenes"
	"github.com/tylinux/nex/go/internal/services/codingplansubscription"
	"github.com/tylinux/nex/go/internal/services/credential"
	"github.com/tylinux/nex/go/internal/services/onboarding"
	"github.com/tylinux/nex/go/internal/services/paths"
	"github.com/tylinux/nex/go/internal/services/setting"
	"github.com/tylinux/nex/go/internal/services/settingssync"
)

// TestRegistryTier3Registration: the eight step-3.5 services register under
// their channel names and are exposed on a connection's server.
func TestRegistryTier3Registration(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(setting.ChannelName, &setting.Channel{Service: setting.NewService(setting.ServiceOptions{})})
	registry.MustRegister(settingssync.ChannelName, &settingssync.Channel{Service: settingssync.NewService(settingssync.Options{})})
	registry.MustRegister(clientconfig.ChannelName, &clientconfig.Channel{Service: clientconfig.NewService(clientconfig.Options{})})
	registry.MustRegister(clientscenes.ChannelName, &clientscenes.Channel{Service: clientscenes.NewService(clientscenes.Options{})})
	registry.MustRegister(onboarding.ChannelName, &onboarding.Channel{Service: onboarding.NewService(onboarding.Options{})})
	registry.MustRegister(credential.ChannelName, &credential.Channel{Service: credential.NewService(credential.Options{})})
	registry.MustRegister(codingplansubscription.ChannelName, &codingplansubscription.Channel{Service: codingplansubscription.NewService(codingplansubscription.Options{})})

	server := &fakeServer{channels: map[string]rpc.Channel{}}
	dispose := registry.ExposeOnChannelServer(server, nil)
	defer dispose()

	// device has NO channel (ensureDeviceMid is a host entry-point function
	// consumed by onboarding/telemetry, not an RPC surface) — assert the
	// seven channel-backed services plus that fact.
	for _, name := range []string{
		"setting", "settings-sync", "client-config", "client-scenes",
		"onboarding-record", "credential", "coding-plan-subscription",
	} {
		if server.channels[name] == nil {
			t.Errorf("channel %q not exposed", name)
		}
	}
}

// TestTier3ErrorContractStrings pins the error strings the UI branches on
// (the recipe's error-contract rule).
func TestTier3ErrorContractStrings(t *testing.T) {
	cases := []struct{ got, want string }{
		{paths.DataBaseDirForbiddenWindowsInstallDirErrorCode, "DATA_BASE_DIR_FORBIDDEN_WINDOWS_INSTALL_DIR"},
		{credential.DecryptErrorCode, "NEX_CREDENTIAL_DECRYPT_FAILED"},
		{credential.DecryptErrorPrefix, "凭据解密失败："},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("contract string = %q, want %q", tc.got, tc.want)
		}
	}
}
