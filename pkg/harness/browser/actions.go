package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TargetSpec identifies a page element via snapshot ref, CSS selector, visible text, or viewport coordinates.
type TargetSpec struct {
	Ref      string   `json:"ref,omitempty"`
	Selector string   `json:"selector,omitempty"`
	Text     string   `json:"text,omitempty"`
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
}

// TabSession binds a single Chromium page target + flattened CDP session to an Agent session.
type TabSession struct {
	TabID            string
	TargetID         string
	CDPSessionID     string
	BrowserContextID string
	OwnerSessionID   string

	cdp *CDPClient

	mu           sync.Mutex
	url          string
	title        string
	viewport     Viewport
	humanControl bool
	streamActive bool
	inFlightAck  bool
	console      []ConsoleEntry
	network      []NetworkEntry
	dialog       *DialogInfo
	lastActiveAt time.Time
}

func newTabSession(tabID, targetID, cdpSessID, bctxID, ownerSessID string, cdp *CDPClient) *TabSession {
	return &TabSession{
		TabID:            tabID,
		TargetID:         targetID,
		CDPSessionID:     cdpSessID,
		BrowserContextID: bctxID,
		OwnerSessionID:   ownerSessID,
		cdp:              cdp,
		viewport:         Viewport{Width: DefaultViewportWidth, Height: DefaultViewportHeight},
		lastActiveAt:     time.Now(),
	}
}

func (t *TabSession) touch() {
	t.mu.Lock()
	t.lastActiveAt = time.Now()
	t.mu.Unlock()
}

func (t *TabSession) Info(active bool) TabInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TabInfo{
		TabID:        t.TabID,
		URL:          t.url,
		Title:        t.title,
		Viewport:     t.viewport,
		Active:       active,
		HumanControl: t.humanControl,
	}
}

func (t *TabSession) initDomains(ctx context.Context) error {
	for _, m := range []string{"Page.enable", "Runtime.enable", "Network.enable", "DOM.enable"} {
		if err := t.cdp.Call(ctx, t.CDPSessionID, m, nil, nil); err != nil {
			return err
		}
	}
	return t.setViewport(ctx, t.viewport.Width, t.viewport.Height)
}

func (t *TabSession) setViewport(ctx context.Context, width, height int) error {
	if width < MinViewportWidth || width > MaxViewportWidth || height < MinViewportHeight || height > MaxViewportHeight {
		return newError(ErrExecutionError, "none",
			fmt.Sprintf("viewport %dx%d out of allowed bounds (%d-%d x %d-%d)",
				width, height, MinViewportWidth, MaxViewportWidth, MinViewportHeight, MaxViewportHeight))
	}
	err := t.cdp.Call(ctx, t.CDPSessionID, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             width,
		"height":            height,
		"deviceScaleFactor": 1,
		"mobile":            width <= 500,
	}, nil)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.viewport = Viewport{Width: width, Height: height}
	t.mu.Unlock()
	return nil
}

func (t *TabSession) navigate(ctx context.Context, rawURL string, cfg Config) (PageState, error) {
	t.touch()
	if err := ValidateNavigationURL(rawURL, cfg); err != nil {
		return PageState{}, err
	}
	var navOut struct {
		ErrorText string `json:"errorText"`
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Page.navigate", map[string]any{"url": rawURL}, &navOut); err != nil {
		return PageState{}, err
	}
	if navOut.ErrorText != "" {
		return PageState{}, newError(ErrExecutionError, "none", fmt.Sprintf("navigation failed: %s", navOut.ErrorText))
	}
	_ = t.waitForLoad(ctx, 5*time.Second)
	return t.getState(ctx)
}

func (t *TabSession) waitForLoad(ctx context.Context, budget time.Duration) error {
	if budget <= 0 {
		budget = DefaultActionTimeout
	}
	deadline := time.Now().Add(budget)
	for {
		var ready string
		if err := t.evalValue(ctx, "document.readyState", &ready); err == nil {
			if ready == "interactive" || ready == "complete" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return newError(ErrTimeout, "none", "timed out waiting for document.readyState")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(80 * time.Millisecond):
		}
	}
}

