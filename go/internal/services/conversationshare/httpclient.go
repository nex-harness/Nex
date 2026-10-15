// httpclient.go ports conversationShareHttpClient.ts + the request/dispatch
// half of ConversationShareHttpClient: one method per endpoint, unified
// auth/error normalization, envelope parsing and the safety-check retry-after
// parsing.
//
// PRIVACY: the transport is the injected HTTPDoer interface. The production
// wiring points at the configured Nex API origin; tests use httptest servers
// (loopback) only. No real endpoint may appear in a test. The auth token
// comes from the injected TokenProvider; nothing about it is logged.
package conversationshare

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client error kinds (ConversationShareClientErrorKind) the service surfaces.
const (
	KindAuthenticationRequired = "authentication_required"
	KindFeatureDisabled        = "feature_disabled"
	KindInvalidContract        = "invalid_contract"
	KindInvalidConversation    = "invalid_conversation"
	KindDisclosureRequired     = "disclosure_required"
	KindUnsafeStructure        = "unsafe_structure"
	KindArtifactNotAllowed     = "artifact_not_allowed"
	KindLimitExceeded          = "limit_exceeded"
	KindUploadIncomplete       = "upload_incomplete"
	KindNotFound               = "not_found"
	KindExpired                = "expired"
	KindImportNotAllowed       = "import_not_allowed"
	KindRateLimited            = "rate_limited"
	KindNetwork                = "network"
	KindSafetyCheckPending     = "safety_check_pending"
	KindUnsupportedSchema      = "unsupported_schema_version"
	KindUnknown                = "unknown"
)

// errorKindByCode ports ERROR_KIND_BY_CODE.
var errorKindByCode = map[int64]string{
	3001: KindInvalidContract, 3002: KindRateLimited, 3200: KindFeatureDisabled,
	3201: KindAuthenticationRequired, 3203: KindInvalidContract, 3204: KindInvalidContract,
	3205: KindInvalidConversation, 3206: KindDisclosureRequired, 3207: KindUnsafeStructure,
	3208: KindArtifactNotAllowed, 3209: KindLimitExceeded, 3210: KindUploadIncomplete,
	3211: KindNotFound, 3212: KindExpired, 3213: KindAuthenticationRequired,
	3214: KindImportNotAllowed, 3215: KindSafetyCheckPending,
}

// requestIDHeaderName ports REQUEST_ID_HEADER_NAME.
const requestIDHeaderName = "x-request-id"

// clientError is the Go ConversationShareClientError.
type clientError struct {
	kind         string
	message      string
	status       int64
	code         int64
	hasCode      bool
	requestID    string
	retryAfterMs int64
	hasRetry     bool
}

func (e *clientError) Error() string { return e.message }

// Kind implements the kinded-error seam used by errKind.
func (e *clientError) Kind() string { return e.kind }

// isClientError reports whether err is a *clientError.
func isClientError(err error) (*clientError, bool) {
	if typed, ok := err.(*clientError); ok {
		return typed, true
	}
	return nil, false
}

const (
	defaultTimeoutMs               = 30_000
	confirmTimeoutMs               = 120_000
	uploadBaseTimeoutMs            = 30_000
	uploadMinThroughputBytesPerSec = 128 * 1024
)

// computeUploadTimeoutMs ports computeUploadTimeoutMs: 按体积动态放宽，保底
// 128KB/s 上行带宽，下限仍是全局默认超时。
func computeUploadTimeoutMs(fileSizeBytes int64, floorMs int64) int64 {
	extra := (fileSizeBytes + uploadMinThroughputBytesPerSec - 1) / uploadMinThroughputBytesPerSec * 1000
	if total := uploadBaseTimeoutMs + extra; total > floorMs {
		return total
	}
	return floorMs
}

// HTTPDoer is the transport seam: one HTTP round trip. http.Client satisfies
// it. The signature matches *http.Client exactly (a type alias, not a new
// interface), so the production wiring hands over an *http.Client unchanged and
// tests hand over an httptest-bound client or a recorder around one.
type HTTPDoer = interface {
	Do(request *http.Request) (*http.Response, error)
}

