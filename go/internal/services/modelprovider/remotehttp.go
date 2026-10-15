// remotehttp.go: the HTTP client seam for the remote model list. The channel
// layer stays free of net/http (archcheck: I/O belongs in adapters); this
// file is the adapter-style seam the service package owns for its own
// outbound calls (models.dev fetches through a *http.Client directly in
// modelsdev.go — same tier as the TS which uses fetch() in the service).
package modelprovider

import (
	"net/http"
	"time"
)

type remoteHTTPRequest = http.Request

func newRemoteHTTPClient() *http.Client {
	return &http.Client{Timeout: remoteModelListTimeout}
}

var _ = time.Second
