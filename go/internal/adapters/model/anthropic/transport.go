package anthropic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/tylinux/nex/go/internal/adapters/model/openai"
	"github.com/tylinux/nex/go/internal/modelclient"
)

// Config configures the Anthropic transport.
type Config struct {
	// BaseURL is the provider origin, WITHOUT a trailing /v1. The SDK appends the
	// literal path "v1/messages" to it, so passing ".../v1" here would request
	// "/v1/v1/messages" and fail as a silent 404 (spike R4 §4). Unlike the OpenAI
	// adapter, which builds its own URL from a path-suffixed base, this value is
	// the bare origin.
	BaseURL string
	// APIKey becomes the x-api-key header.
	APIKey string
	// Headers is the shared attribution/attribution header input. It reuses the
	// OpenAI adapter's HeaderInput and BuildHeaders so both dialects send the same
	// x-nex-* / x-request-id / user-agent set; there is no second implementation
	// of that policy.
	Headers openai.HeaderInput
	// BetaHeaders are anthropic-beta feature flags. The SDK manages none of them,
	// so they are set here. Empty means "use BetaHeadersFor(nil)".
	BetaHeaders []string
	// Params is the Messages request. It is copied per call, so a Transport can be
	// reused across attempts with a different body.
	Params sdk.MessageNewParams
	// Timeout is the whole-request timeout; 0 means no client-level timeout.
	Timeout time.Duration
	// InsecureSkipVerify disables TLS verification (test-only).
	InsecureSkipVerify bool
	// CAFile is a PEM bundle to trust in addition to the system roots (corporate
	// proxy interception).
	CAFile string
	// Proxy is an explicit proxy URL, empty for environment defaults.
	Proxy string
}

// Transport implements modelclient.Transport for the Anthropic Messages API.
//
// It builds the request with anthropic-sdk-go (MessageNewParams, headers, error
// decoding) and hands back the response body untouched, so the SSE framing and
// event dialect stay in internal/modelclient and this package's Decoder.
type Transport struct {
	client sdk.Client
	// params is the Messages request body. stream=true is applied per request as
	// an option, because MessageNewParams has no field for it.
	params sdk.MessageNewParams
}

// New builds a Transport. It returns an error when the client cannot be
// constructed (bad base URL, unreadable CA bundle).
func New(cfg Config) (*Transport, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("anthropic: base url is required")
	}
	httpClient, err := newHTTPClient(cfg)
	if err != nil {
		return nil, err
	}

	opts := []option.RequestOption{
		option.WithHTTPClient(httpClient),
		option.WithBaseURL(cfg.BaseURL),
		// Retry is owned by internal/modelclient (RetryAttemptLoopContinues +
		// calculateRetryDelay). The SDK's own loop would retry underneath ours and
		// double-count attempts against the budget (spike R4 §4).
		option.WithMaxRetries(0),
		// The Messages endpoint authenticates with x-api-key, not Authorization.
		option.WithHeader("anthropic-version", AnthropicVersion),
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithHeader("x-api-key", cfg.APIKey))
	}
	for name, value := range openai.BuildHeaders(cfg.Headers) {
		opts = append(opts, option.WithHeader(name, value))
	}
	if beta := betaValue(cfg.BetaHeaders); beta != "" {
		opts = append(opts, option.WithHeader("anthropic-beta", beta))
	}

	return &Transport{
		client: sdk.NewClient(opts...),
		params: cfg.Params,
	}, nil
}

func betaValue(in []string) string { return strings.Join(BetaHeadersFor(in), ",") }

