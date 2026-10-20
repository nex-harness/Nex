package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tylinux/nex-next/server/internal/app"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

const (
	authToken   = "browser-secret"
	runnerToken = "runner-token"
	workspace   = "/workspace/demo"
	runnerID    = "runner-1"
)

type harness struct {
	server  *httptest.Server
	app     *app.App
	session string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	application, err := app.New(app.Options{
		DataDir:     t.TempDir(),
		WebRoot:     "/nonexistent",
		AuthToken:   authToken,
		RunnerToken: runnerToken,
		ServerName:  "e2e",
		Version:     "1.2.3",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	server := httptest.NewServer(application.Handler())
	t.Cleanup(server.Close)
	return &harness{server: server, app: application}
}

func (h *harness) login(t *testing.T) string {
	t.Helper()
	response, err := http.Post(h.server.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"token":"`+authToken+`"}`))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("login status = %d: %s", response.StatusCode, body)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %d", len(cookies))
	}
	return cookies[0].Value
}

func (h *harness) dialRunner(t *testing.T) *websocket.Conn {
	t.Helper()
	header := http.Header{"Authorization": []string{"Bearer " + runnerToken}}
	conn, _, err := websocket.Dial(context.Background(), h.server.URL+"/runner", &websocket.DialOptions{
		HTTPHeader: header,
	})
	if err != nil {
		t.Fatalf("dial runner: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func (h *harness) dialBrowser(t *testing.T, session string) *websocket.Conn {
	t.Helper()
	header := http.Header{"Cookie": []string{"nex_session=" + session}}
	conn, _, err := websocket.Dial(context.Background(), h.server.URL+"/ws", &websocket.DialOptions{
		HTTPHeader: header,
	})
	if err != nil {
		t.Fatalf("dial browser: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func runnerHello(t *testing.T, conn *websocket.Conn, id, path string) {
	t.Helper()
	hello, _ := json.Marshal(map[string]any{
		"t":        "hello",
		"runnerId": id,
		"name":     "test-runner",
		"version":  "0.1.0",
		"os":       "darwin",
		"arch":     "arm64",
		"workspaces": []map[string]string{
			{"path": path, "label": "demo"},
		},
	})
	if err := conn.Write(context.Background(), websocket.MessageText, hello); err != nil {
		t.Fatalf("writing hello: %v", err)
	}
	_, data, err := conn.Read(context.Background())
	if err != nil {
		t.Fatalf("reading welcome: %v", err)
	}
	var welcome struct {
		T             string `json:"t"`
		ServerVersion string `json:"serverVersion"`
	}
	if err := json.Unmarshal(data, &welcome); err != nil {
		t.Fatalf("decoding welcome: %v", err)
	}
	if welcome.T != "welcome" {
		t.Fatalf("welcome.t = %q", welcome.T)
	}
}

// browserClient encodes/decodes Channel RPC frames over a WS connection.
type browserClient struct {
	t    *testing.T
	conn *websocket.Conn
}

func newBrowserClient(t *testing.T, conn *websocket.Conn) *browserClient {
	t.Helper()
	client := &browserClient{t: t, conn: conn}
	// The server sends Initialize immediately on connect.
	frame := client.readFrame()
	response, err := rpcwire.DecodeResponse(frame.Payload)
	if err != nil {
		t.Fatalf("decoding initialize: %v", err)
	}
	if response.Type != rpcwire.ResponseInitialize {
		t.Fatalf("first response = %s, want Initialize", response.Type)
	}
	return client
}

func (c *browserClient) readFrame() rpcwire.Frame {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := c.conn.Read(ctx)
	if err != nil {
		c.t.Fatalf("reading browser frame: %v", err)
	}
	if typ != websocket.MessageBinary {
		c.t.Fatalf("browser frame type = %v, want binary", typ)
	}
	frame, rest, err := rpcwire.ReadFrame(data, 0)
	if err != nil {
		c.t.Fatalf("decoding frame: %v", err)
	}
	if len(rest) != 0 {
		c.t.Fatalf("unexpected trailing bytes")
	}
	return frame
}

func (c *browserClient) send(reqType rpcwire.RequestType, id uint32, channel, name string, arg rpcwire.Value) {
	c.t.Helper()
	payload, err := rpcwire.EncodeRequest(reqType, id, channel, name, arg)
	if err != nil {
		c.t.Fatalf("encoding request: %v", err)
	}
	data := rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: payload})
	if err := c.conn.Write(context.Background(), websocket.MessageBinary, data); err != nil {
		c.t.Fatalf("writing browser request: %v", err)
	}
}

func (c *browserClient) readResponse() rpcwire.Response {
	c.t.Helper()
	frame := c.readFrame()
	response, err := rpcwire.DecodeResponse(frame.Payload)
	if err != nil {
		c.t.Fatalf("decoding response: %v", err)
	}
	return response
}

func (c *browserClient) call(id uint32, channel, method string, arg rpcwire.Value) rpcwire.Response {
	c.t.Helper()
	c.send(rpcwire.RequestPromise, id, channel, method, arg)
	return c.readResponse()
}

func mustRaw(t *testing.T, literal string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(literal)) {
		t.Fatalf("invalid JSON literal: %s", literal)
	}
	return json.RawMessage(literal)
}

func TestEndToEndRoutingAndReconnect(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)

	runner := h.dialRunner(t)
	runnerHello(t, runner, runnerID, workspace)

	// server-info now advertises the workspace with a "runner: basename" label
	// and no workspaceIdentity.
	request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/server-info", nil)
	request.Header.Set("Cookie", "nex_session="+session)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("server-info: %v", err)
	}
	defer response.Body.Close()
	var info struct {
		Workspaces []map[string]any `json:"workspaces"`
	}
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatalf("decoding server-info: %v", err)
	}
	if len(info.Workspaces) != 1 {
		t.Fatalf("workspaces = %+v", info.Workspaces)
	}
	if info.Workspaces[0]["path"] != workspace {
		t.Fatalf("workspace path = %v", info.Workspaces[0]["path"])
	}
	if info.Workspaces[0]["label"] != "test-runner: demo" {
		t.Fatalf("workspace label = %v", info.Workspaces[0]["label"])
	}
	if _, present := info.Workspaces[0]["workspaceIdentity"]; present {
		t.Fatalf("server-info must not set workspaceIdentity: %+v", info.Workspaces[0])
	}

	browser := newBrowserClient(t, h.dialBrowser(t, session))

	// setting.get injects every routed workspace path into recentProjects.
	settingsResponse := browser.call(1, "setting", "get", rpcwire.Undefined{})
	if settingsResponse.Type != rpcwire.ResponsePromiseSuccess {
		t.Fatalf("setting.get = %+v", settingsResponse)
	}
	settingsBody, _ := settingsResponse.Body.(json.RawMessage)
	var settings struct {
		RecentProjects []string `json:"recentProjects"`
	}
	if err := json.Unmarshal(settingsBody, &settings); err != nil {
		t.Fatalf("decoding settings: %v", err)
	}
	if len(settings.RecentProjects) != 1 || settings.RecentProjects[0] != workspace {
		t.Fatalf("recentProjects = %v, want the routed workspace", settings.RecentProjects)
	}

	// model-selection has no workspace in its args: first connected runner.
	browser.send(rpcwire.RequestPromise, 2, "model-selection", "getView", mustRaw(t, `[{"selection":null}]`))
	_, modelFrame, err := runner.Read(context.Background())
	if err != nil {
		t.Fatalf("runner did not receive model-selection.getView: %v", err)
	}
	var modelCall struct {
		T       string `json:"t"`
		ID      string `json:"id"`
		Channel string `json:"channel"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal(modelFrame, &modelCall); err != nil {
		t.Fatalf("decoding model call: %v", err)
	}
	if modelCall.Channel != "model-selection" || modelCall.Method != "getView" {
		t.Fatalf("model call = %+v", modelCall)
	}
	modelResult, _ := json.Marshal(map[string]any{
		"t":     "result",
		"id":    modelCall.ID,
		"ok":    true,
		"value": map[string]any{"revision": 1, "providers": []any{}},
	})
	if err := runner.Write(context.Background(), websocket.MessageText, modelResult); err != nil {
		t.Fatalf("writing model result: %v", err)
	}
	modelResponse := browser.readResponse()
	if modelResponse.Type != rpcwire.ResponsePromiseSuccess || modelResponse.ID != 2 {
		t.Fatalf("model-selection response = %+v", modelResponse)
	}

	// nex-task list + dynamic listen route by workspacePath.
	browser.send(rpcwire.RequestPromise, 3, "nex-task", "listTasks", mustRaw(t, `[{"workspacePath":"`+workspace+`"}]`))
	_, taskFrame, err := runner.Read(context.Background())
	if err != nil {
		t.Fatalf("runner did not receive nex-task.listTasks: %v", err)
	}
	var taskCall struct {
		ID      string `json:"id"`
		Channel string `json:"channel"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal(taskFrame, &taskCall); err != nil {
		t.Fatalf("decoding task call: %v", err)
	}
	if taskCall.Channel != "nex-task" || taskCall.Method != "listTasks" {
		t.Fatalf("task call = %+v", taskCall)
	}
	taskResult, _ := json.Marshal(map[string]any{"t": "result", "id": taskCall.ID, "ok": true, "value": []any{}})
	if err := runner.Write(context.Background(), websocket.MessageText, taskResult); err != nil {
		t.Fatalf("writing task result: %v", err)
	}
	if taskResponse := browser.readResponse(); taskResponse.Type != rpcwire.ResponsePromiseSuccess || taskResponse.ID != 3 {
		t.Fatalf("nex-task response = %+v", taskResponse)
	}

	browser.send(rpcwire.RequestEventListen, 4, "nex-task", "onDynamicWorkspaceEvent",
		mustRaw(t, `[{"workspacePath":"`+workspace+`"}]`))
	_, taskListenFrame, err := runner.Read(context.Background())
	if err != nil {
		t.Fatalf("runner did not receive nex-task listen: %v", err)
	}
	var taskListen struct {
		T     string `json:"t"`
		ID    string `json:"id"`
		Event string `json:"event"`
	}
	if err := json.Unmarshal(taskListenFrame, &taskListen); err != nil {
		t.Fatalf("decoding task listen: %v", err)
	}
	if taskListen.T != "listen" || taskListen.Event != "onDynamicWorkspaceEvent" {
		t.Fatalf("task listen = %+v", taskListen)
	}
	taskEvent, _ := json.Marshal(map[string]any{
		"t": "event", "id": taskListen.ID,
		"value": map[string]any{"type": "workspace_task_list_changed", "workspacePath": workspace},
	})
	if err := runner.Write(context.Background(), websocket.MessageText, taskEvent); err != nil {
		t.Fatalf("writing task event: %v", err)
	}
	if fired := browser.readResponse(); fired.Type != rpcwire.ResponseEventFire || fired.ID != 4 {
		t.Fatalf("task event = %+v", fired)
	}

	// Promise: nex-agent.subscribeConversationV4 routes by workspacePath.
	// The real client (ProxyChannel.toService → ChannelClient) sends the method
	// arguments as an array, so the wire body is `[arg]`.
	browser.send(rpcwire.RequestPromise, 7, "nex-agent", "subscribeConversationV4", mustRaw(t, `[{
		"workspacePath": "`+workspace+`",
		"sessionId": "sess-1",
		"workspaceIdentity": "runner:ignored:should-not-be-used"
	}]`))

	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	_, runnerFrame, err := runner.Read(readCtx)
	cancelRead()
	if err != nil {
		t.Fatalf("runner did not receive the forwarded call: %v", err)
	}
	var call struct {
		T       string          `json:"t"`
		ID      string          `json:"id"`
		Channel string          `json:"channel"`
		Method  string          `json:"method"`
		Arg     json.RawMessage `json:"arg"`
	}
	if err := json.Unmarshal(runnerFrame, &call); err != nil {
		t.Fatalf("decoding runner call: %v", err)
	}
	if call.T != "call" || call.Channel != "nex-agent" || call.Method != "subscribeConversationV4" || call.ID == "" {
		t.Fatalf("runner call = %+v", call)
	}
	// The runner link preserves the Channel RPC argument array verbatim; the
	// runner applies it exactly like ProxyChannel.fromService.
	var forwardedArgs []struct {
		WorkspacePath string `json:"workspacePath"`
		SessionID     string `json:"sessionId"`
	}
	if err := json.Unmarshal(call.Arg, &forwardedArgs); err != nil {
		t.Fatalf("decoding forwarded arg %s: %v", call.Arg, err)
	}
	if len(forwardedArgs) != 1 || forwardedArgs[0].WorkspacePath != workspace || forwardedArgs[0].SessionID != "sess-1" {
		t.Fatalf("forwarded arg = %+v", forwardedArgs)
	}

	// Runner result -> browser PromiseSuccess.
	result, _ := json.Marshal(map[string]any{
		"t":     "result",
		"id":    call.ID,
		"ok":    true,
		"value": map[string]any{"ack": map[string]any{"subscriptionId": "sub-1", "mode": "snapshot", "logEpoch": "e1"}},
	})
	if err := runner.Write(context.Background(), websocket.MessageText, result); err != nil {
		t.Fatalf("writing runner result: %v", err)
	}
	success := browser.readResponse()
	if success.Type != rpcwire.ResponsePromiseSuccess || success.ID != 7 {
		t.Fatalf("browser response = %+v, want PromiseSuccess id=7", success)
	}
	body, ok := success.Body.(json.RawMessage)
	if !ok || !strings.Contains(string(body), `"subscriptionId":"sub-1"`) {
		t.Fatalf("response body = %s", body)
	}

	// Listen: onDynamicConversationFrame forwards to the runner.
	browser.send(rpcwire.RequestEventListen, 11, "nex-agent", "onDynamicConversationFrame", mustRaw(t, `{
		"workspacePath": "`+workspace+`"
	}`))
	_, listenFrame, err := runner.Read(context.Background())
	if err != nil {
		t.Fatalf("runner did not receive listen: %v", err)
	}
	var listen struct {
		T       string `json:"t"`
		ID      string `json:"id"`
		Channel string `json:"channel"`
		Event   string `json:"event"`
	}
	if err := json.Unmarshal(listenFrame, &listen); err != nil {
		t.Fatalf("decoding listen: %v", err)
	}
	if listen.T != "listen" || listen.Channel != "nex-agent" || listen.Event != "onDynamicConversationFrame" {
		t.Fatalf("listen = %+v", listen)
	}

	event, _ := json.Marshal(map[string]any{
		"t":     "event",
		"id":    listen.ID,
		"value": map[string]any{"wireVersion": 3, "kind": "complete"},
	})
	if err := runner.Write(context.Background(), websocket.MessageText, event); err != nil {
		t.Fatalf("writing runner event: %v", err)
	}
	fire := browser.readResponse()
	if fire.Type != rpcwire.ResponseEventFire || fire.ID != 11 {
		t.Fatalf("event response = %+v, want EventFire id=11", fire)
	}

	// Pending call fails with "runner offline" when the runner dies.
	browser.send(rpcwire.RequestPromise, 21, "nex-agent", "subscribeConversationV4", mustRaw(t, `{
		"workspacePath": "`+workspace+`",
		"sessionId": "sess-2"
	}`))
	_, pendingFrame, err := runner.Read(context.Background())
	if err != nil {
		t.Fatalf("runner did not receive pending call: %v", err)
	}
	var pending struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(pendingFrame, &pending)
	if pending.ID == "" {
		t.Fatalf("pending call had no id")
	}
	_ = runner.CloseNow()

	offline := browser.readResponse()
	if offline.Type != rpcwire.ResponsePromiseError || offline.ID != 21 {
		t.Fatalf("offline response = %+v, want PromiseError id=21", offline)
	}
	errorBody, ok := offline.Body.(json.RawMessage)
	if !ok || !strings.Contains(string(errorBody), "runner offline") {
		t.Fatalf("offline error body = %s", errorBody)
	}

	// The routed workspace disappears while the runner is offline.
	request, _ = http.NewRequest(http.MethodGet, h.server.URL+"/api/server-info", nil)
	request.Header.Set("Cookie", "nex_session="+session)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("server-info after disconnect: %v", err)
	}
	var emptyInfo struct {
		Workspaces []map[string]any `json:"workspaces"`
	}
	_ = json.NewDecoder(response.Body).Decode(&emptyInfo)
	_ = response.Body.Close()
	if len(emptyInfo.Workspaces) != 0 {
		t.Fatalf("workspaces after disconnect = %+v, want empty", emptyInfo.Workspaces)
	}

	// Reconnect: every browser listen for the runner is re-issued automatically
	// (nex-task.onDynamicWorkspaceEvent and nex-agent.onDynamicConversationFrame),
	// each with its original listen id.
	runner2 := h.dialRunner(t)
	runnerHello(t, runner2, runnerID, workspace)

	reissued := make(map[string]string)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, reListenFrame, err := runner2.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("reconnected runner did not receive re-listen: %v", err)
		}
		var reListen struct {
			T     string `json:"t"`
			ID    string `json:"id"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal(reListenFrame, &reListen); err != nil {
			t.Fatalf("decoding re-listen: %v", err)
		}
		if reListen.T != "listen" {
			t.Fatalf("re-listen = %+v", reListen)
		}
		reissued[reListen.Event] = reListen.ID
	}
	if reissued["onDynamicConversationFrame"] != listen.ID {
		t.Fatalf("onDynamicConversationFrame re-listen id = %q, want %q", reissued["onDynamicConversationFrame"], listen.ID)
	}
	if reissued["onDynamicWorkspaceEvent"] != taskListen.ID {
		t.Fatalf("onDynamicWorkspaceEvent re-listen id = %q, want %q", reissued["onDynamicWorkspaceEvent"], taskListen.ID)
	}

	// Events flow again after the reconnect.
	event2, _ := json.Marshal(map[string]any{
		"t":     "event",
		"id":    reissued["onDynamicConversationFrame"],
		"value": map[string]any{"wireVersion": 3, "kind": "complete", "reconnected": true},
	})
	if err := runner2.Write(context.Background(), websocket.MessageText, event2); err != nil {
		t.Fatalf("writing event after reconnect: %v", err)
	}
	fire2 := browser.readResponse()
	if fire2.Type != rpcwire.ResponseEventFire || fire2.ID != 11 {
		t.Fatalf("event after reconnect = %+v", fire2)
	}
}

func TestLocalChannelsOverBrowserWebSocket(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	browser := newBrowserClient(t, h.dialBrowser(t, session))

	// setting.get returns defaults.
	response := browser.call(1, "setting", "get", rpcwire.Undefined{})
	if response.Type != rpcwire.ResponsePromiseSuccess {
		t.Fatalf("setting.get = %+v", response)
	}
	body, ok := response.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("setting.get body = %T", response.Body)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("decoding settings: %v", err)
	}
	if string(settings["locale"]) != `"zh-CN"` {
		t.Fatalf("locale = %s", settings["locale"])
	}

	// Unknown event on a known local channel: empty event, no response.
	browser.send(rpcwire.RequestEventListen, 2, "setting", "onNope", rpcwire.Undefined{})

	// nex-agent.helloConversationV4 is answered locally.
	hello := browser.call(3, "nex-agent", "helloConversationV4", rpcwire.Undefined{})
	if hello.Type != rpcwire.ResponsePromiseSuccess || hello.ID != 3 {
		t.Fatalf("hello = %+v", hello)
	}
	helloBody, ok := hello.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("hello body = %T", hello.Body)
	}
	var parsed struct {
		Kind            string `json:"kind"`
		ProtocolVersion int    `json:"protocolVersion"`
		ClientMode      string `json:"clientMode"`
		DeliveryProfile string `json:"deliveryProfile"`
		ConnectionID    string `json:"connectionId"`
	}
	if err := json.Unmarshal(helloBody, &parsed); err != nil {
		t.Fatalf("decoding hello: %v", err)
	}
	if parsed.Kind != "hello" || parsed.ProtocolVersion != 3 ||
		parsed.ClientMode != "desktop-continuous" || parsed.DeliveryProfile != "continuous" || parsed.ConnectionID == "" {
		t.Fatalf("hello = %+v", parsed)
	}

	// Unknown channel rejects immediately with the ChannelServer message.
	unknown := browser.call(4, "does-not-exist", "anything", rpcwire.Undefined{})
	if unknown.Type != rpcwire.ResponsePromiseError || unknown.ID != 4 {
		t.Fatalf("unknown channel response = %+v", unknown)
	}
	errorBody, _ := unknown.Body.(json.RawMessage)
	if !strings.Contains(string(errorBody), "Channel name 'does-not-exist' not found") {
		t.Fatalf("unknown channel error = %s", errorBody)
	}

	// model-selection with no runner connected reports the stable error.
	noRunner := browser.call(5, "model-selection", "getView", rpcwire.Undefined{})
	if noRunner.Type != rpcwire.ResponsePromiseError {
		t.Fatalf("model-selection response = %+v", noRunner)
	}
	noRunnerBody, _ := noRunner.Body.(json.RawMessage)
	if !strings.Contains(string(noRunnerBody), "no runner connected") {
		t.Fatalf("model-selection error = %s", noRunnerBody)
	}
}

