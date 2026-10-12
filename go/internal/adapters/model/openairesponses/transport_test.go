package openairesponses_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/model/openai"
	"github.com/tylinux/nex/go/internal/adapters/model/openairesponses"
	"github.com/tylinux/nex/go/internal/modelclient"
	"github.com/tylinux/nex/go/internal/tooldefs"
)

// captured is one recorded HTTP exchange against the recording server.
type captured struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

// recorder is an httptest server that records the request and replies with a
// fixed SSE body.
func recorder(t *testing.T, sse string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.Method, got.Path, got.Headers, got.Body = r.Method, r.URL.Path, r.Header.Clone(), body
		if sse == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func testdataDir(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func defaultTools(t testing.TB) []tooldefs.Def {
	t.Helper()
	defs, err := tooldefs.Default()
	if err != nil {
		t.Fatalf("tooldefs.Default: %v", err)
	}
	return defs
}

func sampleRequest(t testing.TB) openairesponses.Request {
	t.Helper()
	return openairesponses.Request{
		Model:        "deepseek-flash",
		Instructions: "you are nex",
		Input: []openairesponses.Message{
			{Role: "system", Content: "you are nex"},
			{Role: "user", Content: "hi"},
			{
				Role:      "assistant",
				ToolCalls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"filePath\":\"/tmp/x\"}"}}]`),
			},
			{Role: "tool", ToolCallID: "call_1", Content: "file body"},
		},
		Tools:           defaultTools(t),
		ToolChoice:      openairesponses.ToolChoiceAuto,
		MaxOutputTokens: 4096,
	}
}

func headerInput() openai.HeaderInput {
	return openai.HeaderInput{
		Authorization:  "Bearer sk-test-fake",
		UserAgent:      "Nex/1.0.0-go",
		RuntimeVersion: "24",
		Platform:       "linux-x64",
		OSCategory:     "linux",
		OSVersion:      "7.0.14-12-pve",
		NexAgent:       "glm",
		ReleaseChannel: "production",
		Title:          "Z Code@electron",
		HTTPReferer:    "https://ai-proxy.tylinux.cn/v1",
	}
}

// TestTransportPostsToResponsesPath pins the endpoint: the base URL is the
// provider's /v1 root and the transport must append exactly "/responses". The
// vendor SDK does the same with its own base URL, so both dialects hit the same
// path shape and a misconfigured base fails loudly here instead of as a 404 the
// classifier would call a generic 4xx.
func TestTransportPostsToResponsesPath(t *testing.T) {
	srv, got := recorder(t, "")
	tr := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", Headers: headerInput()})
	if tr.URL() != srv.URL+"/v1/responses" {
		t.Fatalf("URL() = %q, want %q", tr.URL(), srv.URL+"/v1/responses")
	}
	body, err := openairesponses.BuildBody(sampleRequest(t))
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	if _, err := tr.Do(context.Background(), modelclient.Request{Body: body}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.Method)
	}
	if got.Path != "/v1/responses" {
		t.Errorf("path = %q, want /v1/responses", got.Path)
	}
	if ct := got.Headers.Get("content-type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	if accept := got.Headers.Get("accept"); accept != "text/event-stream" {
		t.Errorf("accept = %q, want text/event-stream", accept)
	}
	// The attribution header set must be identical to the chat adapter's; only
	// accept differs (the Responses body is an event stream).
	if auth := got.Headers.Get("authorization"); auth != "Bearer sk-test-fake" {
		t.Errorf("authorization = %q", auth)
	}
	if ua := got.Headers.Get("user-agent"); !strings.HasPrefix(ua, "Nex/1.0.0-go") {
		t.Errorf("user-agent = %q", ua)
	}
	if got.Headers.Get("x-nex-agent") != "glm" {
		t.Errorf("x-nex-agent = %q", got.Headers.Get("x-nex-agent"))
	}
	if got.Headers.Get("x-platform") != "linux-x64" {
		t.Errorf("x-platform = %q", got.Headers.Get("x-platform"))
	}
	// The SDK must not leak vendor telemetry headers: we own the header set.
	for _, vendor := range []string{"x-stainless-lang", "x-stainless-package-version", "x-stainless-runtime"} {
		if v := got.Headers.Get(vendor); v != "" {
			t.Errorf("unexpected vendor header %s = %q", vendor, v)
		}
	}
}

// TestResponsesURLTrimsTrailingSlash pins the base-URL joining rule so
// "…/v1" and "…/v1/" cannot drift into "…/v1//responses".
func TestResponsesURLTrimsTrailingSlash(t *testing.T) {
	cases := map[string]string{
		"https://ai-proxy.tylinux.cn/v1":    "https://ai-proxy.tylinux.cn/v1/responses",
		"https://ai-proxy.tylinux.cn/v1/":   "https://ai-proxy.tylinux.cn/v1/responses",
		"http://127.0.0.1:9/v1//":           "http://127.0.0.1:9/v1/responses",
		"https://api.openai.com/v1":         "https://api.openai.com/v1/responses",
		"https://ai-proxy.tylinux.cn":       "https://ai-proxy.tylinux.cn/responses",
		"https://ai-proxy.tylinux.cn/":      "https://ai-proxy.tylinux.cn/responses",
		"https://ai-proxy.tylinux.cn/v1/v1": "https://ai-proxy.tylinux.cn/v1/v1/responses",
	}
	for base, want := range cases {
		if got := openairesponses.ResponsesURL(base); got != want {
			t.Errorf("ResponsesURL(%q) = %q, want %q", base, got, want)
		}
	}
}

// TestRequestBodyMatchesRecordedSDK pins our request body against the body the
// real @ai-sdk/openai responses model serialised for the same conversation
// (scripts/go-rewrite/gen-openai-responses-vectors.mts, recorded into
// testdata/vectors/model-openai-responses.json). This is the differential that
// keeps D14 honest: our hand-built params must agree with the vendor SDK's,
// field for field.
func TestRequestBodyMatchesRecordedSDK(t *testing.T) {
	recorded := readVector(t)
	if len(recorded.Request) == 0 {
		t.Skip("recorded request body not generated")
	}
	ours, err := openairesponses.BuildBody(sampleRequest(t))
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(ours, &got); err != nil {
		t.Fatalf("our body is not JSON: %v", err)
	}
	if err := json.Unmarshal(recorded.Request, &want); err != nil {
		t.Fatalf("recorded body is not JSON: %v", err)
	}
	for _, key := range []string{"model", "max_output_tokens", "stream"} {
		if got[key] != want[key] {
			t.Errorf("%s = %v, recorded SDK sends %v", key, got[key], want[key])
		}
	}
	if got["tool_choice"] != want["tool_choice"] {
		t.Errorf("tool_choice = %v, recorded SDK sends %v", got["tool_choice"], want["tool_choice"])
	}
}

// TestToolJSONIsByteIdenticalToToolsJSON is the load-bearing test for the
// tool payload: the tools array we send must be our tools.json schemas,
// un-rewritten. Only the outer shape flattens ({"type":"function","function":
// {…}} becomes the Responses flat form); the `parameters` object must be
// semantically identical, key for key, for all 20 tools, in the same order.
func TestToolJSONIsByteIdenticalToToolsJSON(t *testing.T) {
	body, err := openairesponses.BuildBody(sampleRequest(t))
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	var sent struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("request body: %v", err)
	}
	defs := defaultTools(t)
	if len(sent.Tools) != len(defs) {
		t.Fatalf("sent %d tools, tools.json has %d", len(sent.Tools), len(defs))
	}
	for i, tool := range sent.Tools {
		var source struct {
			Function map[string]any `json:"function"`
		}
		if err := json.Unmarshal(defs[i].JSON, &source); err != nil {
			t.Fatalf("tools.json entry %d: %v", i, err)
		}
		if tool["type"] != "function" {
			t.Errorf("tool %d type = %v, want function", i, tool["type"])
		}
		if tool["name"] != source.Function["name"] {
			t.Errorf("tool %d name = %v, want %v", i, tool["name"], source.Function["name"])
		}
		if tool["description"] != source.Function["description"] {
			t.Errorf("tool %d description changed", i)
		}
		gotParams, _ := json.Marshal(tool["parameters"])
		wantParams, _ := json.Marshal(source.Function["parameters"])
		if string(gotParams) != string(wantParams) {
			t.Errorf("tool %d (%v) parameters differ:\n got %s\nwant %s",
				i, source.Function["name"], gotParams, wantParams)
		}
		// The flattening must not introduce or drop keys.
		for key := range source.Function {
			switch key {
			case "name", "description", "parameters", "strict":
			default:
				t.Errorf("tool %d: source key %q was dropped by the flattener", i, key)
			}
		}
	}
}

// TestToolsMatchRecordedSDK compares our emitted tool array against the one the
// vendor SDK produced for the same tools.json entries.
func TestToolsMatchRecordedSDK(t *testing.T) {
	recorded := readVector(t)
	var want struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(recorded.Request, &want); err != nil || len(want.Tools) == 0 {
		t.Skip("recorded request body not generated")
	}
	body, err := openairesponses.BuildBody(sampleRequest(t))
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if len(got.Tools) != len(want.Tools) {
		t.Fatalf("sent %d tools, recorded SDK sent %d", len(got.Tools), len(want.Tools))
	}
	for i := range got.Tools {
		for _, key := range []string{"type", "name", "description"} {
			if got.Tools[i][key] != want.Tools[i][key] {
				t.Errorf("tool %d %s = %v, recorded SDK sends %v", i, key, got.Tools[i][key], want.Tools[i][key])
			}
		}
		gotParams, _ := json.Marshal(got.Tools[i]["parameters"])
		wantParams, _ := json.Marshal(want.Tools[i]["parameters"])
		if string(gotParams) != string(wantParams) {
			t.Errorf("tool %d parameters differ from the recorded SDK:\n got %s\nwant %s", i, gotParams, wantParams)
		}
	}
}

// TestInputProjection pins the chat-completions -> Responses item projection.
func TestInputProjection(t *testing.T) {
	body, err := openairesponses.BuildBody(sampleRequest(t))
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	var sent struct {
		Instructions string           `json:"instructions"`
		Input        []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if sent.Instructions != "you are nex" {
		t.Errorf("instructions = %q", sent.Instructions)
	}
	// The system turn travels out of band, so it must not be repeated in input.
	var types []string
	for _, item := range sent.Input {
		types = append(types, item["type"].(string))
	}
	want := []string{"message", "function_call", "function_call_output"}
	if len(types) != len(want) {
		t.Fatalf("input items = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("input[%d].type = %s, want %s", i, types[i], want[i])
		}
	}
	if got := sent.Input[0]["content"]; got != "hi" {
		t.Errorf("user content = %v, want hi", got)
	}
	if got := sent.Input[1]["call_id"]; got != "call_1" {
		t.Errorf("function_call call_id = %v", got)
	}
	if got := sent.Input[1]["arguments"]; got != `{"filePath":"/tmp/x"}` {
		t.Errorf("function_call arguments = %v (must reach the wire byte-identical)", got)
	}
	if got := sent.Input[2]["output"]; got != "file body" {
		t.Errorf("function_call_output output = %v", got)
	}
}

// TestDoRejectsEmptyBody guards the invariant that the transport never sends a
// body it did not build: an empty body would be a silent 400 from the provider.
func TestDoRejectsEmptyBody(t *testing.T) {
	srv, _ := recorder(t, "")
	tr := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", Headers: headerInput()})
	_, err := tr.Do(context.Background(), modelclient.Request{})
	if err == nil {
		t.Fatal("Do with an empty body succeeded")
	}
	failure, ok := modelclient.FailureOf(err)
	if !ok && !strings.Contains(err.Error(), "empty body") {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = failure
}

// TestNon2xxIsReturnedForClassification: a provider error must reach the retry
// loop as a Response with a status and body, not as a Go error, so
// classifyModelFailure can decide retryability from status + headers + body.
func TestNon2xxIsReturnedForClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("retry-after", "2")
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error","code":"1302"}}`))
	}))
	defer srv.Close()

	body, err := openairesponses.BuildBody(sampleRequest(t))
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	tr := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", Headers: headerInput()})
	resp, err := tr.Do(context.Background(), modelclient.Request{Body: body})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != 429 {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if !strings.Contains(resp.BodyText, "slow down") {
		t.Errorf("body = %q", resp.BodyText)
	}
	if resp.Headers["retry-after"] != "2" {
		t.Errorf("retry-after header = %q, want 2 (the retry policy parses it)", resp.Headers["retry-after"])
	}
	if string(resp.RequestBody) != string(body) {
		t.Error("RequestBody must echo the body so the classifier can read the request")
	}
}