// TokenProvider supplies the bearer token (nil/"" means anonymous).
type TokenProvider func(ctx context.Context) (string, error)

// ClientOptions ports ConversationShareHttpClientOptions.
type ClientOptions struct {
	BaseURL          string
	Doer             HTTPDoer
	Token            TokenProvider
	TimeoutMs        int64
	ConfirmTimeoutMs int64
	// Now is the clock for retry-after parsing (tests).
	Now func() time.Time
}

// Client is the Go ConversationShareHttpClient.
type Client struct {
	baseURL          string
	doer             HTTPDoer
	token            TokenProvider
	timeoutMs        int64
	confirmTimeoutMs int64
	now              func() time.Time
}

// NewClient builds the client.
func NewClient(options ClientOptions) *Client {
	timeout := options.TimeoutMs
	if timeout <= 0 {
		timeout = defaultTimeoutMs
	}
	confirmTimeout := options.ConfirmTimeoutMs
	if confirmTimeout <= 0 {
		confirmTimeout = confirmTimeoutMs
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		baseURL: strings.TrimRight(options.BaseURL, "/"), doer: options.Doer,
		token: options.Token, timeoutMs: timeout, confirmTimeoutMs: confirmTimeout, now: now,
	}
}

// GetCapabilities ports getCapabilities (auth required).
func (c *Client) GetCapabilities(ctx context.Context) (Capabilities, error) {
	var wire capabilitiesWire
	if err := c.requestData(ctx, "/shares/capabilities", http.MethodGet, "required", c.timeoutMs, nil, &wire, false); err != nil {
		return Capabilities{}, err
	}
	capabilities, _, _ := narrowCapabilities(wire)
	return capabilities, nil
}

// CreatePreparation ports createPreparation.
func (c *Client) CreatePreparation(ctx context.Context, request PreparationRequest) (Preparation, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return Preparation{}, err
	}
	var preparation Preparation
	params := requestParams{body: body, contentType: "application/json"}
	if err := c.requestData(ctx, "/shares/preparations", http.MethodPost, "required", c.timeoutMs, &params, &preparation, false); err != nil {
		return Preparation{}, err
	}
	return preparation, nil
}

// UploadArtifact ports uploadArtifact: multipart with the descriptor JSON and
// the file under its display name.
func (c *Client) UploadArtifact(ctx context.Context, preparationID string, descriptor ArtifactDescriptor, fileName string, data []byte) (ArtifactUpload, error) {
	descriptorJSON, err := json.Marshal(descriptor)
	if err != nil {
		return ArtifactUpload{}, err
	}
	form := &bytes.Buffer{}
	writer := newMultipartWriter(form)
	writer.writeField("descriptor", string(descriptorJSON))
	writer.writeFile("file", descriptor.DisplayName, descriptor.MimeType, data)
	writer.close()
	var upload ArtifactUpload
	path := "/shares/preparations/" + pathEscape(preparationID) + "/artifacts"
	params := requestParams{body: form.Bytes(), contentType: writer.contentType()}
	if err := c.requestData(ctx, path, http.MethodPost, "required", computeUploadTimeoutMs(int64(len(data)), c.timeoutMs), &params, &upload, false); err != nil {
		return ArtifactUpload{}, err
	}
	return upload, nil
}

// Confirm ports confirm (long single-request timeout).
func (c *Client) Confirm(ctx context.Context, preparationID string, request *ConfirmRequest) (ShareRecord, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return ShareRecord{}, err
	}
	var record ShareRecord
	path := "/shares/preparations/" + pathEscape(preparationID) + "/confirm"
	params := requestParams{body: body, contentType: "application/json"}
	if err := c.requestData(ctx, path, http.MethodPost, "required", c.confirmTimeoutMs, &params, &record, false); err != nil {
		return ShareRecord{}, err
	}
	return record, nil
}

// GetPreview ports getPreview (auth optional). Returns the raw envelope body
// so the service can do per-row decoding.
func (c *Client) GetPreview(ctx context.Context, shareCode string) (PreviewData, error) {
	var data PreviewData
	path := "/shares/" + pathEscape(shareCode) + "/preview"
	if err := c.requestData(ctx, path, http.MethodGet, "optional", c.timeoutMs, nil, &data, false); err != nil {
		return PreviewData{}, err
	}
	return data, nil
}