func TestRunnerTokenRejected(t *testing.T) {
	h := newHarness(t)
	header := http.Header{"Authorization": []string{"Bearer wrong"}}
	conn, response, err := websocket.Dial(context.Background(), h.server.URL+"/runner", &websocket.DialOptions{
		HTTPHeader: header,
	})
	if conn != nil {
		_ = conn.CloseNow()
		t.Fatalf("dial with a bad token succeeded")
	}
	if err == nil {
		t.Fatal("expected dial error")
	}
	if response != nil && response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func TestDuplicateRunnerNewestWins(t *testing.T) {
	h := newHarness(t)
	first := h.dialRunner(t)
	runnerHello(t, first, runnerID, workspace)

	second := h.dialRunner(t)
	runnerHello(t, second, runnerID, "/workspace/other")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := first.Read(ctx); err == nil {
		t.Fatalf("old runner connection stayed open")
	}

	request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/server-info?token="+authToken, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("server-info: %v", err)
	}
	defer response.Body.Close()
	var info struct {
		Workspaces []map[string]any `json:"workspaces"`
	}
	_ = json.NewDecoder(response.Body).Decode(&info)
	if len(info.Workspaces) != 1 || info.Workspaces[0]["path"] != "/workspace/other" {
		t.Fatalf("workspaces = %+v, want only the newest runner's path", info.Workspaces)
	}
}

