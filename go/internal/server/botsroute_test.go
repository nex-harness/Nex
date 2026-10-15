package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tylinux/nex/go/internal/services/bots"
)

// fakeBotsService records what the route forwarded and returns a canned result.
type fakeBotsService struct {
	mu       sync.Mutex
	payloads []map[string]any
	provider []bots.Provider
	result   bots.CallbackResult
	calls    int
}

func (f *fakeBotsService) HandleProviderCallbackResponse(_ context.Context, provider bots.Provider, payload map[string]any) bots.CallbackResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.provider = append(f.provider, provider)
	f.payloads = append(f.payloads, payload)
	return f.result
}

func (f *fakeBotsService) lastPayload() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.payloads) == 0 {
		return nil
	}
	return f.payloads[len(f.payloads)-1]
}

// postToRoute drives one request through the handler built by NewHandler, so the
// test also proves the route is reachable at its real URL (the /api/* chain,
// behind auth when configured).
func postToRoute(t *testing.T, service BotsService, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return requestRoute(t, service, http.MethodPost, path, body, headers)
}

func requestRoute(t *testing.T, service BotsService, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	handler, _, err := NewHandler(Options{Bots: service})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// TestBotCallbackStatusMapping is the four-branch status contract of
// handleBotCallback, in the TS's own order.
func TestBotCallbackStatusMapping(t *testing.T) {
	t.Run("unknown provider is 400 with the exact body", func(t *testing.T) {
		response := postToRoute(t, &fakeBotsService{}, "/api/bots/nope", `{}`, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.Code)
		}
		if got := strings.TrimSpace(response.Body.String()); got != `{"error":"Unsupported provider: nope"}` {
			t.Errorf("body = %s", got)
		}
	})

	t.Run("known provider without an HTTP callback is 400", func(t *testing.T) {
		// telegram, feishu, lark, weixin, discord and wecom are all valid
		// providers that only speak their own long-poll/websocket transport.
		for _, provider := range []string{"telegram", "feishu", "lark", "weixin", "discord", "wecom"} {
			response := postToRoute(t, &fakeBotsService{}, "/api/bots/"+provider, `{}`, nil)
			if response.Code != http.StatusBadRequest {
				t.Errorf("provider %s: status = %d, want 400", provider, response.Code)
			}
			want := `{"error":"Provider ` + provider + ` does not support HTTP callbacks."}`
			if got := strings.TrimSpace(response.Body.String()); got != want {
				t.Errorf("provider %s: body = %s, want %s", provider, got, want)
			}
		}
	})

	t.Run("missing service is 503", func(t *testing.T) {
		// The unsupported-provider checks run BEFORE the service lookup, so a
		// valid webhook provider with no service is what reaches this branch.
		response := postToRoute(t, nil, "/api/bots/webhook", `{}`, nil)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", response.Code)
		}
		if got := strings.TrimSpace(response.Body.String()); got != `{"error":"Bots service is not available."}` {
			t.Errorf("body = %s", got)
		}
	})

	t.Run("service statuses pass through and anything else is 200", func(t *testing.T) {
		cases := []struct {
			name   string
			status int
			want   int
		}{
			{"400 passes through", 400, 400},
			{"401 passes through", 401, 401},
			// Bugfix (TS): Bot 业务失败必须把可重试状态透传给 HTTP provider；
			// 返回 200 会让 webhook/网关误以为消息已消费。
			{"503 passes through", 503, 503},
			{"explicit 200", 200, 200},
			{"absent status is 200", 0, 200},
			{"an unmapped status becomes 200", 418, 200},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				service := &fakeBotsService{result: bots.CallbackResult{OK: testCase.status < 400, Status: testCase.status}}
				response := postToRoute(t, service, "/api/bots/webhook", `{}`, nil)
				if response.Code != testCase.want {
					t.Errorf("status = %d, want %d", response.Code, testCase.want)
				}
			})
		}
	})
}

