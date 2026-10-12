// Package openai implements the net/http transport for the OpenAI-compatible
// chat-completions model client. It is the only place in the model path that
// imports net/http; the pure client logic (parsing, retry, classification) lives
// in internal/modelclient behind modelclient.Transport.
package openai

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tylinux/nex/go/internal/modelclient"
)

// HeaderInput are the per-run values that flow into the outbound headers. They
// mirror createCliNexSourceHeaders + the attribution headers; values that vary
// per run (ids, platform, version) are inputs, not constants.
type HeaderInput struct {
	// Authorization is the full "Bearer <key>" value.
	Authorization string
	// HTTPReferer is resolveRuntimeNexEndpointOrigin(env).
	HTTPReferer string
	// UserAgent is the base user agent, e.g. "Nex/1.2.3"; the adapter appends the
	// ai-sdk/provider-utils suffix it sends.
	UserAgent string
	// RuntimeVersion is the Node major in the user-agent runtime suffix (default 24).
	RuntimeVersion string
	// AppVersion is X-Nex-App-Version (omitted when empty).
	AppVersion string
	// Title is X-Title, e.g. "Z Code@electron".
	Title string
	// ReleaseChannel is X-Release-Channel ("production" or "test").
	ReleaseChannel string
	// ClientLanguage is X-Client-Language.
	ClientLanguage string
	// ClientTimezone is X-Client-Timezone.
	ClientTimezone string
	// NexAgent is X-Nex-Agent ("glm").
	NexAgent string
	// Platform is X-Platform, e.g. "linux-x64" (omitted when empty).
	Platform string
	// OSCategory is X-Os-Category ("linux"/"macos"/"windows").
	OSCategory string
	// OSVersion is X-Os-Version (omitted when empty).
	OSVersion string

	// Attribution headers (createModelRequestAttributionHeaders).
	RequestID       string
	TraceID         string
	SessionType     string // main | subagent | other
	QueryID         string // header value, internal prefix already stripped
	SessionID       string // header value, internal prefix already stripped
	OpenCodeSession string

	// Extra are provider-configured static headers (merged last, lower priority
	// than attribution; the caller decides precedence before calling).
	Extra map[string]string
}

// Transport is the net/http implementation of modelclient.Transport.
type Transport struct {
	client *http.Client
	// headers is the resolved static header set (per run).
	headers map[string]string
	// url is the absolute chat-completions URL the request body's own URL comes
	// from; kept as a field so retries reuse it.
	url string
}

// Config configures the transport.
type Config struct {
	// URL is the absolute chat-completions endpoint.
	URL string
	// Headers is the per-run header input.
	Headers HeaderInput
	// Timeout is the whole-request timeout; 0 means no client-level timeout (the
	// client's idle timeout governs stalls).
	Timeout time.Duration
	// InsecureSkipVerify disables TLS verification (test-only).
	InsecureSkipVerify bool
	// Proxy is an explicit proxy URL, empty for environment defaults.
	Proxy string
}

// New builds a Transport.
func New(cfg Config) *Transport {
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 30 * time.Second,
	}
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	if cfg.Proxy != "" {
		if u, err := parseProxy(cfg.Proxy); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &Transport{
		client:  &http.Client{Transport: transport, Timeout: cfg.Timeout},
		headers: BuildHeaders(cfg.Headers),
		url:     cfg.URL,
	}
}

// Do implements modelclient.Transport.
func (t *Transport) Do(ctx context.Context, req modelclient.Request) (*modelclient.Response, error) {
	url := req.URL
	if url == "" {
		url = t.url
	}
	body := strings.NewReader(string(req.Body))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, modelclient.NewAttrError(modelclient.ErrorAttrs{
			Message: err.Error(),
			Cause:   err,
		})
	}
	for name, value := range t.headers {
		httpReq.Header.Set(name, value)
	}
	for name, value := range req.Headers {
		httpReq.Header.Set(name, value)
	}
	httpReq.Header.Set("content-type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, classifyNetError(err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
		return &modelclient.Response{
			StatusCode:  resp.StatusCode,
			StatusText:  resp.Status,
			Headers:     headerMap(resp.Header),
			BodyText:    string(text),
			RequestBody: req.Body,
		}, nil
	}

	return &modelclient.Response{
		StatusCode:  resp.StatusCode,
		StatusText:  resp.Status,
		Headers:     headerMap(resp.Header),
		Stream:      &bodyStream{body: resp.Body},
		RequestBody: req.Body,
	}, nil
}

const maxErrorBodyBytes = 64_000

// bodyStream adapts an http response body to modelclient.ResponseStream.
type bodyStream struct {
	body io.ReadCloser
}

func (b *bodyStream) Read(p []byte) (int, error) { return b.body.Read(p) }
func (b *bodyStream) Close() error               { return b.body.Close() }

// classifyNetError maps a net/http round-trip error to an AttrError the classifier
// understands, preserving the low-level code (ECONNRESET, timeout, TLS, ...).
func classifyNetError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return modelclient.NewAttrError(modelclient.ErrorAttrs{
			Name:    "AbortError",
			Message: "Model request was cancelled.",
			Cause:   err,
		})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return modelclient.NewAttrError(modelclient.ErrorAttrs{
			Code:    "ETIMEDOUT",
			Name:    "TimeoutError",
			Message: "Model request timed out.",
			Cause:   err,
		})
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return modelclient.NewAttrError(modelclient.ErrorAttrs{
			Code:    "ETIMEDOUT",
			Name:    "TimeoutError",
			Message: err.Error(),
			Cause:   err,
		})
	}
	code := errorCode(err)
	attrs := modelclient.ErrorAttrs{Message: err.Error(), Cause: err}
	if code != "" {
		attrs.Code = code
	}
	// A connection reset / broken pipe during a response body read is what the
	// dropAfterChunks fixtures produce.
	if opErr, ok := err.(*net.OpError); ok {
		attrs.Code = netOpCode(opErr)
	}
	return modelclient.NewAttrError(attrs)
}

func netOpCode(opErr *net.OpError) string {
	if opErr.Err == nil {
		return ""
	}
	msg := opErr.Err.Error()
	switch {
	case strings.Contains(msg, "connection reset"):
		return "ECONNRESET"
	case strings.Contains(msg, "broken pipe"):
		return "EPIPE"
	case strings.Contains(msg, "connection refused"):
		return "ECONNREFUSED"
	}
	return ""
}

// errorCode extracts an errno-style code via a Code() method when available.
func errorCode(err error) string {
	type coder interface{ Code() string }
	var c coder
	if errors.As(err, &c) {
		return c.Code()
	}
	return ""
}

func headerMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		out[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	return out
}
