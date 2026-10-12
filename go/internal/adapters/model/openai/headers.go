package openai

import (
	"net/url"
	"strings"
)

// ProviderUtilsVersion is the ai-sdk/provider-utils version whose user-agent
// suffix the Node agent sends. It is pinned here because the header value is
// observed on the wire.
const ProviderUtilsVersion = "4.0.39"

// BuildHeaders reproduces the outbound header set the Node agent sends, as
// captured from the mock provider (scripts/go-rewrite/record/mock-model.mjs with
// NEX_REC_BODY_DIR). The per-run values come from HeaderInput.
//
// Headers reproduced exactly:
//
//	authorization, content-type, http-referer, user-agent, x-client-language,
//	x-client-timezone, x-nex-agent, x-nex-app-version, x-nex-session-type,
//	x-nex-trace-id, x-os-category, x-os-version, x-platform, x-query-id,
//	x-release-channel, x-request-id, x-session-id, x-title
//
// Headers NOT reproduced: host, connection, content-length (set by the HTTP
// transport), and accept/accept-language/accept-encoding/sec-fetch-mode (undici
// defaults in Node; Go's net/http uses its own defaults). These are transport
// concerns, not model-client attribution, and gateways do not key on them.
func BuildHeaders(in HeaderInput) map[string]string {
	h := map[string]string{
		"user-agent":        userAgent(in),
		"http-referer":      in.HTTPReferer,
		"x-title":           in.Title,
		"x-release-channel": in.ReleaseChannel,
		"x-client-language": in.ClientLanguage,
		"x-client-timezone": in.ClientTimezone,
		"x-nex-agent":       in.NexAgent,
	}
	if in.Authorization != "" {
		h["authorization"] = in.Authorization
	}
	if in.AppVersion != "" {
		h["x-nex-app-version"] = in.AppVersion
	}
	if in.Platform != "" {
		h["x-platform"] = in.Platform
	}
	if in.OSCategory != "" {
		h["x-os-category"] = in.OSCategory
	}
	if in.OSVersion != "" {
		h["x-os-version"] = in.OSVersion
	}
	// Attribution headers.
	if in.RequestID != "" {
		h["x-request-id"] = in.RequestID
	}
	if in.TraceID != "" {
		h["x-nex-trace-id"] = in.TraceID
	}
	if in.SessionType != "" {
		h["x-nex-session-type"] = in.SessionType
	}
	if in.QueryID != "" {
		h["x-query-id"] = in.QueryID
	}
	if in.SessionID != "" {
		h["x-session-id"] = in.SessionID
	}
	if in.OpenCodeSession != "" {
		h["x-opencode-session"] = in.OpenCodeSession
	}
	// Provider static headers are merged last; they may override defaults except
	// that attribution headers must win (x-nex-* and x-request-id are attribution).
	for name, value := range in.Extra {
		lower := strings.ToLower(name)
		if isAttributionHeader(lower) {
			continue
		}
		h[lower] = value
	}
	// Drop empty values so we never send an empty header.
	for name, value := range h {
		if value == "" {
			delete(h, name)
		}
	}
	return h
}

func isAttributionHeader(lower string) bool {
	switch lower {
	case "x-request-id", "x-nex-trace-id", "x-nex-session-type", "x-query-id", "x-session-id":
		return true
	}
	return false
}

// userAgent reproduces withUserAgentSuffix(headers, `ai-sdk/provider-utils/${VERSION}`,
// `runtime/node.js/${process.version}`): the base agent (Nex/<version>), then the
// library suffix, then the runtime suffix. RuntimeVersion defaults to the Node
// major the agent ships (24).
func userAgent(in HeaderInput) string {
	base := in.UserAgent
	if base == "" {
		base = "Nex/unknown"
	}
	runtimeVersion := in.RuntimeVersion
	if runtimeVersion == "" {
		runtimeVersion = "24"
	}
	return base + " ai-sdk/provider-utils/" + ProviderUtilsVersion + " runtime/node.js/" + runtimeVersion
}

func parseProxy(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return url.Parse(raw)
}