// GetContinuation ports getContinuation (auth optional: public_importable 分享
// 的 continuation 由 share code + client request id 授权).
func (c *Client) GetContinuation(ctx context.Context, shareCode, clientRequestID string) (Continuation, error) {
	body, err := json.Marshal(map[string]any{
		"schema_version": SchemaVersion, "client_request_id": clientRequestID,
	})
	if err != nil {
		return Continuation{}, err
	}
	var continuation Continuation
	path := "/shares/" + pathEscape(shareCode) + "/continuation"
	params := requestParams{body: body, contentType: "application/json"}
	if err := c.requestData(ctx, path, http.MethodPost, "optional", c.timeoutMs, &params, &continuation, false); err != nil {
		return Continuation{}, err
	}
	return continuation, nil
}

// PreviewData ports the wire shape of conversationSharePreviewDataSchema the
// service reads (rows stay raw for per-row decoding).
type PreviewData struct {
	SchemaVersion int                            `json:"schema_version"`
	Share         ContinuationShareMetaWithoutID `json:"share"`
	Rows          []json.RawMessage              `json:"rows"`
	Artifacts     []continuationArtifact         `json:"artifacts"`
	Integrity     struct {
		ProjectionSha256  string `json:"projection_sha256"`
		ArtifactSetSha256 string `json:"artifact_set_sha256"`
	} `json:"integrity"`
}

// ContinuationShareMetaWithoutID is the public metadata (no share_id).
type ContinuationShareMetaWithoutID struct {
	Title      string `json:"title"`
	AccessMode string `json:"access_mode"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
}

type requestParams struct {
	body        []byte
	contentType string
}

// requestData ports requestData: token, request-id header, error envelope
// normalization, success envelope unwrap.
func (c *Client) requestData(ctx context.Context, path, method, auth string, timeoutMs int64, params *requestParams, out any, _ bool) error {
	token := ""
	if c.token != nil {
		if value, err := c.token(ctx); err == nil {
			token = strings.TrimSpace(value)
		}
	}
	if auth == "required" && token == "" {
		return &clientError{kind: KindAuthenticationRequired, message: "Conversation share authentication required", status: 401}
	}
	url := c.baseURL + "/" + strings.TrimLeft(path, "/")
	var bodyReader io.Reader
	if params != nil {
		bodyReader = bytes.NewReader(params.body)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return &clientError{kind: KindNetwork, message: err.Error()}
	}
	request.Header.Set(requestIDHeaderName, newRequestID())
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if params != nil && params.contentType != "" {
		request.Header.Set("Content-Type", params.contentType)
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	request = request.WithContext(requestCtx)
	response, err := c.doer.Do(request)
	if err != nil {
		return &clientError{kind: KindNetwork, message: networkMessage(err)}
	}
	defer response.Body.Close()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		return &clientError{kind: KindNetwork, message: "Conversation share network error"}
	}
	responseRequestID := normalizeRequestID(response.Header.Get(requestIDHeaderName))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return c.responseError(path, method, response, string(text), responseRequestID)
	}
	// Success envelope: {code: 0, msg: string, data: <payload>}.
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(text, &envelope); err != nil || envelope.Code != 0 {
		return &clientError{
			kind: KindInvalidContract, message: "Conversation share API response does not match the client contract",
			status: int64(response.StatusCode), requestID: responseRequestID,
		}
	}
	if out != nil {
		if len(envelope.Data) == 0 {
			// A missing data member cannot satisfy any of the callers' shapes.
			return &clientError{kind: KindInvalidContract, message: "Conversation share API response does not match the client contract"}
		}
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return &clientError{
				kind: KindInvalidContract, message: "Conversation share API response does not match the client contract",
				status: int64(response.StatusCode), requestID: responseRequestID,
			}
		}
	}
	if err := assertSupportedSchemaVersion(out); err != nil {
		return err
	}
	return nil
}

// responseError ports the !response.ok branch: 401-empty, error envelope with
