// adapter.go is the Go shape of BotProviderAdapter
// (packages/services/src/bots/providers/types.ts) plus the shared
// providerRequest.ts bounded-HTTP client.
//
// Why raw HTTP instead of the vendor SDKs (recorded in decisions.md D3.9):
// the TS itself only uses `@larksuiteoapi/node-sdk` for the Feishu *WebSocket*
// long connection (feishuProvider.ts line 1514 — `await import(...)`); every
// HTTP call in all three providers is hand-written fetch against
// open.feishu.cn / api.telegram.org / ilinkai.weixin.qq.com. Porting the wire
// format is therefore a 1:1 translation with no behavioural drift, while
// adopting github.com/larksuite/oapi-sdk-go/v3 would replace a handful of typed
// POSTs with a 30k-line dependency whose long-connection client is a second,
// differently-versioned protocol surface. Telegram has no long connection at all
// (getUpdates long-poll), so go-telegram-bot-api would add nothing.
//
// The client is an interface so tests point it at httptest servers and never at
// a real provider endpoint.
package bots

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode"
)

// ProviderRequestTimeout is BOT_PROVIDER_REQUEST_TIMEOUT_MS.
const ProviderRequestTimeout = 15 * time.Second

// HTTPDoer is the injected transport seam (the TS `fetch`).
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ProviderResponse is the light result Telegram-style calls need. The TS
// consumes the body before returning (Bugfix: fetch() 在响应头到达时就会完成),
// so the status is the whole point of this call.
type ProviderResponse struct {
	OK     bool
	Status int
}

// JSONResponse is BotProviderJsonResponse.
type JSONResponse struct {
	OK     bool
	Status int
	// Payload is nil when the body was empty or unparseable AND the response was
	// not ok; an ok response with a bad body is an error at the call site.
	Payload any
	// LogID is the `x-tt-logid` header (Telegram).
	LogID string
}

// ProviderClient performs the bounded provider requests. Every call takes a
// context so a bot callback cannot outlive its HTTP request.
type ProviderClient struct {
	// Doer performs the request; nil uses http.DefaultClient.
	Doer HTTPDoer
	// Timeout bounds ONE request (default ProviderRequestTimeout).
	Timeout time.Duration
}

// Fetch ports fetchBotProvider: headers are not the completion, so the body is
// drained under the same deadline before returning.
func (c *ProviderClient) Fetch(ctx context.Context, method, url string, body any) (ProviderResponse, error) {
	resp, err := c.do(ctx, method, url, body)
	if err != nil {
		return ProviderResponse{}, err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return ProviderResponse{}, err
	}
	return ProviderResponse{OK: resp.StatusCode >= 200 && resp.StatusCode < 300, Status: resp.StatusCode}, nil
}

// FetchJSON ports fetchBotProviderJson.
func (c *ProviderClient) FetchJSON(ctx context.Context, method, url string, body any) (JSONResponse, error) {
	resp, err := c.do(ctx, method, url, body)
	if err != nil {
		return JSONResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return JSONResponse{}, err
	}
	result := JSONResponse{
		OK:     resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status: resp.StatusCode,
		LogID:  resp.Header.Get("x-tt-logid"),
	}
	if len(bytes.TrimSpace(data)) > 0 {
		var payload any
		if unmarshalErr := json.Unmarshal(data, &payload); unmarshalErr == nil {
			result.Payload = payload
		} else if result.OK {
			// The TS throws only when the response claimed success.
			return result, fmt.Errorf("bots: provider response is not JSON: %w", unmarshalErr)
		}
	}
	return result, nil
}

func (c *ProviderClient) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = ProviderRequestTimeout
	}
	// 修复原因：收到响应头不代表请求完成。deadline 与消费响应体共用同一个 ctx，
	// 否则服务端 headers 后停滞仍会永久堵住 Bot actor 队列。
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	doer := c.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	return doer.Do(req)
}

// AcknowledgeResult is BotProviderAcknowledgeResult.
type AcknowledgeResult struct {
	Handled bool
}

// StreamingCardBlock is one block of BotStreamingReplyCardBlock.
type StreamingCardBlock struct {
	// Message blocks set Text; tools blocks set Summaries.
	Text      string
	Summaries []string
	Title     string
	Expanded  bool
	IsTools   bool
}

// StreamingCardState is BotStreamingReplyCardState.
type StreamingCardState struct {
	ProviderUserID string
	Locale         string
	Blocks         []StreamingCardBlock
	Status         string // running | sealed | completed | error
}

// CardHandle is BotStreamingReplyCardHandle.
type CardHandle struct {
	ProviderMessageID string
}

// TypingTarget is BotTypingTarget.
type TypingTarget struct {
	ProviderUserID       string
	ProviderMessageID    string
	ProviderContextToken string
}

