package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/protocol"
)

func TestValidateNavigationURL(t *testing.T) {
	cfg := Config{
		AllowedDomains: []string{"example.com", "localhost"},
		BlockedDomains: []string{"evil.example.com"},
	}

	if err := ValidateNavigationURL("https://app.example.com/path", cfg); err != nil {
		t.Fatalf("expected allowed subdomain to pass: %v", err)
	}
	if err := ValidateNavigationURL("about:blank", cfg); err != nil {
		t.Fatalf("expected about:blank to pass: %v", err)
	}
	for _, bad := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"data:text/html,<h1>hi</h1>",
		"chrome://settings",
		"https://evil.example.com/login",
		"https://other.org/",
	} {
		if err := ValidateNavigationURL(bad, cfg); err == nil {
			t.Fatalf("expected URL %q to be blocked", bad)
		}
	}
}

func TestSnapshotJSONOrderingAndToolReadOnly(t *testing.T) {
	snap := Snapshot{
		URL:      "https://example.com",
		Title:    "Example",
		Viewport: Viewport{Width: 1280, Height: 720},
		DOM: []SnapshotDOMNode{
			{Tag: "h1", Depth: 1, InViewport: true, Text: "Welcome"},
		},
		Elements: []SnapshotElement{
			{Ref: "e1", Tag: "button", Selector: "#go", Rect: Rect{X: 10, Y: 20, Width: 80, Height: 30}, InViewport: true},
		},
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	s := string(b)
	domIdx := strings.Index(s, `"dom":`)
	elIdx := strings.Index(s, `"elements":`)
	if domIdx < 0 || elIdx < 0 || domIdx > elIdx {
		t.Fatalf("expected dom before elements in snapshot JSON: %s", s)
	}

	tool := &BrowserTool{}
	if !tool.IsReadOnly(json.RawMessage(`{"action":"snapshot"}`)) {
		t.Fatalf("snapshot must be read-only")
	}
	if !tool.IsReadOnly(json.RawMessage(`{"action":"screenshot"}`)) {
		t.Fatalf("screenshot must be read-only")
	}
	if tool.IsReadOnly(json.RawMessage(`{"action":"click","ref":"e1"}`)) {
		t.Fatalf("click must not be read-only")
	}
	if tool.IsReadOnly(json.RawMessage(`{"action":"eval","expression":"1+1"}`)) {
		t.Fatalf("eval must not be read-only")
	}
}

func TestCDPClientScreencastAndHandoffWithMockServer(t *testing.T) {
	var ackCount int
	var ackMu sync.Mutex

	mockCDP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()

		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var req cdpRequest
			_ = json.Unmarshal(data, &req)

			var res any = map[string]any{}
			switch req.Method {
			case "Page.startScreencast":
				resp, _ := json.Marshal(cdpEnvelope{ID: req.ID, SessionID: req.SessionID, Result: []byte(`{}`)})
				_ = ws.Write(ctx, websocket.MessageText, resp)
				// Push a synthetic screencastFrame event
				frameEv, _ := json.Marshal(map[string]any{
					"method":    "Page.screencastFrame",
					"sessionId": req.SessionID,
					"params": map[string]any{
						"data":      base64.StdEncoding.EncodeToString([]byte("FAKEJPEG")),
						"sessionId": 42,
					},
				})
				_ = ws.Write(ctx, websocket.MessageText, frameEv)
				continue
			case "Page.screencastFrameAck":
				ackMu.Lock()
				ackCount++
				ackMu.Unlock()
			case "Page.captureScreenshot":
				res = map[string]any{"data": "SHOTB64=="}
			}
			rawRes, _ := json.Marshal(res)
			resp, _ := json.Marshal(cdpEnvelope{ID: req.ID, SessionID: req.SessionID, Result: rawRes})
			_ = ws.Write(ctx, websocket.MessageText, resp)
		}
	}))
	defer mockCDP.Close()

	wsURL := "ws" + strings.TrimPrefix(mockCDP.URL, "http")
	cdp, err := DialCDP(context.Background(), wsURL)
	if err != nil {
		t.Fatalf("DialCDP: %v", err)
	}
	defer cdp.Close()

	tab := newTabSession("bt_test_1", "tgt_1", "cdp_s1", "", "sess_1", cdp)
	gotBinary := make(chan []byte, 1)
	cdp.OnEvent("Page.screencastFrame", func(_ string, params json.RawMessage) {
		tab.HandleScreencastFrame(params, func(tabID string, jpeg []byte) {
			gotBinary <- protocol.EncodeBrowserBinaryFrame(tabID, jpeg)
		})
	})

	if err := tab.StartScreencast(context.Background(), 70, 800, 600); err != nil {
		t.Fatalf("StartScreencast: %v", err)
	}

	select {
	case frame := <-gotBinary:
		op, id, payload, decErr := protocol.DecodePTYBinaryFrame(frame)
		if decErr != nil || op != protocol.BrowserBinaryScreencast || id != "bt_test_1" || string(payload) != "FAKEJPEG" {
			t.Fatalf("unexpected binary frame: err=%v op=%x id=%q data=%q", decErr, op, id, string(payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for screencast binary frame")
	}

	// Allow async ack goroutine to send Page.screencastFrameAck
	time.Sleep(50 * time.Millisecond)
	ackMu.Lock()
	acks := ackCount
	ackMu.Unlock()
	if acks == 0 {
		t.Fatalf("expected Page.screencastFrameAck to be sent")
	}

	// Verify handoff resume + cancel
	var statesMu sync.Mutex
	var states []HandoffState
	hc := NewHandoffController(func(st HandoffState) {
		statesMu.Lock()
		states = append(states, st)
		statesMu.Unlock()
	})
	done := make(chan bool, 1)
	go func() {
		resumed, _ := hc.RequestHandoff(context.Background(), "sess_h1", "bt_1", "Please sign in", "Complete OAuth")
		done <- resumed
	}()
	time.Sleep(30 * time.Millisecond)
	if !hc.Resolve("sess_h1", true) {
		t.Fatalf("expected Resolve to succeed")
	}
	if !<-done {
		t.Fatalf("expected handoff to resolve with resumed=true")
	}
	statesMu.Lock()
	defer statesMu.Unlock()
	if len(states) < 2 || states[0].State != "awaiting_human" || states[len(states)-1].State != "resumed" {
		t.Fatalf("unexpected handoff state transitions: %+v", states)
	}
}

func TestRealHeadlessChromeE2E(t *testing.T) {
	if p := Probe(context.Background(), t.TempDir(), DefaultConfig()); p.Status != "ready" {
		t.Skipf("skipping real Chrome E2E test: browser probe status=%s (%s)", p.Status, p.Error)
	}

	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head><title>Nex Browser E2E</title></head>
  <body>
    <h1>Sign In</h1>
    <form>
      <label for="email">Email</label>
      <input id="email" type="email" placeholder="you@example.com" />
      <button id="submit-btn" type="button" onclick="console.log('submitted:'+document.getElementById('email').value);document.getElementById('status').textContent='DONE:'+document.getElementById('email').value;">Submit</button>
    </form>
    <div id="status">IDLE</div>
  </body>
</html>`))
	}))
	defer appSrv.Close()

	var statesMu sync.Mutex
	var states []SessionStatePayload
	binaryFrames := make(chan []byte, 16)

	mgr := NewManager(
		t.TempDir(),
		"run_test",
		nil,
		func(st SessionStatePayload) {
			statesMu.Lock()
			states = append(states, st)
			statesMu.Unlock()
		},
		nil,
		func(frame []byte) {
			select {
			case binaryFrames <- frame:
			default:
			}
		},
	)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	tool := mgr.SessionTool("sess_e2e", t.TempDir())

	// 1. Open page
	openRes, err := tool.Execute(ctx, "", json.RawMessage(`{"action":"open","url":"`+appSrv.URL+`/login"}`), nil)
	if err != nil || openRes.IsError {
		t.Fatalf("open failed: err=%v out=%s", err, openRes.Output)
	}
	if !strings.Contains(openRes.Output, "Nex Browser E2E") {
		t.Fatalf("unexpected open output: %s", openRes.Output)
	}

	// 2. Same-host navigation reuses existing single tab
	open2Res, err := tool.Execute(ctx, "", json.RawMessage(`{"action":"open","url":"`+appSrv.URL+`/dashboard"}`), nil)
	if err != nil || open2Res.IsError {
		t.Fatalf("second open failed: err=%v out=%s", err, open2Res.Output)
	}
	tabs := mgr.listTabs("sess_e2e")
	if len(tabs) != 1 {
		t.Fatalf("expected same-host open to reuse 1 tab, got %d", len(tabs))
	}

	// 3. Snapshot reveals DOM + input ref + button ref
	snapRes, err := tool.Execute(ctx, "", json.RawMessage(`{"action":"snapshot"}`), nil)
	if err != nil || snapRes.IsError {
		t.Fatalf("snapshot failed: err=%v out=%s", err, snapRes.Output)
	}
	rawSnap := snapRes.Output
	start := strings.Index(rawSnap, "{")
	end := strings.LastIndex(rawSnap, "}")
	var snap Snapshot
	if start < 0 || end <= start || json.Unmarshal([]byte(rawSnap[start:end+1]), &snap) != nil {
		t.Fatalf("decode snapshot output failed: %s", rawSnap)
	}
	var emailRef, btnRef string
	for _, el := range snap.Elements {
		if el.Tag == "input" {
			emailRef = el.Ref
		}
		if el.Tag == "button" {
			btnRef = el.Ref
		}
	}
	if emailRef == "" || btnRef == "" {
		t.Fatalf("expected input and button refs in snapshot, got %+v", snap.Elements)
	}

	// 4. Fill input via ref + Click button via ref
	fillArgs, _ := json.Marshal(map[string]any{"action": "fill", "ref": emailRef, "text": "alice@nex.dev"})
	if res, err := tool.Execute(ctx, "", fillArgs, nil); err != nil || res.IsError {
		t.Fatalf("fill failed: err=%v out=%s", err, res.Output)
	}
	clickArgs, _ := json.Marshal(map[string]any{"action": "click", "ref": btnRef})
	if res, err := tool.Execute(ctx, "", clickArgs, nil); err != nil || res.IsError {
		t.Fatalf("click failed: err=%v out=%s", err, res.Output)
	}

	// 5. Verify DOM mutation & console log
	evalRes, err := tool.Execute(ctx, "", json.RawMessage(`{"action":"eval","expression":"document.getElementById('status').textContent"}`), nil)
	if err != nil || evalRes.IsError || !strings.Contains(evalRes.Output, "DONE:alice@nex.dev") {
		t.Fatalf("expected DOM update DONE:alice@nex.dev, got err=%v out=%s", err, evalRes.Output)
	}
	conRes, err := tool.Execute(ctx, "", json.RawMessage(`{"action":"console"}`), nil)
	if err != nil || conRes.IsError || !strings.Contains(conRes.Output, "submitted:alice@nex.dev") {
		t.Fatalf("expected console entry submitted:alice@nex.dev, got err=%v out=%s", err, conRes.Output)
	}

	// 6. Screenshot returns ExtraBlocks image block
	shotRes, err := tool.Execute(ctx, "", json.RawMessage(`{"action":"screenshot"}`), nil)
	if err != nil || shotRes.IsError || len(shotRes.ExtraBlocks) != 1 || shotRes.ExtraBlocks[0].Type != ai.BlockImage {
		t.Fatalf("expected 1 BlockImage ExtraBlock, got err=%v res=%+v", err, shotRes)
	}

	// 7. Start Screencast pushes real 0x10 binary JPEG frame
	if _, err := mgr.HandleControl(ctx, ControlRequest{
		SessionID: "sess_e2e",
		TabID:     tabs[0].TabID,
		Action:    "stream_start",
		Quality:   70,
	}); err != nil {
		t.Fatalf("stream_start failed: %v", err)
	}
	select {
	case frame := <-binaryFrames:
		op, gotTabID, payload, decErr := protocol.DecodePTYBinaryFrame(frame)
		if decErr != nil || op != protocol.BrowserBinaryScreencast || gotTabID != tabs[0].TabID || len(payload) < 100 {
			t.Fatalf("unexpected real screencast frame: err=%v op=%x len=%d", decErr, op, len(payload))
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for real Chrome screencast frame")
	}
}
