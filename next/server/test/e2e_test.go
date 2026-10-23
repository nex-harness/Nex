package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tylinux/nex-next/server/internal/app"
	"github.com/tylinux/nex-next/server/internal/configstore"
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
	runnerHelloNamed(t, conn, id, "test-runner", "darwin", path)
}

func runnerHelloNamed(t *testing.T, conn *websocket.Conn, id, name, runnerOS, path string) {
	t.Helper()
	hello, _ := json.Marshal(map[string]any{
		"t":        "hello",
		"runnerId": id,
		"name":     name,
		"version":  "0.1.0",
		"os":       runnerOS,
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

// readRunnerFrameSkewed reads the next runner frame while skipping config
// pushes. A push arrives right after welcome whenever the server config defines
// providers, and again after every edit, so it must never be mistaken for the
// reply a test is waiting for.
func readRunnerFrameSkewed(t *testing.T, conn *websocket.Conn) (int, []byte) {
	t.Helper()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		typ, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("reading runner frame: %v", err)
		}
		var probe struct {
			T       string `json:"t"`
			Version uint64 `json:"version"`
		}
		if json.Unmarshal(data, &probe) == nil && probe.T == "config" {
			continue
		}
		return int(typ), data
	}
}

// browserClient encodes/decodes Channel RPC frames over a WS connection.
type browserClient struct {
	t    *testing.T
	conn *websocket.Conn
}

func newBrowserClient(t *testing.T, conn *websocket.Conn) *browserClient {
	t.Helper()
	// The real web client raises this limit too; a provider view with models can
	// exceed the library default in a long test process.
	conn.SetReadLimit(32 << 20)
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

func identityFor(runnerID, path string) string {
	return "runner:" + runnerID + ":" + path
}

func TestEndToEndRoutingAndReconnect(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)

	runner := h.dialRunner(t)
	runnerHello(t, runner, runnerID, workspace)

	// server-info advertises the workspace identity and runner metadata; the
	// label is "<runnerName>: <basename>".
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
	if info.Workspaces[0]["workspaceIdentity"] != "runner:"+runnerID+":"+workspace {
		t.Fatalf("workspaceIdentity = %v", info.Workspaces[0]["workspaceIdentity"])
	}
	if info.Workspaces[0]["runnerId"] != runnerID {
		t.Fatalf("runnerId = %v", info.Workspaces[0]["runnerId"])
	}
	if info.Workspaces[0]["runnerName"] != "test-runner" {
		t.Fatalf("runnerName = %v", info.Workspaces[0]["runnerName"])
	}
	if info.Workspaces[0]["runnerOs"] != "darwin" {
		t.Fatalf("runnerOs = %v", info.Workspaces[0]["runnerOs"])
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

	// model-selection is answered by the server, never forwarded: the server
	// compiles the view from its config doc plus runner facts (fallback mode: the
	// catalog this runner reported in configApplied). This fake runner reports
	// no facts, so the contract outcome is the stable "no runner connected"
	// error the UI retries — a forwarded call would stay unanswered here.
	browser.send(rpcwire.RequestPromise, 2, "model-selection", "getView", mustRaw(t, `[{"selection":null}]`))
	modelResponse := browser.readResponse()
	if modelResponse.ID != 2 {
		t.Fatalf("model-selection response id = %+v", modelResponse)
	}
	switch modelResponse.Type {
	case rpcwire.ResponsePromiseError:
		modelBody, _ := modelResponse.Body.(json.RawMessage)
		if !strings.Contains(string(modelBody), "no runner connected") {
			t.Fatalf("model-selection error = %s", modelBody)
		}
	case rpcwire.ResponsePromiseSuccess:
		// A server-owned doc (another test in this process may have configured
		// one) answers with a view instead.
		if _, ok := responseObject(t, modelResponse)["providers"].([]any); !ok {
			t.Fatalf("model-selection view has no providers field: %+v", modelResponse.Body)
		}
	default:
		t.Fatalf("model-selection response = %+v", modelResponse)
	}

	// nex-task list + dynamic listen route by workspacePath.
	browser.send(rpcwire.RequestPromise, 3, "nex-task", "listTasks", mustRaw(t, `[{"workspacePath":"`+workspace+`"}]`))
	_, taskFrame := readRunnerFrameSkewed(t, runner)
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
	_, taskListenFrame := readRunnerFrameSkewed(t, runner)
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
		"workspaceIdentity": "`+identityFor(runnerID, workspace)+`"
	}]`))

	_, runnerFrame := readRunnerFrameSkewed(t, runner)
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
	_, listenFrame := readRunnerFrameSkewed(t, runner)
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
	_, pendingFrame := readRunnerFrameSkewed(t, runner)
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

	// model-selection (no runner connected): the server config of this process
	// may already own providers (tests share one process), so accept both
	// contract outcomes: the stable "no runner connected" error (fallback, no
	// facts to compile) or a successful view. Never a transport failure.
	noRunner := browser.call(5, "model-selection", "getView", rpcwire.Undefined{})
	switch noRunner.Type {
	case rpcwire.ResponsePromiseError:
		noRunnerBody, _ := noRunner.Body.(json.RawMessage)
		if !strings.Contains(string(noRunnerBody), "no runner connected") {
			t.Fatalf("model-selection error = %s", noRunnerBody)
		}
	case rpcwire.ResponsePromiseSuccess:
		if _, ok := responseObject(t, noRunner)["providers"].([]any); !ok {
			t.Fatalf("model-selection view has no providers field: %+v", noRunner.Body)
		}
	default:
		t.Fatalf("model-selection response = %+v %v", noRunner, noRunner.Body)
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
	for {
		_, data, err := first.Read(ctx)
		if err != nil {
			break
		}
		var frame rawRunnerFrame
		if json.Unmarshal(data, &frame) == nil && frame.T == "config" {
			continue
		}
		t.Fatalf("old runner connection stayed open: %s", data)
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

// Two runners serving the same path are both listed with distinct identities,
// and calls/listens reach only the runner named by workspaceIdentity. A path
// call without identity is ambiguous; an offline identity fails fast.
func TestTwoRunnersSamePathIdentityRouting(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)

	alpha := h.dialRunner(t)
	runnerHelloNamed(t, alpha, "alpha", "alpha-box", "linux", workspace)
	beta := h.dialRunner(t)
	runnerHelloNamed(t, beta, "beta", "win-box", "win32", workspace)

	// Both machines are listed, same path, distinct identities, labels from
	// the runner name.
	request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/server-info", nil)
	request.Header.Set("Cookie", "nex_session="+session)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("server-info: %v", err)
	}
	defer response.Body.Close()
	var info struct {
		Workspaces []struct {
			Path              string `json:"path"`
			Label             string `json:"label"`
			WorkspaceIdentity string `json:"workspaceIdentity"`
			RunnerID          string `json:"runnerId"`
			RunnerName        string `json:"runnerName"`
			RunnerOS          string `json:"runnerOs"`
		} `json:"workspaces"`
	}
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatalf("decoding server-info: %v", err)
	}
	if len(info.Workspaces) != 2 {
		t.Fatalf("workspaces = %+v, want both runners", info.Workspaces)
	}
	want := []struct {
		identity   string
		runnerID   string
		runnerName string
		runnerOS   string
		label      string
	}{
		{identityFor("alpha", workspace), "alpha", "alpha-box", "linux", "alpha-box: demo"},
		{identityFor("beta", workspace), "beta", "win-box", "win32", "win-box: demo"},
	}
	for index, expected := range want {
		row := info.Workspaces[index]
		if row.Path != workspace || row.WorkspaceIdentity != expected.identity ||
			row.RunnerID != expected.runnerID || row.RunnerName != expected.runnerName ||
			row.RunnerOS != expected.runnerOS || row.Label != expected.label {
			t.Fatalf("workspaces[%d] = %+v, want %+v", index, row, expected)
		}
	}

	// recentProjects is path-only: the shared path appears once.
	browser := newBrowserClient(t, h.dialBrowser(t, session))
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
		t.Fatalf("recentProjects = %v, want the deduplicated path", settings.RecentProjects)
	}

	// Identity call routes to beta only.
	browser.send(rpcwire.RequestPromise, 2, "nex-task", "listTasks", mustRaw(t, `[{
		"workspacePath": "`+workspace+`",
		"workspaceIdentity": "`+identityFor("beta", workspace)+`"
	}]`))
	betaStream := newRunnerStream(t, beta)
	betaCall := betaStream.next(t, 5*time.Second)
	if betaCall.Channel != "nex-task" || betaCall.Method != "listTasks" {
		t.Fatalf("beta call = %+v", betaCall)
	}
	if err := beta.Write(context.Background(), websocket.MessageText,
		callResultFrame(t, betaCall.ID, []any{})); err != nil {
		t.Fatalf("writing beta result: %v", err)
	}
	if reply := browser.readResponse(); reply.Type != rpcwire.ResponsePromiseSuccess || reply.ID != 2 {
		t.Fatalf("beta reply = %+v", reply)
	}
	alphaStream := newRunnerStream(t, alpha)
	alphaStream.expectNone(t, 300*time.Millisecond)

	// A listen for alpha's identity must reach only alpha.
	browser.send(rpcwire.RequestEventListen, 3, "nex-task", "onDynamicWorkspaceEvent", mustRaw(t, `[{
		"workspacePath": "`+workspace+`",
		"workspaceIdentity": "`+identityFor("alpha", workspace)+`"
	}]`))
	alphaListen := alphaStream.next(t, 5*time.Second)
	if alphaListen.T != "listen" || alphaListen.Event != "onDynamicWorkspaceEvent" {
		t.Fatalf("alpha listen = %+v", alphaListen)
	}
	betaStream.expectNone(t, 300*time.Millisecond)

	event, _ := json.Marshal(map[string]any{
		"t": "event", "id": alphaListen.ID,
		"value": map[string]any{"type": "workspace_task_list_changed", "workspacePath": workspace},
	})
	if err := alpha.Write(context.Background(), websocket.MessageText, event); err != nil {
		t.Fatalf("writing alpha event: %v", err)
	}
	if fired := browser.readResponse(); fired.Type != rpcwire.ResponseEventFire || fired.ID != 3 {
		t.Fatalf("alpha event = %+v", fired)
	}

	// A path-only call is ambiguous.
	browser.send(rpcwire.RequestPromise, 4, "nex-task", "listTasks", mustRaw(t, `[{"workspacePath":"`+workspace+`"}]`))
	ambiguous := browser.readResponse()
	if ambiguous.Type != rpcwire.ResponsePromiseError || ambiguous.ID != 4 {
		t.Fatalf("ambiguous response = %+v", ambiguous)
	}
	ambiguousBody, _ := ambiguous.Body.(json.RawMessage)
	if !strings.Contains(string(ambiguousBody), "ambiguous workspace path; workspaceIdentity required") {
		t.Fatalf("ambiguous error = %s", ambiguousBody)
	}
	alphaStream.expectNone(t, 300*time.Millisecond)
	betaStream.expectNone(t, 100*time.Millisecond)

	// A beta identity listen is bound before the disconnect; it must be
	// re-issued to beta's next connection only.
	browser.send(rpcwire.RequestEventListen, 7, "nex-agent", "onDynamicConversationFrame", mustRaw(t, `[{
		"workspacePath": "`+workspace+`",
		"workspaceIdentity": "`+identityFor("beta", workspace)+`"
	}]`))
	betaBoundListen := betaStream.next(t, 5*time.Second)
	if betaBoundListen.T != "listen" || betaBoundListen.Event != "onDynamicConversationFrame" {
		t.Fatalf("beta bound listen = %+v", betaBoundListen)
	}
	alphaStream.expectNone(t, 300*time.Millisecond)

	// Disconnect beta: its identity fails fast with "runner offline" and never
	// falls back to alpha.
	_ = beta.CloseNow()
	waitForRunnerGone(t, h, session, "beta")
	browser.send(rpcwire.RequestPromise, 5, "nex-task", "listTasks", mustRaw(t, `[{
		"workspacePath": "`+workspace+`",
		"workspaceIdentity": "`+identityFor("beta", workspace)+`"
	}]`))
	offline := browser.readResponse()
	if offline.Type != rpcwire.ResponsePromiseError || offline.ID != 5 {
		t.Fatalf("offline response = %+v", offline)
	}
	offlineBody, _ := offline.Body.(json.RawMessage)
	if !strings.Contains(string(offlineBody), "runner offline") {
		t.Fatalf("offline error = %s", offlineBody)
	}
	alphaStream.expectNone(t, 300*time.Millisecond)

	// beta reconnects: its identity listen is re-issued with the original listen
	// id, alpha receives nothing, and an event for it reaches the browser.
	beta2 := h.dialRunner(t)
	runnerHelloNamed(t, beta2, "beta", "win-box", "win32", workspace)
	beta2Stream := newRunnerStream(t, beta2)
	reissued := beta2Stream.next(t, 5*time.Second)
	if reissued.T != "listen" || reissued.ID != betaBoundListen.ID || reissued.Event != "onDynamicConversationFrame" {
		t.Fatalf("beta re-listen = %+v, want id %s", reissued, betaBoundListen.ID)
	}
	alphaStream.expectNone(t, 300*time.Millisecond)

	event2, _ := json.Marshal(map[string]any{
		"t": "event", "id": reissued.ID,
		"value": map[string]any{"wireVersion": 3, "kind": "complete", "reconnected": true},
	})
	if err := beta2.Write(context.Background(), websocket.MessageText, event2); err != nil {
		t.Fatalf("writing beta reconnected event: %v", err)
	}
	if fired := browser.readResponse(); fired.Type != rpcwire.ResponseEventFire || fired.ID != 7 {
		t.Fatalf("beta reconnected event = %+v", fired)
	}

	// A fresh beta-identity listen routes to the reconnected beta only.
	browser.send(rpcwire.RequestEventListen, 6, "nex-agent", "onDynamicConversationFrame", mustRaw(t, `[{
		"workspacePath": "`+workspace+`",
		"workspaceIdentity": "`+identityFor("beta", workspace)+`"
	}]`))
	betaListen := beta2Stream.next(t, 5*time.Second)
	if betaListen.T != "listen" || betaListen.Event != "onDynamicConversationFrame" {
		t.Fatalf("beta listen = %+v", betaListen)
	}
	alphaStream.expectNone(t, 300*time.Millisecond)
}

type rawRunnerFrame struct {
	T       string `json:"t"`
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Method  string `json:"method"`
	Event   string `json:"event"`
}

// runnerStream reads one runner connection continuously so "no frame arrived"
// can be asserted without a read deadline (coder/websocket closes the
// connection when a Read's context is canceled).
type runnerStream struct {
	frames chan rawRunnerFrame
	done   chan struct{}
}

func newRunnerStream(t *testing.T, conn *websocket.Conn) *runnerStream {
	t.Helper()
	stream := &runnerStream{
		frames: make(chan rawRunnerFrame, 64),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(stream.frames)
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var frame rawRunnerFrame
			if err := json.Unmarshal(data, &frame); err != nil {
				continue
			}
			select {
			case stream.frames <- frame:
			case <-stream.done:
				return
			}
		}
	}()
	t.Cleanup(func() { close(stream.done) })
	return stream
}

func (s *runnerStream) next(t *testing.T, timeout time.Duration) rawRunnerFrame {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case frame, ok := <-s.frames:
			if !ok {
				t.Fatal("runner stream closed while waiting for a frame")
			}
			// Config pushes arrive after welcome and after every server edit;
			// tests waiting for a call/listen must never see them.
			if frame.T == "config" {
				continue
			}
			return frame
		case <-timer.C:
			t.Fatal("runner did not receive a frame")
			return rawRunnerFrame{}
		}
	}
}

func (s *runnerStream) expectNone(t *testing.T, wait time.Duration) {
	t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case frame, ok := <-s.frames:
			if !ok {
				return
			}
			if frame.T == "config" {
				continue
			}
			t.Fatalf("unexpected runner frame: %+v", frame)
		case <-timer.C:
			return
		}
	}
}

func callResultFrame(t *testing.T, id string, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"t": "result", "id": id, "ok": true, "value": value})
	if err != nil {
		t.Fatalf("encoding result: %v", err)
	}
	return encoded
}

func waitForRunnerGone(t *testing.T, h *harness, session, runnerID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/server-info", nil)
		request.Header.Set("Cookie", "nex_session="+session)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("server-info: %v", err)
		}
		var info struct {
			Workspaces []struct {
				RunnerID string `json:"runnerId"`
			} `json:"workspaces"`
		}
		_ = json.NewDecoder(response.Body).Decode(&info)
		_ = response.Body.Close()
		found := false
		for _, workspace := range info.Workspaces {
			if workspace.RunnerID == runnerID {
				found = true
			}
		}
		if !found {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("runner %s still listed after disconnect", runnerID)
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
	_, listenFrame := readRunnerFrameSkewed(t, runner)
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
	_, unlistenFrame := readRunnerFrameSkewed(t, runner)
	_ = unlistenCtx
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

// ── server-owned config (docs/next/config-mcp-skills.md P0) ──

// readRunnerFrame waits for the next text frame from the server.
func readRunnerFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("reading runner frame: %v", err)
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decoding runner frame: %v", err)
	}
	return frame
}

func runnerHelloWithVersion(t *testing.T, conn *websocket.Conn, id, path string, lastApplied uint64) {
	t.Helper()
	hello, _ := json.Marshal(map[string]any{
		"t":                  "hello",
		"runnerId":           id,
		"name":               "test-runner",
		"version":            "0.1.0",
		"os":                 "darwin",
		"arch":               "arm64",
		"lastAppliedVersion": lastApplied,
		"workspaces":         []map[string]string{{"path": path, "label": "demo"}},
	})
	if err := conn.Write(context.Background(), websocket.MessageText, hello); err != nil {
		t.Fatalf("writing hello: %v", err)
	}
	welcome := readRunnerWelcome(t, conn)
	if welcome != "welcome" {
		t.Fatalf("first runner frame = %q, want welcome", welcome)
	}
}

// readRunnerWelcome reads until the welcome frame. Config pushes that follow it
// belong to the push tests (readRunnerFrameSkewed); other tests never see one
// because each harness starts with an empty config doc.
func readRunnerWelcome(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	_, data, err := conn.Read(context.Background())
	if err != nil {
		t.Fatalf("reading welcome: %v", err)
	}
	var frame struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("decoding welcome: %v", err)
	}
	return frame.T
}

// writeConfigApplied answers one `config` frame with facts.
func writeConfigApplied(t *testing.T, conn *websocket.Conn, version uint64, ok bool, providerID string, models []map[string]any) {
	t.Helper()
	ack, _ := json.Marshal(map[string]any{
		"t":       "configApplied",
		"version": version,
		"ok":      ok,
		"errors":  []any{},
		"facts": map[string]any{
			"modelsByProvider": map[string]any{
				providerID: map[string]any{"configured": true, "models": models},
			},
		},
	})
	if err := conn.Write(context.Background(), websocket.MessageText, ack); err != nil {
		t.Fatalf("writing configApplied: %v", err)
	}
}

// pushVersion decodes the version field of a `config` frame.
func pushVersion(t *testing.T, frame map[string]json.RawMessage) (uint64, map[string]any) {
	t.Helper()
	var version uint64
	encoded, _ := json.Marshal(frame["version"])
	if err := json.Unmarshal(encoded, &version); err != nil {
		t.Fatalf("push version: %v", err)
	}
	raw, _ := json.Marshal(frame)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding push: %v", err)
	}
	return version, decoded
}

// latestPush drains `config` frames until none arrives for a short quiet
// period and returns the newest one.

// awaitApplied waits until the server recorded facts for providerID.
func awaitApplied(t *testing.T, h *harness, providerID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, ok := h.app.Config.Applied(runnerID)
		if ok && state.Facts != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no facts recorded for %s", providerID)
}

// drainConfigFrames consumes every config frame already queued on conn. It is
// not a wait: it is called after browser-driven batches so no push is left
// padding a later read. The last read may time out; callers also stop reading
// that connection afterwards or immediately re-read once the server pushes.
func drainConfigFrames(t *testing.T, conn *websocket.Conn) []uint64 {
	t.Helper()
	return configFramesAfter(t, conn, 150*time.Millisecond)
}

// configFramesAfter reads config frames until no frame arrives within timeout.
//
// A coder/websocket Read cannot be resumed after its deadline expires (the
// connection closes), so a timed-out read ends this connection for the test.
// Every assertion that leaves the drained connection needing another read
// dials a fresh runner and uses hello.lastAppliedVersion instead.
func configFramesAfter(t *testing.T, conn *websocket.Conn, timeout time.Duration) []uint64 {
	t.Helper()
	var versions []uint64
	for {
		timeoutCtx := timeout
		if len(versions) == 0 {
			timeoutCtx = 5 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeoutCtx)
		_, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			return versions
		}
		var frame map[string]json.RawMessage
		if json.Unmarshal(data, &frame) != nil || string(frame["t"]) != `"config"` {
			continue
		}
		var version uint64
		encoded, _ := json.Marshal(frame["version"])
		_ = json.Unmarshal(encoded, &version)
		versions = append(versions, version)
	}
}

// readPushedVersion reads the config push burst: one frame, then a short
// bounded read for any copy still queued (a burst is racing its own writes).
// Each read carries its own deadline, so it must be the ONLY reader on this
// connection while it runs; the last one may time out, which is fine because
// the test closes its sockets right after.
func readPushedVersion(t *testing.T, conn *websocket.Conn, quiet time.Duration) (uint64, map[string]any) {
	t.Helper()
	read := func(timeout time.Duration) (map[string]json.RawMessage, error) {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			_, data, err := conn.Read(ctx)
			cancel()
			if err != nil {
				return nil, err
			}
			var frame map[string]json.RawMessage
			if json.Unmarshal(data, &frame) != nil || string(frame["t"]) != `"config"` {
				continue
			}
			return frame, nil
		}
	}
	frame, err := read(5 * time.Second)
	if err != nil {

		t.Fatalf("no config push received: %v", err)
	}
	version, push := pushVersion(t, frame)
	// Bounded catch-up: only frames already being written arrive in time.
	for {
		next, err := read(quiet)
		if err != nil {
			return version, push
		}
		nextVersion, nextPush := pushVersion(t, next)
		if nextVersion >= version {
			version, push = nextVersion, nextPush
		}
	}
}

// awaitCall polls a browser call until `accept` returns true.
func (c *browserClient) awaitCall(id uint32, channel, method string, arg rpcwire.Value, accept func(rpcwire.Response) bool) rpcwire.Response {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last rpcwire.Response
	for time.Now().Before(deadline) {
		last = c.call(id, channel, method, arg)
		if accept(last) {
			return last
		}
		time.Sleep(40 * time.Millisecond)
	}
	return last
}

// responseObject decodes a successful response body into a generic object.
// providersOf extracts the provider rows of a model-selection view.
func providersOf(view map[string]any) []map[string]any {
	rows, _ := view["providers"].([]any)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if entry, ok := row.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

func responseObject(t *testing.T, response rpcwire.Response) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(response.Body)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decoding response body: %v (%s)", err, raw)
	}
	return object
}

// jsArg encodes a Go object as the browser's JSON fallback argument value.
func jsArg(t *testing.T, value any) rpcwire.Value {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding argument: %v", err)
	}
	return json.RawMessage(encoded)
}

// createE2EProvider creates a provider with one model and a key through the
// browser channel (the same calls the Settings UI makes).
func createE2EProvider(t *testing.T, browser *browserClient) string {
	t.Helper()
	created := browser.call(100, "provider-settings", "createPersonalProvider",
		jsArg(t, []any{map[string]any{"providerName": "E2E Provider"}}))
	if created.Type != rpcwire.ResponsePromiseSuccess {
		t.Fatalf("createPersonalProvider = %+v %v", created, created.Body)
	}
	createdBody := responseObject(t, created)
	providerID, _ := createdBody["providerId"].(string)
	if providerID == "" {
		t.Fatalf("createPersonalProvider returned no providerId: %+v", createdBody)
	}
	overlay := browser.call(101, "provider-settings", "savePersonalProviderOverlay", jsArg(t, []any{
		providerID,
		map[string]any{
			"group":  "standard-personal",
			"access": map[string]any{"type": "api-key", "apiKey": "e2e-key"},
			"api":    map[string]any{"type": "openai-chat-completions", "baseUrl": "https://fake.test/v1"},
		},
		map[string]any{},
	}))
	if overlay.Type != rpcwire.ResponsePromiseSuccess {
		t.Fatalf("savePersonalProviderOverlay = %+v %v", overlay, overlay.Body)
	}
	overlayView := responseObject(t, overlay)
	overlayProviders, _ := overlayView["providers"].([]any)
	var overlayProvider map[string]any
	for _, candidate := range overlayProviders {
		provider, _ := candidate.(map[string]any)
		if provider["providerId"] == providerID {
			overlayProvider = provider
		}
	}
	if overlayProvider == nil {
		t.Fatalf("overlay view has no %s: %+v", providerID, overlayView)
	}
	overlayEffective, _ := overlayProvider["effectiveConfig"].(map[string]any)
	overlayAPI, _ := overlayEffective["api"].(map[string]any)
	if overlayAPI["type"] != "openai-chat-completions" {
		t.Fatalf("overlay api type = %v", overlayAPI["type"])
	}

	modelConfig := map[string]any{
		"enabled": true,
		"name":    "E2E Model",
		"properties": map[string]any{
			"contextWindow": 64000,
			"inputFormat":   map[string]any{"supportsText": true, "supportsImage": false},
		},
		"optionSpecs": map[string]any{
			"reasoningLevel":  map[string]any{"values": []string{"off"}, "map": `{"reasoning_effort": reasoningLevel}`},
			"maxOutputTokens": map[string]any{"max": 4096, "map": `{"max_tokens": maxOutputTokens}`},
		},
	}
	added := browser.call(102, "provider-settings", "addPersonalModel", jsArg(t, []any{providerID, "e2e-model", modelConfig, false}))
	if added.Type != rpcwire.ResponsePromiseSuccess {
		t.Fatalf("addPersonalModel = %+v %v", added, added.Body)
	}
	enabled := browser.call(103, "provider-settings", "savePersonalProviderOverlay", jsArg(t, []any{
		providerID,
		map[string]any{},
		map[string]any{"enabled": true},
	}))
	if enabled.Type != rpcwire.ResponsePromiseSuccess {
		t.Fatalf("enable overlay = %+v %v", enabled, enabled.Body)
	}
	return providerID
}

func TestConfigPushOnEditAndStaleReconnect(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	runner := h.dialRunner(t)
	runnerHelloWithVersion(t, runner, runnerID, workspace, 0)
	browser := newBrowserClient(t, h.dialBrowser(t, session))

	providerID := createE2EProvider(t, browser)
	burst := drainConfigFrames(t, runner)
	if len(burst) == 0 {
		t.Fatalf("no config push after provider creation")
	}
	firstVersion := burst[len(burst)-1]
	// The doc alone answers the picker: the key is provisioned, so the model is
	// executable before (or without) the runner's facts.
	selection := browser.call(110, "model-selection", "getView",
		jsArg(t, []any{map[string]any{"selection": map[string]any{
			"providerId": providerID, "modelId": "e2e-model",
			"options": map[string]any{"reasoningLevel": "off"},
		}}}))
	if selection.Type != rpcwire.ResponsePromiseSuccess {
		t.Fatalf("model-selection.getView = %+v %v", selection, selection.Body)
	}
	view := responseObject(t, selection)
	effective, _ := view["effectiveSelection"].(map[string]any)
	if effective["providerId"] != providerID || effective["modelId"] != "e2e-model" {
		t.Fatalf("effectiveSelection = %+v", view)
	}
	level, _ := effective["options"].(map[string]any)
	if level["reasoningLevel"] != "off" {
		t.Fatalf("effective reasoning level = %+v", effective)
	}
	if _, hasIssue := view["selectionIssue"]; hasIssue {
		t.Fatalf("valid selection carries issue: %+v", view)
	}
	// An unsupported level is reported without dropping the catalog.
	invalid := browser.call(111, "model-selection", "getView",
		jsArg(t, []any{map[string]any{"selection": map[string]any{
			"providerId": providerID, "modelId": "e2e-model",
			"options": map[string]any{"reasoningLevel": "high"},
		}}}))
	invalidView := responseObject(t, invalid)
	if invalidView["selectionIssue"] != "reasoning-level-not-supported" {
		t.Fatalf("invalid selection = %+v", invalidView)
	}

	// A second edit pushes a newer version on the same connection.
	disabled := browser.call(112, "provider-settings", "setPersonalModelEnabled", jsArg(t, []any{providerID, "e2e-model", false}))
	if disabled.Type != rpcwire.ResponsePromiseSuccess {
		t.Fatalf("setPersonalModelEnabled = %+v", disabled)
	}
	// The edit must produce a newer version than what the first connection saw.
	latestDoc, err := h.app.Config.Store().Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if latestDoc.Version <= firstVersion {
		t.Fatalf("disable did not bump the doc: first=%d now=%d", firstVersion, latestDoc.Version)
	}
	secondVersion := latestDoc.Version
	// The model is disabled in the desired doc, so it leaves the picker even
	// before the runner acks.
	afterDisable := browser.call(113, "model-selection", "getView", rpcwire.Undefined{})
	afterView := responseObject(t, afterDisable)
	for _, candidate := range providersOf(afterView) {
		if candidate["providerId"] == providerID {
			t.Fatalf("disabled model still listed for %s: %+v", providerID, candidate)
		}
	}

	// The push snapshot is the desired doc for this runner (no secrets inline).
	currentDoc, err := h.app.Config.Store().Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if currentDoc.Version != secondVersion {
		t.Fatalf("drained push %d is not the latest doc version %d", secondVersion, currentDoc.Version)
	}
	push, _, err := h.app.Config.Store().ProjectPush(runnerID)
	if err != nil {
		t.Fatalf("project push: %v", err)
	}
	if !push.Doc.Owned {
		t.Fatalf("push doc not owned: %+v", push.Doc)
	}
	var entry *configstore.Provider
	for index := range push.Doc.Providers {
		if push.Doc.Providers[index].ID == providerID {
			entry = &push.Doc.Providers[index]
		}
	}
	if entry == nil {
		t.Fatalf("push has no %s: %+v", providerID, push.Doc.Providers)
	}
	if !entry.AuthConfigured || entry.APIKey != "" {
		t.Fatalf("push provider = %+v", entry)
	}
	if push.Secrets["sec_prov_"+providerID+"_apikey"] != "e2e-key" {
		t.Fatalf("push secrets = %+v", len(push.Secrets))
	}
	if len(push.Bundles) != 0 {
		t.Fatalf("push bundles = %+v", push.Bundles)
	}

	// Reconnect with a stale applied version: the server pushes immediately.
	staleRunner := h.dialRunner(t)
	runnerHelloWithVersion(t, staleRunner, runnerID, workspace, 1)
	staleVersion := configFramesAfter(t, staleRunner, 3*time.Second)
	if len(staleVersion) == 0 || staleVersion[len(staleVersion)-1] != secondVersion {
		t.Fatalf("stale push versions = %v, want latest %d", staleVersion, secondVersion)
	}

	// Reconnect up to date: no push.
	currentRunner := h.dialRunner(t)
	runnerHelloWithVersion(t, currentRunner, runnerID, workspace, secondVersion)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, _, err := currentRunner.Read(ctx); err == nil {
		t.Fatal("up-to-date runner received an unexpected frame")
	}
}

func TestProviderSettingsViewsAndSecretHandling(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	runner := h.dialRunner(t)
	runnerHelloWithVersion(t, runner, runnerID, workspace, 0)
	browser := newBrowserClient(t, h.dialBrowser(t, session))
	providerID := createE2EProvider(t, browser)

	viewResponse := browser.call(120, "provider-settings", "getView", []any{})
	view := responseObject(t, viewResponse)
	providers, _ := view["providers"].([]any)
	var provider map[string]any
	for _, candidate := range providers {
		entry, _ := candidate.(map[string]any)
		if entry["providerId"] == providerID {
			provider = entry
		}
	}
	if provider == nil {
		t.Fatalf("view has no %s: %+v", providerID, view)
	}
	if provider["providerName"] != "E2E Provider" || provider["enabled"] != true {
		t.Fatalf("provider = %+v", provider)
	}
	effective, _ := provider["effectiveConfig"].(map[string]any)
	access, _ := effective["access"].(map[string]any)
	if access["apiKey"] != "••••-key" {
		t.Fatalf("masked hint = %v", access["apiKey"])
	}
	models, _ := provider["models"].([]any)
	model, _ := models[0].(map[string]any)
	modelConfig, _ := model["effectiveConfig"].(map[string]any)
	specs, _ := modelConfig["optionSpecs"].(map[string]any)
	levels, _ := specs["reasoningLevel"].(map[string]any)["values"].([]any)
	if len(levels) == 0 {
		t.Fatal("reasoningLevel.values is empty")
	}

	// Nothing on disk carries the raw key. The config dir is the store root:
	// config.json lives in it directly, secrets and the key under config/keys/.
	configRoot := h.app.Config.Store().ConfigDir()
	configText := mustReadFile(t, filepath.Join(configRoot, "config.json"))
	if strings.Contains(configText, "e2e-key") {
		t.Fatal("config.json contains the raw key")
	}
	if strings.Contains(mustReadFile(t, filepath.Join(configRoot, "config", "secrets.enc")), "e2e-key") {
		t.Fatal("secrets.enc contains the raw key")
	}
	keyInfo, err := os.Stat(filepath.Join(configRoot, "keys", "config.key"))
	if err != nil || keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("key file = %v %v", keyInfo, err)
	}

	// Unsupported methods fail with a clear error instead of hanging.
	remote := browser.call(121, "provider-settings", "listRemoteModels", jsArg(t, []any{providerID}))
	if remote.Type != rpcwire.ResponsePromiseError {
		t.Fatalf("listRemoteModels = %+v", remote)
	}
	body, _ := remote.Body.(json.RawMessage)
	if !strings.Contains(string(body), "not available") {
		t.Fatalf("listRemoteModels error = %s", body)
	}

	// resolveModelConfig echoes the same complete model config.
	resolved := browser.call(122, "provider-settings", "resolveModelConfig", jsArg(t, []any{
		map[string]any{"providerId": providerID, "modelId": "e2e-model"},
	}))
	if resolved.Type != rpcwire.ResponsePromiseSuccess {
		t.Fatalf("resolveModelConfig = %+v", resolved)
	}
}

func TestImportRunnerEndpoint(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	runner := h.dialRunner(t)
	runnerHelloWithVersion(t, runner, runnerID, workspace, 0)
	go func() {
		// The export call follows the welcome; any config push queued in
		// between (proactive push of a reset doc) is skipped.
		var frame map[string]json.RawMessage
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			frame = readRunnerFrame(t, runner, 5*time.Second)
			if string(frame["t"]) == `"call"` {
				break
			}
		}
		if frame == nil || string(frame["t"]) != `"call"` {
			return
		}
		var id string
		encoded, _ := json.Marshal(frame["id"])
		_ = json.Unmarshal(encoded, &id)
		response, _ := json.Marshal(map[string]any{
			"t":  "result",
			"id": id,
			"ok": true,
			"value": map[string]any{
				"providers": map[string]any{
					"local-anthropic": map[string]any{
						"id": "local-anthropic", "api": "anthropic-messages",
						"baseUrl": "https://api.anthropic.com", "enabled": true,
						"models": []map[string]any{{
							"id": "claude-test", "reasoning": true,
							"reasoningLevels": []string{"off", "low", "high"},
							"contextWindow":   200000, "maxTokens": 8192, "enabled": true,
							"input": []string{"text"},
						}},
					},
				},
				"keys": map[string]string{"local-anthropic": "local-key"},
			},
		})
		_ = runner.Write(context.Background(), websocket.MessageText, response)
	}()

	request, _ := http.NewRequest(http.MethodPost,
		h.server.URL+"/api/config/import-runner?runnerId="+runnerID, nil)
	request.Header.Set("Cookie", "nex_session="+session)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d: %s", response.StatusCode, body)
	}
	if strings.Contains(string(body), "local-key") {
		t.Fatal("import response leaks the key")
	}
	doc, _ := h.app.Config.Store().Snapshot()
	imported := doc.Scopes.Runner[runnerID].Providers["local-anthropic"]
	if imported == nil || imported.APIKey != "sec_prov_local-anthropic_apikey" {
		t.Fatalf("imported provider = %+v", imported)
	}
	if value, ok, _ := h.app.Config.Store().Secrets().Get("sec_prov_local-anthropic_apikey"); !ok || value != "local-key" {
		t.Fatalf("secret not stored: %q %v", value, ok)
	}

	missing, _ := http.NewRequest(http.MethodPost, h.server.URL+"/api/config/import-runner?runnerId=ghost", nil)
	missing.Header.Set("Cookie", "nex_session="+session)
	ghostResponse, err := http.DefaultClient.Do(missing)
	if err != nil {
		t.Fatalf("ghost import: %v", err)
	}
	ghostResponse.Body.Close()
	if ghostResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("ghost runner status = %d", ghostResponse.StatusCode)
	}
}

func mustJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}