// DownloadedAttachment is BotProviderDownloadedAttachment.
type DownloadedAttachment struct {
	Attachment Attachment
	Data       []byte
}

// CallbackResponse is the provider's own HTTP answer for a callback, the
// equivalent of BotProviderAdapter.handleCallbackResponse's Pick.
type CallbackResponse struct {
	ResponseBody any
	Status       int
}

// ProviderAdapter is BotProviderAdapter. Methods the adapter does not implement
// are nil in Go (interfaces cannot be partially implemented per method), so the
// optional surface is expressed by the Capability check on the concrete type
// instead: a nil field here means "not supported by this provider", exactly like
// the TS's optional methods.
type ProviderAdapter interface {
	// Test is adapter.test.
	Test(ctx context.Context, bot Config) (TestResult, error)
	// Send is adapter.send.
	Send(ctx context.Context, bot Config, message OutboundMessage) error
	// ParseCallback is adapter.parseCallback.
	ParseCallback(payload map[string]any) []InboundMessage
	// The rest are optional; a provider that does not support them returns
	// ErrUnsupportedCapability (and the callback checks Has* before calling).
	ResolveName(ctx context.Context, bot Config) (string, error)
	SyncCommands(ctx context.Context, bot Config) error
	SendTyping(ctx context.Context, bot Config, target TypingTarget) error
	StopTyping(ctx context.Context, bot Config, target TypingTarget) error
	AcknowledgeCallback(ctx context.Context, bot Config, payload map[string]any, text string, message *OutboundMessage) (AcknowledgeResult, error)
	PrepareCallbackPayload(ctx context.Context, bot Config, payload map[string]any) (map[string]any, error)
	HandleCallbackResponse(ctx context.Context, bot Config, payload map[string]any) (*CallbackResponse, error)
	DownloadAttachment(ctx context.Context, bot Config, attachment Attachment, actor *Actor) (*DownloadedAttachment, error)
	CreateStreamingReplyCard(ctx context.Context, bot Config, state StreamingCardState) (*CardHandle, error)
	UpdateStreamingReplyCard(ctx context.Context, bot Config, handle CardHandle, state StreamingCardState) error
}

// ErrUnsupportedCapability is returned by the optional methods of an adapter
// that does not implement them; the service checks before calling, so this only
// escapes in a test that calls one directly.
var ErrUnsupportedCapability = fmt.Errorf("bots: provider capability not supported")

// CredentialLoader is the credential-service seam (ICredentialService.load).
type CredentialLoader interface {
	Load(ctx context.Context, key string) (string, error)
}

// CredentialLoaderFunc adapts a function to CredentialLoader.
type CredentialLoaderFunc func(ctx context.Context, key string) (string, error)

// Load implements CredentialLoader.
func (f CredentialLoaderFunc) Load(ctx context.Context, key string) (string, error) {
	return f(ctx, key)
}

// FetchRaw performs a GET and returns the body bytes (the Telegram file
// download endpoint returns the file itself, not JSON).
func (c *ProviderClient) FetchRaw(ctx context.Context, url string) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = ProviderRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	doer := c.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bots: file download returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// newAuthRequest builds a Bearer-authorised request (the Feishu shape). The
// Feishu API is the only provider that needs it, which is why it lives here
// rather than on ProviderClient's public surface.
func newAuthRequest(ctx context.Context, client *ProviderClient, method, endpoint, token string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

// readJSONResponse converts an *http.Response into a JSONResponse (the TS
// fetchBotProviderJson body-consumption rule).
func readJSONResponse(resp *http.Response) (JSONResponse, error) {
	data, err := readAllBody(resp)
	if err != nil {
		return JSONResponse{}, err
	}
	result := JSONResponse{
		OK:     resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status: resp.StatusCode,
		LogID:  resp.Header.Get("x-tt-logid"),
	}
	if len(bytes.TrimSpace(data)) > 0 {
		var payload any
		if unmarshalErr := json.Unmarshal(data, &payload); unmarshalErr == nil {
			result.Payload = payload
		} else if result.OK {
			return result, fmt.Errorf("bots: provider response is not JSON: %w", unmarshalErr)
		}
	}
	return result, nil
}

func readAllBody(resp *http.Response) ([]byte, error) { return io.ReadAll(resp.Body) }

// jsonMustMarshal encodes a card payload; a card value is always a
// marshalable map[string]any, so an error here is a programming error and an
// empty JSON object is the safe degenerate value.
func jsonMustMarshal(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return []byte(`{}`)
	}
	return data
}

// unicodeIsSpaceRune mirrors the `\S` complement used by stripAtMentions.
func unicodeIsSpaceRune(r rune) bool { return unicode.IsSpace(r) }
