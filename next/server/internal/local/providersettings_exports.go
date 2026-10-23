package local

// Channel constructors and the view fan-out shared by the server-owned config
// channels (split out of providersettings.go for the 400-line file budget).

import (
	"encoding/base64"
	"sort"

	"github.com/tylinux/nex-next/server/internal/configstore"
)

func randomSuffix() string {
	var raw [4]byte
	for index := range raw {
		raw[index] = byte(timeNowNano() >> (8 * index))
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])[:6]
}

// timeNowNano is a seam for tests.
var timeNowNano = func() int64 {
	return timeNow().UnixNano()
}

// SortedProviderIDs is exported for tests/diagnostics.
func SortedProviderIDs(providers map[string]*configstore.Provider) []string {
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// NewProviderSettingsChannel is the app-facing constructor.
func NewProviderSettingsChannel(manager *Manager) Channel {
	return newProviderSettings(manager)
}

// NewModelSelectionChannel is the app-facing constructor.
func NewModelSelectionChannel(manager *Manager) Channel {
	return newModelSelection(newProviderSettings(manager))
}

// NewConfigChannel is the app-facing constructor.
func NewConfigChannel(manager *Manager) Channel {
	return newConfigChannel(manager)
}

// PublishConfigViews re-broadcasts provider-settings and model-selection
// onDidChange after a doc change or a fact update.
func PublishConfigViews() { viewBroadcasts.publish() }