func TestDuplicateWorkspacePathRejected(t *testing.T) {
	h := newHarness(t)
	first := h.dialRunner(t)
	runnerHello(t, first, "runner-a", workspace)
	second := h.dialRunner(t)
	runnerHello(t, second, "runner-b", workspace)

	request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/server-info?token="+authToken, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("server-info: %v", err)
	}
	defer response.Body.Close()
	var info struct {
		Workspaces []map[string]any `json:"workspaces"`
	}
	_ = json.NewDecoder(response.Body).Decode(&info)
	if len(info.Workspaces) != 1 || info.Workspaces[0]["label"] != "test-runner: demo" {
		t.Fatalf("workspaces = %+v, want the first owner only", info.Workspaces)
	}

	// A call for the path goes to the first runner.
	browser := newBrowserClient(t, h.dialBrowser(t, h.login(t)))
	browser.send(rpcwire.RequestPromise, 1, "nex-agent", "subscribeConversationV4",
		mustRaw(t, `{"workspacePath":"`+workspace+`","sessionId":"s"}`))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := first.Read(ctx); err != nil {
		t.Fatalf("first runner did not receive the call: %v", err)
	}
}

// A runner disconnect must fail each pending call exactly once: the socket
// close path and the registry unregister path both observe the disconnect.
func TestRunnerDisconnectFailsPendingCallOnce(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	runner := h.dialRunner(t)
	runnerHello(t, runner, runnerID, workspace)
	browser := newBrowserClient(t, h.dialBrowser(t, session))

	browser.send(rpcwire.RequestPromise, 31, "nex-agent", "subscribeConversationV4",
		mustRaw(t, `{"workspacePath":"`+workspace+`","sessionId":"s"}`))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := runner.Read(ctx); err != nil {
		t.Fatalf("runner did not receive the call: %v", err)
	}
	_ = runner.CloseNow()

	if offline := browser.readResponse(); offline.Type != rpcwire.ResponsePromiseError || offline.ID != 31 {
		t.Fatalf("first response = %+v", offline)
	}
	// No duplicate error and no late success for the same id.
	readCtx, cancelRead := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancelRead()
	if _, data, err := browser.conn.Read(readCtx); err == nil {
		frame, _, decodeErr := rpcwire.ReadFrame(data, 0)
		if decodeErr == nil {
			if response, err := rpcwire.DecodeResponse(frame.Payload); err == nil && response.ID == 31 {
				t.Fatalf("duplicate response for the failed call: %+v", response)
			}
		}
	}
}

