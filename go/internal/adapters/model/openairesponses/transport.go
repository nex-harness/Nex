// Package openairesponses implements the net/http transport and the SSE event
// decoder for the OpenAI Responses API (`POST {baseURL}/responses`), the dialect
// the production provider `ai-proxy.tylinux.cn/v1` is configured with
// (`api.type = "openai-responses"`).
//
// Division of labour (D14, and the spike's R1/R4):
//
//   - internal/modelclient/sse.go stays the SSE framing layer. The vendor SDK's
//     stream reader has failure modes we refuse to ship: R1, it aborts the moment
//     gjson reports a top-level "error" key, and gjson reports an explicit
//     `"error": null` as present — which every successful Responses payload
//     carries. R4, it switches on the SSE `event:` header against an allow-list,
//     so a proxy that forwards only `data:` lines loses every event. Events are
//     framed by modelclient.SSEFramer and handed to the decoder one body at a time.
//   - github.com/openai/openai-go is used for what it is genuinely good at: the
//     request body (responses.ResponseNewParams with our 20 raw tool schemas) and
//     typed per-event decoding into responses.ResponseStreamEventUnion.
//   - The HTTP round trip goes through a plain *http.Client wired exactly like the
//     chat adapter's (proxy env, TLS config, timeouts, header set), so the
//     outbound bytes are identical across the two dialects. Retries stay with
//     modelclient.Client; the SDK is constructed with WithMaxRetries(0) where it
//     is used at all, so it can never retry underneath ours.
package openairesponses

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/model/openai"
	"github.com/tylinux/nex/go/internal/modelclient"
)

const maxErrorBodyBytes = 64_000

// Transport is the net/http implementation of modelclient.Transport for the
// Responses API. Like the chat adapter, it is the only place in the model path
// that imports net/http; everything else lives in internal/modelclient.
type Transport struct {
	client *http.Client
	// headers is the resolved static header set (per run), built by the chat
	// adapter's builder so both dialects send the same attribution headers.
	headers map[string]string
	// url is the absolute `{baseURL}/responses` endpoint, kept as a field so
	// retries reuse it.
	url string
}

// Config configures the transport.
type Config struct {
	// BaseURL is the provider base, e.g. "https://ai-proxy.tylinux.cn/v1". The
	// transport appends "/responses".
	BaseURL string
	// Headers is the per-run header input. Authorization comes from here (the
	// caller supplies the full "Bearer <key>" value), so no credential is stored
	// twice.
	Headers openai.HeaderInput
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
		Proxy:               http.ProxyFromEnvironment,
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
	headers := openai.BuildHeaders(cfg.Headers)
	// The Responses endpoint must be told the body is an event stream.
	headers["accept"] = "text/event-stream"
	return &Transport{
		client:  &http.Client{Transport: transport, Timeout: cfg.Timeout},
		headers: headers,
		url:     ResponsesURL(cfg.BaseURL),
	}
}

// ResponsesURL appends the Responses path to a provider base URL. A trailing
// slash is trimmed first so "…/v1" and "…/v1/" both yield "…/v1/responses" — the
// same path the vendor SDK produces from its own base URL.
func ResponsesURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/responses"
}

// URL returns the absolute endpoint this transport posts to.
func (t *Transport) URL() string { return t.url }

// Do implements modelclient.Transport.
func (t *Transport) Do(ctx context.Context, req modelclient.Request) (*modelclient.Response, error) {
	url := req.URL
	if url == "" {
		url = t.url
	}
	if len(bytes.TrimSpace(req.Body)) == 0 {
		return nil, modelclient.NewAttrError(modelclient.ErrorAttrs{
			Code:    modelclient.CodeInvalidModelRequest,
			Name:    "TypeError",
			Message: "OpenAI Responses request has an empty body; build it with BuildBody.",
		})
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req.Body))
	if err != nil {
		return nil, modelclient.NewAttrError(modelclient.ErrorAttrs{Message: err.Error(), Cause: err})
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

func parseProxy(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return url.Parse(raw)
}

// bodyStream adapts an http response body to modelclient.ResponseStream.
type bodyStream struct{ body io.ReadCloser }

func (b *bodyStream) Read(p []byte) (int, error) { return b.body.Read(p) }
func (b *bodyStream) Close() error               { return b.body.Close() }

// classifyNetError maps a net/http round-trip error to an AttrError the
// classifier understands, preserving the low-level code (ECONNRESET, timeout,
// TLS, ...) so a mid-body disconnect classifies as a retryable stream failure.
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
	attrs := modelclient.ErrorAttrs{Message: err.Error(), Cause: err}
	if code := errorCode(err); code != "" {
		attrs.Code = code
	}
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

// headerMap lowercases response header names so the classifier can look them up.
func headerMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		out[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	return out
}
