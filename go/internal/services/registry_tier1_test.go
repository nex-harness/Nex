package services

// registry_tier1_test.go (step 3.2): the Tier-1 channels register into the
// step-3.1 Registry under the ServiceChannels names, and a gateway-routes
// table that flips them to "go" parses and routes them to the Go handlers.
// The PRODUCTION default routes stay "node" — per-tier cutover is step f of
// the recipe; this test only proves the switch works when it happens.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/gateway"
	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/services/file"
	"github.com/tylinux/nex/go/internal/services/filewatcher"
	"github.com/tylinux/nex/go/internal/services/mediapreview"
	"github.com/tylinux/nex/go/internal/services/system"
)

type fakeServer struct {
	channels map[string]rpc.Channel
}

func (f *fakeServer) RegisterChannel(name string, channel rpc.Channel) {
	f.channels[name] = channel
}

// TestRegistryTier1Registration: the four Tier-1 services register under
// their ServiceChannels names and are exposed on a connection's server.
func TestRegistryTier1Registration(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(file.ChannelName, &file.Channel{Service: file.NewService(nil)})
	registry.MustRegister(system.ChannelName, &system.Channel{Service: system.NewService(system.Options{})})
	registry.MustRegister(filewatcher.ChannelName, &filewatcher.Channel{Service: filewatcher.NewService()})
	registry.MustRegister(mediapreview.ChannelName, &mediapreview.Channel{Service: mediapreview.NewService(mediapreview.Options{
		FileService: file.NewService(nil),
	})})

	server := &fakeServer{channels: map[string]rpc.Channel{}}
	dispose := registry.ExposeOnChannelServer(server, nil)
	defer dispose()

	for _, name := range []string{"file", "system", "file-watcher", "media-preview"} {
		if server.channels[name] == nil {
			t.Errorf("channel %q not exposed", name)
		}
	}
}

// TestGatewayRoutesTier1GoSwitch proves the gateway routing table accepts a
// test file flipping the four Tier-1 channels to "go" — the same file an
// operator writes for the per-tier cutover. It does NOT change any default.
func TestGatewayRoutesTier1GoSwitch(t *testing.T) {
	// The committed tier-1 cutover fixture — the exact file an operator drops
	// into the config dir at cutover time (kept in-tree so shape drift is
	// caught by this test).
	routesPath := filepath.Join("..", "..", "testdata", "services", "gateway", "gateway-routes.tier1-go.json")
	if _, err := os.Stat(routesPath); err != nil {
		t.Fatalf("tier-1 routes fixture missing: %v", err)
	}

	router, err := gateway.LoadRouter(routesPath)
	if err != nil {
		t.Fatalf("routes file with the tier-1 go switch must load: %v", err)
	}
	for _, name := range []string{"file", "system", "file-watcher", "media-preview"} {
		if !router.ChannelOwnedByGo(name) {
			t.Errorf("channel %q not routed to go", name)
		}
	}
	// untouched channels stay node (the production default)
	if router.ChannelOwnedByGo("git") {
		t.Errorf("git must stay node")
	}
}