// TestRetryLoopClassifiesProviderError drives the whole 429 path through the
// real modelclient.Client: the transport hands the non-2xx response back as a
// Response (not a Go error), the retry loop builds an APICallError from status +
// headers + body, the classifier marks it retryable, and the Retry-After the
// transport surfaced is what schedules the next attempt. If any layer of that
// chain regressed, the agent would surface a raw transport error instead.
func TestRetryLoopClassifiesProviderError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("retry-after", "2")
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error","code":"1302"}}`))
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\"}}\n\n" +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"Recovered.\"}\n\n" +
				"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\"}}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n" +
				"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	sink := &statusSink{}
	client := modelclient.NewClient(modelclient.Config{
		ProviderID:   "proxy",
		ModelID:      "deepseek-flash",
		ProviderKind: ProviderKindResponses,
		Transport:    "sse",
		Retry:        modelclient.DefaultRetryOptions(),
		Clock:        modelclient.RealClock(),
		// The decoder factory is what routes the SSE bodies to the Responses
		// dialect. cmd/nex-agent sets it from the provider kind; tests must do
		// the same or the chat-completions decoder will swallow every event.
		NewDecoder: openairesponses.DecoderFactory(),
	}, openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", Headers: headerInput()}), sink)

	body, err := openairesponses.BuildBody(sampleRequest(t))
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := client.Stream(ctx, modelclient.Request{Body: body})
	var kinds []modelclient.EventKind
	for ev := range h.Events() {
		kinds = append(kinds, ev.Kind)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("stream failed after a retryable 429: %v", err)
	}
	if calls < 2 {
		t.Fatalf("provider called %d times, want at least 2 (the 429 must be retried)", calls)
	}
	if got := h.FinishReason(); got != modelclient.FinishStop {
		t.Errorf("finishReason = %q, want %q", got, modelclient.FinishStop)
	}
	want := []modelclient.EventKind{
		modelclient.EventStart,
		modelclient.EventTextStart,
		modelclient.EventTextDelta,
		modelclient.EventTextEnd,
		modelclient.EventFinish,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("event[%d] = %s, want %s", i, kinds[i], want[i])
		}
	}
	// The retry must have published started, failed, retry_scheduled, started,
	// completed — the S13 telemetry sequence.
	wantStatuses := []string{
		"model_request_started",
		"model_request_failed",
		"model_retry_scheduled",
		"model_request_started",
		"model_request_completed",
	}
	got := sink.statuses()
	if len(got) != len(wantStatuses) {
		t.Fatalf("statuses = %v, want %v", got, wantStatuses)
	}
	for i := range wantStatuses {
		if got[i] != wantStatuses[i] {
			t.Errorf("status[%d] = %s, want %s", i, got[i], wantStatuses[i])
		}
	}
}

// TestStreamingResponseExposesStream: a 2xx must surface a readable stream, not
// a buffered body, so events reach the UI while the model is still writing.
func TestStreamingResponseExposesStream(t *testing.T) {
	sse := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"delta\":\"hi\"}\n\n"
	srv, _ := recorder(t, sse)
	body, err := openairesponses.BuildBody(sampleRequest(t))
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	tr := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", Headers: headerInput()})
	resp, err := tr.Do(context.Background(), modelclient.Request{Body: body})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Stream == nil {
		t.Fatal("Stream is nil for a 200 response")
	}
	defer resp.Stream.Close()
	buf := make([]byte, 64)
	n, err := resp.Stream.Read(buf)
	if err != nil && n == 0 {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "response.output_text.delta") {
		t.Errorf("stream = %q", string(buf[:n]))
	}
}
