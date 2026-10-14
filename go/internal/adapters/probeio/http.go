// http.go (in this package probeio): the fetch-equivalent HTTP client. The TS probeIntranet uses global fetch with default TLS and
// redirect behavior; this adapter is the Go equivalent so the system service
// package stays free of net/http (arch rule: external I/O lives in
// internal/adapters).
package probeio

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
)

// HTTPDoer performs one HTTP request. It is the injection seam for tests and
// this adapter; service packages stay free of net/http.
type HTTPDoer interface {
	Do(ctx context.Context, method, url string, header map[string]string) (HTTPResponse, error)
}

// HTTPResponse is the response subset the intranet probe reads.
type HTTPResponse struct {
	StatusCode int
	Body       []byte
}

// doer is the concrete HTTPDoer.
type doer struct {
	client *http.Client
}

// DefaultDoer returns the fetch-equivalent client: redirects followed,
// default TLS settings, no global timeout (the probe applies its own
// per-request deadline through the context).
func DefaultDoer() HTTPDoer {
	return &doer{client: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{}}}}
}

// Do performs one request and returns the status and the (fully read) body.
func (d *doer) Do(ctx context.Context, method, url string, header map[string]string) (HTTPResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return HTTPResponse{}, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return HTTPResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return HTTPResponse{}, err
	}
	return HTTPResponse{StatusCode: resp.StatusCode, Body: body}, nil
}