func (t *TabSession) back(ctx context.Context) (PageState, error) {
	return t.navigateOffset(ctx, -1)
}

func (t *TabSession) forward(ctx context.Context) (PageState, error) {
	return t.navigateOffset(ctx, +1)
}

func (t *TabSession) navigateOffset(ctx context.Context, delta int) (PageState, error) {
	t.touch()
	var hist struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Page.getNavigationHistory", nil, &hist); err != nil {
		return PageState{}, err
	}
	nextIdx := hist.CurrentIndex + delta
	if nextIdx < 0 || nextIdx >= len(hist.Entries) {
		return t.getState(ctx)
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Page.navigateToHistoryEntry", map[string]any{
		"entryId": hist.Entries[nextIdx].ID,
	}, nil); err != nil {
		return PageState{}, err
	}
	_ = t.waitForLoad(ctx, 3*time.Second)
	return t.getState(ctx)
}

func (t *TabSession) reload(ctx context.Context) (PageState, error) {
	t.touch()
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Page.reload", nil, nil); err != nil {
		return PageState{}, err
	}
	_ = t.waitForLoad(ctx, 5*time.Second)
	return t.getState(ctx)
}

func (t *TabSession) getState(ctx context.Context) (PageState, error) {
	t.touch()
	var jsState struct {
		URL          string `json:"url"`
		Title        string `json:"title"`
		ScrollX      int    `json:"scroll_x"`
		ScrollY      int    `json:"scroll_y"`
		InnerWidth   int    `json:"inner_width"`
		InnerHeight  int    `json:"inner_height"`
	}
	script := `(function(){return {url:location.href,title:document.title||'',scroll_x:Math.round(window.scrollX||0),scroll_y:Math.round(window.scrollY||0),inner_width:window.innerWidth||0,inner_height:window.innerHeight||0};})()`
	if err := t.evalValue(ctx, script, &jsState); err != nil {
		return PageState{}, err
	}

	var canBack, canForward bool
	var hist struct {
		CurrentIndex int   `json:"currentIndex"`
		Entries      []any `json:"entries"`
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Page.getNavigationHistory", nil, &hist); err == nil {
		canBack = hist.CurrentIndex > 0
		canForward = hist.CurrentIndex >= 0 && hist.CurrentIndex < len(hist.Entries)-1
	}

	t.mu.Lock()
	if jsState.URL != "" {
		t.url = jsState.URL
	}
	t.title = jsState.Title
	vp := t.viewport
	if jsState.InnerWidth > 0 && jsState.InnerHeight > 0 {
		vp = Viewport{Width: jsState.InnerWidth, Height: jsState.InnerHeight}
		t.viewport = vp
	}
	t.mu.Unlock()

	return PageState{
		TabID:        t.TabID,
		URL:          jsState.URL,
		Title:        jsState.Title,
		CanGoBack:    canBack,
		CanGoForward: canForward,
		ScrollX:      jsState.ScrollX,
		ScrollY:      jsState.ScrollY,
		Viewport:     vp,
	}, nil
}

func (t *TabSession) snapshot(ctx context.Context, selector string, maxElements int, includeHidden bool) (Snapshot, error) {
	t.touch()
	var snap Snapshot
	if err := t.evalValue(ctx, BuildSnapshotScript(selector, maxElements, includeHidden), &snap); err != nil {
		return Snapshot{}, err
	}
	t.mu.Lock()
	if snap.URL != "" {
		t.url = snap.URL
	}
	if snap.Title != "" {
		t.title = snap.Title
	}
	errCount := 0
	for _, c := range t.console {
		if c.Level == "error" {
			errCount++
		}
	}
	snap.ConsoleErrorsCount = errCount
	if snap.DOM == nil {
		snap.DOM = []SnapshotDOMNode{}
	}
	if snap.Elements == nil {
		snap.Elements = []SnapshotElement{}
	}
	t.mu.Unlock()
	return snap, nil
}