// TestBotCallbackBodyShape pins the default `{ok, replies}` body and the
// responseBody override.
func TestBotCallbackBodyShape(t *testing.T) {
	t.Run("default body is ok + replies", func(t *testing.T) {
		service := &fakeBotsService{result: bots.CallbackResult{
			OK:      true,
			Replies: []bots.OutboundMessage{{BotID: "b1", Provider: bots.ProviderWebhook, ProviderUserID: "u1", Text: "hi"}},
		}}
		response := postToRoute(t, service, "/api/bots/webhook", `{}`, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.Code)
		}
		var decoded struct {
			OK      bool                   `json:"ok"`
			Replies []bots.OutboundMessage `json:"replies"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("body is not JSON: %v (%s)", err, response.Body.String())
		}
		if !decoded.OK || len(decoded.Replies) != 1 || decoded.Replies[0].Text != "hi" {
			t.Errorf("decoded = %+v", decoded)
		}
	})

	t.Run("nil replies serialize as an empty array, not null", func(t *testing.T) {
		service := &fakeBotsService{result: bots.CallbackResult{OK: false}}
		response := postToRoute(t, service, "/api/bots/webhook", `{}`, nil)
		if got := strings.TrimSpace(response.Body.String()); got != `{"ok":false,"replies":[]}` {
			t.Errorf("body = %s, want ok:false with an empty replies array", got)
		}
	})

	t.Run("responseBody replaces the default body", func(t *testing.T) {
		service := &fakeBotsService{result: bots.CallbackResult{
			OK:           false,
			ResponseBody: map[string]any{"error": "prepare failed"},
			Status:       401,
		}}
		response := postToRoute(t, service, "/api/bots/webhook", `{}`, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", response.Code)
		}
		if got := strings.TrimSpace(response.Body.String()); got != `{"error":"prepare failed"}` {
			t.Errorf("body = %s", got)
		}
	})
}

// TestBotCallbackForwardsSecretAndRawBody pins the two fields the webhook
// provider verifies against, plus the non-JSON and empty-body shapes.
func TestBotCallbackForwardsSecretAndRawBody(t *testing.T) {
	t.Run("header and raw body travel into the payload", func(t *testing.T) {
		service := &fakeBotsService{}
		body := `{"botId":"b1","text":"hello"}`
		postToRoute(t, service, "/api/bots/webhook", body, map[string]string{
			BotSecretHeaderName: "s3cret",
		})
		payload := service.lastPayload()
		if payload["webhookSecret"] != "s3cret" {
			t.Errorf("webhookSecret = %v, want s3cret", payload["webhookSecret"])
		}
		if payload["rawBody"] != body {
			t.Errorf("rawBody = %v, want the exact request body", payload["rawBody"])
		}
		if payload["text"] != "hello" {
			t.Errorf("text = %v, want the JSON body's own field to survive", payload["text"])
		}
	})

	t.Run("an absent secret header is not added", func(t *testing.T) {
		service := &fakeBotsService{}
		postToRoute(t, service, "/api/bots/webhook", `{}`, nil)
		if _, present := service.lastPayload()["webhookSecret"]; present {
			t.Error("webhookSecret must be absent, not empty, when the header is missing")
		}
	})

	t.Run("a non-JSON body becomes {payload: raw}", func(t *testing.T) {
		service := &fakeBotsService{}
		postToRoute(t, service, "/api/bots/webhook", `not json at all`, nil)
		payload := service.lastPayload()
		if payload["payload"] != "not json at all" {
			t.Errorf("payload = %v, want the raw text", payload["payload"])
		}
		if payload["rawBody"] != "not json at all" {
			t.Errorf("rawBody = %v", payload["rawBody"])
		}
	})

	t.Run("an empty body becomes an empty object plus an empty rawBody", func(t *testing.T) {
		service := &fakeBotsService{}
		postToRoute(t, service, "/api/bots/webhook", "", nil)
		payload := service.lastPayload()
		if payload["rawBody"] != "" {
			t.Errorf("rawBody = %v, want an empty string", payload["rawBody"])
		}
		if _, present := payload["payload"]; present {
			t.Error("an empty body must not synthesise a payload key")
		}
	})

	t.Run("a JSON array body becomes {payload: [...]} rather than spreading", func(t *testing.T) {
		service := &fakeBotsService{}
		postToRoute(t, service, "/api/bots/webhook", `[1,2]`, nil)
		payload := service.lastPayload()
		values, ok := payload["payload"].([]any)
		if !ok || len(values) != 2 {
			t.Errorf("payload = %v, want the decoded array under `payload`", payload["payload"])
		}
	})
}

// TestBotCallbackBotIDPathParam covers the second route and the precedence of the
// path param over a body botId.
func TestBotCallbackBotIDPathParam(t *testing.T) {
	t.Run("the path param sets botId", func(t *testing.T) {
		service := &fakeBotsService{}
		postToRoute(t, service, "/api/bots/webhook/bot-42", `{}`, nil)
		if service.lastPayload()["botId"] != "bot-42" {
			t.Errorf("botId = %v, want bot-42", service.lastPayload()["botId"])
		}
	})

	t.Run("the path param wins over the body", func(t *testing.T) {
		service := &fakeBotsService{}
		postToRoute(t, service, "/api/bots/webhook/bot-42", `{"botId":"from-body"}`, nil)
		// The TS spreads the body first and then sets botId, so the path param wins.
		if service.lastPayload()["botId"] != "bot-42" {
			t.Errorf("botId = %v, want the path param to win", service.lastPayload()["botId"])
		}
	})

	t.Run("a percent-encoded botId is decoded", func(t *testing.T) {
		service := &fakeBotsService{}
		postToRoute(t, service, "/api/bots/webhook/bot%2F42", `{}`, nil)
		if service.lastPayload()["botId"] != "bot/42" {
			t.Errorf("botId = %v, want the decoded bot/42", service.lastPayload()["botId"])
		}
	})
}

// TestBotCallbackRouteOwnership pins which requests the route claims. A method or
// shape it does not own must fall through to the gateway (or 404), because
// swallowing it would answer a request Node owns.
func TestBotCallbackRouteOwnership(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		path    string
		claimed bool
	}{
		{"POST without a bot id", http.MethodPost, "/api/bots/webhook", true},
		{"POST with a bot id", http.MethodPost, "/api/bots/webhook/b1", true},
		{"GET is not this route", http.MethodGet, "/api/bots/webhook", false},
		{"PUT is not this route", http.MethodPut, "/api/bots/webhook", false},
		{"a third path segment is not a route", http.MethodPost, "/api/bots/webhook/b1/extra", false},
		{"a trailing slash matches neither TS route", http.MethodPost, "/api/bots/webhook/", false},
		{"the bare prefix is not a route", http.MethodPost, "/api/bots/", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// A fresh fake per case: the call counter is what proves whether the
			// route claimed the request at all.
			service := &fakeBotsService{result: bots.CallbackResult{OK: true}}
			response := requestRoute(t, service, testCase.method, testCase.path, `{}`, nil)
			claimed := service.calls > 0
			if claimed != testCase.claimed {
				t.Errorf("claimed = %v, want %v (status %d)", claimed, testCase.claimed, response.Code)
			}
			if !testCase.claimed && response.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for an unclaimed route with no gateway", response.Code)
			}
		})
	}
}

// TestBotCallbackSitsBehindAuth proves the route is inside the /api/* auth
// middleware, like every other /api route.
func TestBotCallbackSitsBehindAuth(t *testing.T) {
	handler, _, err := NewHandler(Options{
		Bots:         &fakeBotsService{},
		AuthToken:    "token",
		AuthRequired: true,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/bots/webhook", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK {
		t.Error("an unauthenticated POST /api/bots/webhook must not reach the service")
	}
}

// TestBotCallbackDoesNotDoubleReadTheBody guards the streaming property: the
// route reads the body once and the gateway (when present) never sees a
// consumed body for a route Go answered.
func TestBotCallbackDoesNotDoubleReadTheBody(t *testing.T) {
	service := &fakeBotsService{result: bots.CallbackResult{OK: true}}
	response := postToRoute(t, service, "/api/bots/webhook", `{"text":"x"}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := service.lastPayload()["rawBody"]; got != `{"text":"x"}` {
		t.Errorf("rawBody = %q, want the exact bytes", got)
	}
	// The recorder body must be the service's answer, not the echoed request.
	if strings.Contains(response.Body.String(), "rawBody") {
		t.Errorf("response body leaked the request: %s", response.Body.String())
	}
}

// TestBotCallbackContentType pins the JSON content type on both the fixed error
// bodies and the encoded ones.
func TestBotCallbackContentType(t *testing.T) {
	for _, path := range []string{"/api/bots/nope", "/api/bots/telegram", "/api/bots/webhook"} {
		response := postToRoute(t, &fakeBotsService{}, path, `{}`, nil)
		if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("%s: Content-Type = %q", path, got)
		}
	}
}

var _ = io.Discard