// A browser that unsubscribes must send unlisten to the runner for that exact
// listen id; otherwise the runner keeps projecting snapshots forever.
func TestBrowserDisposeSendsUnlistenToRunner(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	runner := h.dialRunner(t)
	runnerHello(t, runner, runnerID, workspace)
	browser := newBrowserClient(t, h.dialBrowser(t, session))

	browser.send(rpcwire.RequestEventListen, 41, "nex-agent", "onDynamicConversationFrame",
		mustRaw(t, `{"workspacePath":"`+workspace+`"}`))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, listenFrame, err := runner.Read(ctx)
	if err != nil {
		t.Fatalf("runner did not receive listen: %v", err)
	}
	var listen struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(listenFrame, &listen); err != nil {
		t.Fatalf("decode listen: %v", err)
	}

	dispose, err := rpcwire.EncodeEventDispose(41)
	if err != nil {
		t.Fatalf("encode dispose: %v", err)
	}
	if err := browser.conn.Write(context.Background(), websocket.MessageBinary,
		rpcwire.EncodeFrame(rpcwire.Frame{Type: rpcwire.MessageRegular, Payload: dispose})); err != nil {
		t.Fatalf("write dispose: %v", err)
	}

	unlistenCtx, cancelUnlisten := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelUnlisten()
	_, unlistenFrame, err := runner.Read(unlistenCtx)
	if err != nil {
		t.Fatalf("runner did not receive unlisten: %v", err)
	}
	var unlisten struct {
		T  string `json:"t"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(unlistenFrame, &unlisten); err != nil {
		t.Fatalf("decode unlisten: %v", err)
	}
	if unlisten.T != "unlisten" || unlisten.ID != listen.ID {
		t.Fatalf("unlisten = %+v, want unlisten %s", unlisten, listen.ID)
	}
}
