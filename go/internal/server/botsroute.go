// botsroute.go ports the two POST /api/bots/* routes from
// packages/server/src/http.ts (handleBotCallback, lines 382-427).
//
// The wire contract, preserved exactly:
//
//	POST /api/bots/:provider            → handleBotCallback
//	POST /api/bots/:provider/:botId     → handleBotCallback
//
//	Check order (each one a distinct status):
//	  1. provider not in botProviders        → 400 {"error":"Unsupported provider: X"}
//	  2. provider != "webhook"               → 400 {"error":"Provider X does not support HTTP callbacks."}
//	  3. no bots service registered          → 503 {"error":"Bots service is not available."}
//	  4. otherwise the service's own result: 400 / 401 / 503 pass through,
//	     everything else is 200.
//
//	Body: `result.responseBody ?? {ok: result.ok, replies: result.replies}`, and
//	the x-nex-bot-secret header plus the exact raw body text travel into the
//	payload as `webhookSecret` / `rawBody` (the webhook provider verifies the
//	secret against them).
//
//	Body parsing: invalid JSON becomes `{"payload": <raw text>}` — the raw text
//	is never lost — and an empty body becomes `{}`.
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tylinux/nex/go/internal/services/bots"
)

// botsRoutePath is the prefix this file owns.
const botsRoutePath = "/api/bots/"

// BotSecretHeaderName is the inbound secret header (the webhook provider's
// default outbound header of the same name).
const BotSecretHeaderName = bots.DefaultWebhookAuthHeader

// botsUnavailableBody is the exact 503 body, byte for byte.
const botsUnavailableBody = `{"error":"Bots service is not available."}`

// BotsService is the seam the two routes need from the bots package: exactly
// handleProviderCallbackResponse, nothing else.
type BotsService interface {
	HandleProviderCallbackResponse(ctx context.Context, provider bots.Provider, payload map[string]any) bots.CallbackResult
}

// serveBotCallback handles POST /api/bots/:provider[/:botId]. It reports
// whether it owned the request, exactly like servePetsSpritesheet.
func serveBotCallback(service BotsService, w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	escaped, ok := urlPathname(r)
	if !ok || !strings.HasPrefix(escaped, botsRoutePath) {
		return false
	}
	rest := strings.TrimPrefix(escaped, botsRoutePath)
	if rest == "" || strings.HasSuffix(rest, "/") {
		// /api/bots/ with no provider matches neither TS route.
		return false
	}
	segments := strings.Split(rest, "/")
	// The TS registers exactly two patterns, so a third segment is not a route.
	if len(segments) > 2 {
		return false
	}
	provider, err := decodeRouteSegment(segments[0])
	if err != nil {
		// Hono hands an undecodable param through as-is; a bad escape never
		// matches a known provider, so it answers like any unknown provider.
		provider = segments[0]
	}
	botID := ""
	if len(segments) == 2 {
		if decoded, decodeErr := decodeRouteSegment(segments[1]); decodeErr == nil {
			botID = decoded
		} else {
			botID = segments[1]
		}
	}
	if !bots.IsProvider(provider) {
		writeBotsJSONError(w, http.StatusBadRequest, `{"error":"Unsupported provider: `+provider+`"}`)
		return true
	}
	if provider != string(bots.ProviderWebhook) {
		writeBotsJSONError(w, http.StatusBadRequest,
			`{"error":"Provider `+provider+` does not support HTTP callbacks."}`)
		return true
	}
	if service == nil {
		// services.getOptional(IBotsService) missing.
		writeBotsJSONError(w, http.StatusServiceUnavailable, botsUnavailableBody)
		return true
	}
	rawBodyText := readRawBody(r)
	rawBody := map[string]any{}
	if rawBodyText != "" {
		var decoded any
		if unmarshalErr := json.Unmarshal([]byte(rawBodyText), &decoded); unmarshalErr != nil {
			// Not JSON: the raw text is preserved under `payload` so a form-encoded
			// or truncated body still reaches the provider parser.
			rawBody = map[string]any{"payload": rawBodyText}
		} else {
			rawBody = spreadPayload(decoded)
		}
	}
	rawBody["rawBody"] = rawBodyText
	if botID != "" {
		rawBody["botId"] = botID
	}
	if secret := r.Header.Get(BotSecretHeaderName); secret != "" {
		rawBody["webhookSecret"] = secret
	}
	result := service.HandleProviderCallbackResponse(r.Context(), bots.ProviderWebhook, rawBody)
	writeBotsCallbackResult(w, result)
	return true
}

// spreadPayload ports `{...(typeof rawBody === "object" && rawBody !== null ? rawBody : { payload: rawBody })}`:
// an object spreads its own keys, anything else (including an array) becomes the
// `payload` key.
func spreadPayload(decoded any) map[string]any {
	if record, ok := decoded.(map[string]any); ok {
		return record
	}
	return map[string]any{"payload": decoded}
}

// readRawBody ports `await c.req.text().catch(() => "")`: a body that cannot be
// read is an empty string, not an error.
func readRawBody(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	return string(data)
}

// writeBotsCallbackResult ports the status mapping at the end of
// handleBotCallback. Only 400/401/503 are pass-through; every other value — and
// every absent one — is 200.
func writeBotsCallbackResult(w http.ResponseWriter, result bots.CallbackResult) {
	var body any
	if result.ResponseBody != nil {
		body = result.ResponseBody
	} else {
		replies := result.Replies
		if replies == nil {
			replies = []bots.OutboundMessage{}
		}
		body = map[string]any{"ok": result.OK, "replies": replies}
	}
	switch result.Status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusServiceUnavailable:
		// Bugfix (TS, preserved): Bot 业务失败必须把可重试状态透传给 HTTP provider；
		// 返回 200 会让 webhook/网关误以为消息已消费，效果与提前提交 Telegram offset 相同。
		writeBotsJSON(w, result.Status, body)
	default:
		writeBotsJSON(w, http.StatusOK, body)
	}
}

// writeBotsJSONError writes a pre-serialised body verbatim: the three fixed
// error strings are byte-for-byte the TS bodies, and re-encoding them would let
// a stray escaping rule change a client-visible string.
func writeBotsJSONError(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// writeBotsJSON encodes a result body. Hono's c.json uses JSON.stringify with no
// indentation, which is what encoding/json's Marshal produces.
func writeBotsJSON(w http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		writeBotsJSONError(w, http.StatusInternalServerError, `{"error":"Failed to encode bot callback response"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// decodeRouteSegment percent-decodes one path segment (Hono's param decoding).
// A malformed escape leaves the segment untouched, which then simply fails the
// provider check like any other unknown value.
func decodeRouteSegment(segment string) (string, error) {
	return url.PathUnescape(segment)
}