// newHTTPClient mirrors the net/http wiring in adapters/model/openai: same dialer
// timeouts, same keep-alive pool, same explicit-proxy handling, plus an optional
// extra CA bundle for an intercepting proxy.
func newHTTPClient(cfg Config) (*http.Client, error) {
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
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("anthropic: read ca file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("anthropic: no certificates in ca file %q", cfg.CAFile)
		}
		// Merge with any InsecureSkipVerify already set.
		tlsConfig := transport.TLSClientConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		} else {
			tlsConfig = tlsConfig.Clone()
		}
		tlsConfig.RootCAs = pool
		transport.TLSClientConfig = tlsConfig
	}
	if cfg.Proxy != "" {
		u, err := parseProxy(cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("anthropic: proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: transport, Timeout: cfg.Timeout}, nil
}

// parseProxy accepts "host:port" as well as a full URL, matching the OpenAI
// adapter's helper.
func parseProxy(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return url.Parse(raw)
}

// Do implements modelclient.Transport. It POSTs the SDK-built body to
// {BaseURL}/v1/messages and returns the body as a modelclient stream.
//
// The SDK owns request construction and error plumbing: a non-2xx response is
// decoded into its apierror.Error (status, headers, raw JSON body) and mapped
// here onto the classifier's input shape. A 2xx response body is handed back
// unread, because the SSE framing belongs to internal/modelclient (spike R4).
func (t *Transport) Do(ctx context.Context, req modelclient.Request) (*modelclient.Response, error) {
	body, err := BuildRequestBody(t.params)
	if err != nil {
		return nil, modelclient.NewAttrError(modelclient.ErrorAttrs{
			Message: "Failed to encode Anthropic request body.",
			Cause:   err,
		})
	}

	// The Messages API streams only when the body says so, and MessageNewParams has
	// no field for it: NewStreaming injects exactly this JSON override. We do not
	// call NewStreaming (see above), so we apply the identical override here.
	var rawResponse *http.Response
	opts := []option.RequestOption{
		option.WithResponseBodyInto(&rawResponse),
		option.WithJSONSet("stream", true),
	}
	for name, value := range req.Headers {
		opts = append(opts, option.WithHeader(name, value))
	}

	// Messages.New is the non-streaming entry point, but with WithResponseBodyInto
	// the body is never deserialized: on 2xx it is handed back unread. Using it
	// instead of NewStreaming is deliberate — NewStreaming hands the body to the
	// SDK's ssestream decoder, whose Stream.Next switches on the SSE `event:`
	// header against a hardcoded allow-list with no default branch and silently
	// drops anything else (spike R4).
	_, err = t.client.Messages.New(ctx, t.params, opts...)
	if err != nil {
		return t.responseFromError(body, err)
	}
	if rawResponse == nil {
		return nil, modelclient.NewAttrError(modelclient.ErrorAttrs{
			Message: "Anthropic response carried no body.",
		})
	}
	return &modelclient.Response{
		StatusCode:  rawResponse.StatusCode,
		StatusText:  rawResponse.Status,
		Headers:     headerMap(rawResponse.Header),
		Stream:      &bodyStream{body: rawResponse.Body},
		RequestBody: body,
	}, nil
}

// responseFromError turns a non-2xx SDK error into the modelclient.Response the
// retry loop classifies. The SDK already read the body, so it is taken from the
// apierror's raw JSON rather than re-read from a closed stream.
func (t *Transport) responseFromError(body []byte, err error) (*modelclient.Response, error) {
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		return &modelclient.Response{
			StatusCode:  apiErr.Response.StatusCode,
			StatusText:  apiErr.Response.Status,
			Headers:     headerMap(apiErr.Response.Header),
			BodyText:    apiErr.RawJSON(),
			RequestBody: body,
		}, nil
	}
	// No response at all: a connection, timeout or cancellation error.
	return nil, t.classifyError(err)
}

// Params returns the request params this transport sends.
func (t *Transport) Params() sdk.MessageNewParams { return t.params }

// bodyStream adapts an http response body to modelclient.ResponseStream.
type bodyStream struct {
	body io.ReadCloser
}

func (b *bodyStream) Read(p []byte) (int, error) { return b.body.Read(p) }
func (b *bodyStream) Close() error               { return b.body.Close() }

// classifyError maps an SDK error onto the modelclient classifier's input shape.
func (t *Transport) classifyError(err error) error {
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
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		return modelclient.NewAttrError(modelclient.ErrorAttrs{
			Name:            string(apiErr.Type()),
			Message:         apiErr.Error(),
			ResponseHeaders: headerMap(apiErr.Response.Header),
			StatusCode:      statusPtr(apiErr.Response.StatusCode),
			Cause:           err,
		})
	}
	attrs := modelclient.ErrorAttrs{Message: err.Error(), Cause: err}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		attrs.Code = "ETIMEDOUT"
		attrs.Name = "TimeoutError"
	}
	return modelclient.NewAttrError(attrs)
}

func statusPtr(code int) *int { return &code }

func headerMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		out[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	return out
}