func (t *TabSession) captureScreenshot(ctx context.Context, fullPage bool) (string, string, error) {
	t.touch()
	params := map[string]any{
		"format":  "jpeg",
		"quality": 82,
	}
	if fullPage {
		params["captureBeyondViewport"] = true
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Page.captureScreenshot", params, &out); err != nil {
		return "", "", err
	}
	return out.Data, "image/jpeg", nil
}

func (t *TabSession) evalValue(ctx context.Context, expression string, out any) error {
	var res struct {
		Result struct {
			Type        string          `json:"type"`
			Subtype     string          `json:"subtype"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err := t.cdp.Call(ctx, t.CDPSessionID, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
		"userGesture":   true,
	}, &res)
	if err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		msg := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception != nil && res.ExceptionDetails.Exception.Description != "" {
			msg = res.ExceptionDetails.Exception.Description
		}
		return newError(ErrExecutionError, "uncertain", fmt.Sprintf("page JS error: %s", msg))
	}
	if out != nil && len(res.Result.Value) > 0 {
		return json.Unmarshal(res.Result.Value, out)
	}
	return nil
}

type resolvedPoint struct {
	Found    bool   `json:"found"`
	Disabled bool   `json:"disabled"`
	CX       float64 `json:"cx"`
	CY       float64 `json:"cy"`
	Reason   string `json:"reason"`
}

func (t *TabSession) resolvePoint(ctx context.Context, target TargetSpec) (float64, float64, error) {
	if target.X != nil && target.Y != nil {
		return *target.X, *target.Y, nil
	}
	if target.Ref == "" && strings.TrimSpace(target.Selector) == "" && strings.TrimSpace(target.Text) == "" {
		return 0, 0, newError(ErrExecutionError, "none", "target must specify ref, selector, text, or x/y coordinates")
	}

	script := buildResolveTargetScript(target)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var rp resolvedPoint
		if err := t.evalValue(ctx, script, &rp); err != nil {
			return 0, 0, err
		}
		if rp.Found {
			if rp.Disabled {
				return 0, 0, newError(ErrExecutionError, "none", "target element is disabled")
			}
			return rp.CX, rp.CY, nil
		}
		// Never retry missing snapshot ref; prompt model to re-snapshot immediately.
		if target.Ref != "" {
			return 0, 0, newError(ErrRefNotFound, "none", fmt.Sprintf("snapshot ref %q is no longer in the DOM; run snapshot again", target.Ref))
		}
		if time.Now().After(deadline) {
			return 0, 0, newError(ErrRefNotFound, "none", fmt.Sprintf("target element not found or not visible (%s)", rp.Reason))
		}
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func buildResolveTargetScript(target TargetSpec) string {
	return fmt.Sprintf(`(function(){
var ref=%s;var sel=%s;var txt=%s;
function findByText(needle){
  var all=document.querySelectorAll('a,button,input,textarea,select,[role],label,summary,h1,h2,h3,h4,span,div');
  var exact=null;var partial=null;
  for(var i=0;i<all.length;i++){
    var el=all[i];
    var r=el.getBoundingClientRect();
    if(r.width<=0||r.height<=0)continue;
    var t=String(el.innerText||el.textContent||el.getAttribute('aria-label')||'').trim().replace(/\s+/g,' ');
    if(t===needle){if(exact)return {ambiguous:true};exact=el;}
    else if(!exact&&t.indexOf(needle)>=0&&!partial){partial=el;}
  }
  return {el:exact||partial};
}
function frameOffsetFor(el){
  var ox=0;var oy=0;
  try{
    var win=el.ownerDocument.defaultView;
    while(win&&win!==window&&win.frameElement){
      var fr=win.frameElement.getBoundingClientRect();
      ox+=fr.left;oy+=fr.top;
      win=win.parent;
    }
  }catch(e){}
  return {ox:ox,oy:oy};
}
var el=null;
if(ref){
  el=(window.__nexRefs&&window.__nexRefs.get(ref))||null;
  if(!el||!el.isConnected)return {found:false,reason:'ref_missing'};
}else if(sel){
  el=document.querySelector(sel);
  if(!el)return {found:false,reason:'selector_not_found'};
}else if(txt){
  var m=findByText(txt);
  if(m.ambiguous)return {found:false,reason:'multiple_text_matches'};
  el=m.el||null;
  if(!el)return {found:false,reason:'text_not_found'};
}
try{el.scrollIntoView({block:'center',inline:'center'});}catch(e){}
var st=el.ownerDocument.defaultView.getComputedStyle(el);
if(st&&(st.display==='none'||st.visibility==='hidden'||st.opacity==='0'))return {found:false,reason:'hidden'};
var b=el.getBoundingClientRect();
if(b.width<=0||b.height<=0)return {found:false,reason:'zero_size'};
var off=frameOffsetFor(el);
return {found:true,disabled:el.disabled===true,cx:Math.round(b.left+off.ox+b.width/2),cy:Math.round(b.top+off.oy+b.height/2)};
})()`, quoteJS(target.Ref), quoteJS(target.Selector), quoteJS(target.Text))
}

func (t *TabSession) click(ctx context.Context, target TargetSpec, button string, clickCount int) (string, error) {
	t.touch()
	cx, cy, err := t.resolvePoint(ctx, target)
	if err != nil {
		return "", err
	}
	if button == "" {
		button = "left"
	}
	if clickCount <= 0 {
		clickCount = 1
	}
	if err := t.dispatchMouseSequence(ctx, cx, cy, button, clickCount); err != nil {
		return "", err
	}
	_ = t.waitForLoad(ctx, 1200*time.Millisecond)
	return fmt.Sprintf("clicked (%v, %v)", cx, cy), nil
}

func (t *TabSession) hover(ctx context.Context, target TargetSpec) error {
	t.touch()
	cx, cy, err := t.resolvePoint(ctx, target)
	if err != nil {
		return err
	}
	return t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved",
		"x":    cx,
		"y":    cy,
	}, nil)
}

func (t *TabSession) dispatchMouseSequence(ctx context.Context, cx, cy float64, button string, clickCount int) error {
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": cx, "y": cy,
	}, nil); err != nil {
		return err
	}
	for c := 1; c <= clickCount; c++ {
		if err := t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
			"type": "mousePressed", "x": cx, "y": cy, "button": button, "clickCount": c,
		}, nil); err != nil {
			return err
		}
		if err := t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseReleased", "x": cx, "y": cy, "button": button, "clickCount": c,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (t *TabSession) fill(ctx context.Context, target TargetSpec, text string, clearFirst bool) error {
	t.touch()
	cx, cy, err := t.resolvePoint(ctx, target)
	if err != nil {
		return err
	}
	if err := t.dispatchMouseSequence(ctx, cx, cy, "left", 1); err != nil {
		return err
	}
	if clearFirst {
		clearScript := `(function(){
var el=document.activeElement;
if(!el)return;
if('value' in el){
  var proto=el.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;
  var setter=Object.getOwnPropertyDescriptor(proto,'value');
  if(setter&&setter.set)setter.set.call(el,'');else el.value='';
  el.dispatchEvent(new Event('input',{bubbles:true}));
}else if(el.isContentEditable){
  el.textContent='';
}
})()`
		_ = t.evalValue(ctx, clearScript, nil)
	}
	return t.cdp.Call(ctx, t.CDPSessionID, "Input.insertText", map[string]any{"text": text}, nil)
}

func (t *TabSession) typeText(ctx context.Context, text string) error {
	t.touch()
	return t.cdp.Call(ctx, t.CDPSessionID, "Input.insertText", map[string]any{"text": text}, nil)
}

func (t *TabSession) pressKey(ctx context.Context, keyCombo string) error {
	t.touch()
	parts := strings.Split(keyCombo, "+")
	mainKey := strings.TrimSpace(parts[len(parts)-1])
	mods := 0
	for _, mod := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(mod)) {
		case "alt", "option":
			mods |= 1
		case "ctrl", "control":
			mods |= 2
		case "meta", "cmd", "command":
			mods |= 4
		case "shift":
			mods |= 8
		}
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchKeyEvent", map[string]any{
		"type":                  "rawKeyDown",
		"key":                   mainKey,
		"code":                  keyCodeGuess(mainKey),
		"modifiers":             mods,
		"windowsVirtualKeyCode": virtualKeyCode(mainKey),
	}, nil); err != nil {
		return err
	}
	if len([]rune(mainKey)) == 1 && mods == 0 {
		_ = t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchKeyEvent", map[string]any{
			"type": "char",
			"text": mainKey,
		}, nil)
	}
	return t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchKeyEvent", map[string]any{
		"type":                  "keyUp",
		"key":                   mainKey,
		"code":                  keyCodeGuess(mainKey),
		"modifiers":             mods,
		"windowsVirtualKeyCode": virtualKeyCode(mainKey),
	}, nil)
}

func (t *TabSession) selectOption(ctx context.Context, target TargetSpec, values []string) error {
	t.touch()
	valJSON, _ := json.Marshal(values)
	script := fmt.Sprintf(`(function(){
var ref=%s;var sel=%s;var vals=%s;
var el=ref?(window.__nexRefs&&window.__nexRefs.get(ref)):(sel?document.querySelector(sel):null);
if(!el||el.tagName!=='SELECT')return false;
for(var i=0;i<el.options.length;i++){
  el.options[i].selected=vals.indexOf(el.options[i].value)>=0||vals.indexOf(el.options[i].text)>=0;
}
el.dispatchEvent(new Event('input',{bubbles:true}));
el.dispatchEvent(new Event('change',{bubbles:true}));
return true;
})()`, quoteJS(target.Ref), quoteJS(target.Selector), string(valJSON))
	var ok bool
	if err := t.evalValue(ctx, script, &ok); err != nil {
		return err
	}
	if !ok {
		return newError(ErrRefNotFound, "none", "select element not found")
	}
	return nil
}

func (t *TabSession) setChecked(ctx context.Context, target TargetSpec, checked bool) error {
	t.touch()
	script := fmt.Sprintf(`(function(){
var ref=%s;var sel=%s;var want=%v;
var el=ref?(window.__nexRefs&&window.__nexRefs.get(ref)):(sel?document.querySelector(sel):null);
if(!el)return {ok:false};
return {ok:true,current:el.checked===true};
})()`, quoteJS(target.Ref), quoteJS(target.Selector), checked)
	var st struct {
		OK      bool `json:"ok"`
		Current bool `json:"current"`
	}
	if err := t.evalValue(ctx, script, &st); err != nil {
		return err
	}
	if !st.OK {
		return newError(ErrRefNotFound, "none", "checkbox/radio element not found")
	}
	if st.Current == checked {
		return nil
	}
	_, err := t.click(ctx, target, "left", 1)
	return err
}

func (t *TabSession) scroll(ctx context.Context, target TargetSpec, direction string, deltaX, deltaY float64) error {
	t.touch()
	if deltaX == 0 && deltaY == 0 {
		switch strings.ToLower(direction) {
		case "up":
			deltaY = -480
		case "down", "":
			deltaY = 480
		case "left":
			deltaX = -480
		case "right":
			deltaX = 480
		}
	}
	t.mu.Lock()
	cx := float64(t.viewport.Width) / 2
	cy := float64(t.viewport.Height) / 2
	t.mu.Unlock()
	if target.Ref != "" || target.Selector != "" || target.Text != "" || (target.X != nil && target.Y != nil) {
		px, py, err := t.resolvePoint(ctx, target)
		if err != nil {
			return err
		}
		cx, cy = px, py
	}
	return t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseWheel",
		"x":      cx,
		"y":      cy,
		"deltaX": deltaX,
		"deltaY": deltaY,
	}, nil)
}

func (t *TabSession) drag(ctx context.Context, from, to TargetSpec) error {
	t.touch()
	fx, fy, err := t.resolvePoint(ctx, from)
	if err != nil {
		return err
	}
	tx, ty, err := t.resolvePoint(ctx, to)
	if err != nil {
		return err
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": fx, "y": fy,
	}, nil); err != nil {
		return err
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mousePressed", "x": fx, "y": fy, "button": "left", "clickCount": 1,
	}, nil); err != nil {
		return err
	}
	steps := 5
	for i := 1; i <= steps; i++ {
		nx := fx + (tx-fx)*float64(i)/float64(steps)
		ny := fy + (ty-fy)*float64(i)/float64(steps)
		_ = t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseMoved", "x": nx, "y": ny, "button": "left",
		}, nil)
	}
	return t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseReleased", "x": tx, "y": ty, "button": "left", "clickCount": 1,
	}, nil)
}

func (t *TabSession) uploadFiles(ctx context.Context, workDir string, target TargetSpec, paths []string) error {
	t.touch()
	if len(paths) == 0 {
		return newError(ErrExecutionError, "none", "paths must not be empty")
	}
	var resolved []string
	absWork, _ := filepath.Abs(workDir)
	for _, p := range paths {
		cand := p
		if !filepath.IsAbs(cand) && workDir != "" {
			cand = filepath.Join(workDir, cand)
		}
		absPath, err := filepath.Abs(cand)
		if err != nil {
			return newError(ErrExecutionError, "none", err.Error())
		}
		if absWork != "" && !strings.HasPrefix(absPath, absWork+string(filepath.Separator)) && absPath != absWork {
			return newError(ErrPermissionDenied, "none", fmt.Sprintf("upload path %q is outside workspace root", p))
		}
		resolved = append(resolved, absPath)
	}
	var doc struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "DOM.getDocument", map[string]any{"depth": 0}, &doc); err != nil {
		return err
	}
	sel := target.Selector
	if sel == "" {
		sel = `input[type="file"]`
	}
	var q struct {
		NodeID int `json:"nodeId"`
	}
	if err := t.cdp.Call(ctx, t.CDPSessionID, "DOM.querySelector", map[string]any{
		"nodeId":   doc.Root.NodeID,
		"selector": sel,
	}, &q); err != nil {
		return err
	}
	if q.NodeID == 0 {
		return newError(ErrRefNotFound, "none", fmt.Sprintf("file input not found for selector %q", sel))
	}
	return t.cdp.Call(ctx, t.CDPSessionID, "DOM.setFileInputFiles", map[string]any{
		"nodeId": q.NodeID,
		"files":  resolved,
	}, nil)
}

func (t *TabSession) handleDialog(ctx context.Context, accept bool, promptText string) error {
	t.touch()
	params := map[string]any{"accept": accept}
	if promptText != "" {
		params["promptText"] = promptText
	}
	err := t.cdp.Call(ctx, t.CDPSessionID, "Page.handleJavaScriptDialog", params, nil)
	if err == nil {
		t.mu.Lock()
		t.dialog = nil
		t.mu.Unlock()
	}
	return err
}

func (t *TabSession) waitFor(ctx context.Context, mode, value string, timeoutMS int) error {
	t.touch()
	if timeoutMS <= 0 || timeoutMS > 5000 {
		timeoutMS = 5000
	}
	budget := time.Duration(timeoutMS) * time.Millisecond
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "load":
		return t.waitForLoad(ctx, budget)
	case "timeout":
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(budget):
			return nil
		}
	case "selector":
		deadline := time.Now().Add(budget)
		script := fmt.Sprintf(`Boolean(document.querySelector(%s))`, quoteJS(value))
		for {
			var found bool
			if err := t.evalValue(ctx, script, &found); err == nil && found {
				return nil
			}
			if time.Now().After(deadline) {
				return newError(ErrTimeout, "none", fmt.Sprintf("timed out waiting for selector %q", value))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	case "text":
		deadline := time.Now().Add(budget)
		script := fmt.Sprintf(`Boolean(document.body && (document.body.innerText||'').indexOf(%s)>=0)`, quoteJS(value))
		for {
			var found bool
			if err := t.evalValue(ctx, script, &found); err == nil && found {
				return nil
			}
			if time.Now().After(deadline) {
				return newError(ErrTimeout, "none", fmt.Sprintf("timed out waiting for text %q", value))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	case "url":
		deadline := time.Now().Add(budget)
		for {
			var cur string
			if err := t.evalValue(ctx, "location.href", &cur); err == nil && strings.Contains(cur, value) {
				return nil
			}
			if time.Now().After(deadline) {
				return newError(ErrTimeout, "none", fmt.Sprintf("timed out waiting for URL containing %q", value))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	default:
		return newError(ErrExecutionError, "none", fmt.Sprintf("unknown wait mode %q", mode))
	}
}

func (t *TabSession) pushConsole(entry ConsoleEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.console = append(t.console, entry)
	if len(t.console) > MaxConsoleEntries {
		t.console = t.console[len(t.console)-MaxConsoleEntries:]
	}
}

func (t *TabSession) consoleEntries(level string) []ConsoleEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if level == "" {
		return append([]ConsoleEntry(nil), t.console...)
	}
	var out []ConsoleEntry
	for _, c := range t.console {
		if strings.EqualFold(c.Level, level) {
			out = append(out, c)
		}
	}
	return out
}

func (t *TabSession) pushNetwork(entry NetworkEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.network = append(t.network, entry)
	if len(t.network) > MaxNetworkEntries {
		t.network = t.network[len(t.network)-MaxNetworkEntries:]
	}
}

func (t *TabSession) networkEntries(failedOnly bool) []NetworkEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !failedOnly {
		return append([]NetworkEntry(nil), t.network...)
	}
	var out []NetworkEntry
	for _, n := range t.network {
		if n.Status >= 400 || n.ErrorText != "" {
			out = append(out, n)
		}
	}
	return out
}

func keyCodeGuess(k string) string {
	switch k {
	case "Enter":
		return "Enter"
	case "Escape":
		return "Escape"
	case "Backspace":
		return "Backspace"
	case "Tab":
		return "Tab"
	case " ":
		return "Space"
	case "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight":
		return k
	}
	if len(k) == 1 {
		c := strings.ToUpper(k)
		if c >= "A" && c <= "Z" {
			return "Key" + c
		}
		if c >= "0" && c <= "9" {
			return "Digit" + c
		}
	}
	return k
}

func virtualKeyCode(k string) int {
	switch k {
	case "Backspace":
		return 8
	case "Tab":
		return 9
	case "Enter":
		return 13
	case "Escape":
		return 27
	case " ":
		return 32
	case "ArrowLeft":
		return 37
	case "ArrowUp":
		return 38
	case "ArrowRight":
		return 39
	case "ArrowDown":
		return 40
	case "Delete":
		return 46
	}
	if len(k) == 1 {
		r := strings.ToUpper(k)[0]
		return int(r)
	}
	return 0
}

func isSameSiteURL(a, b string) bool {
	if sameHostname(a, b) {
		return true
	}
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && ua.Host != "" && ua.Host == ub.Host
}
