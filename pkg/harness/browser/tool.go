package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
)

// BrowserTool exposes unified browser automation operations to the LLM agent loop.
type BrowserTool struct {
	mgr       *Manager
	sessionID string
	workDir   string
}

func (b *BrowserTool) Name() string     { return "browser" }
func (b *BrowserTool) Category() string { return "system" }

type browserArgs struct {
	Action        string   `json:"action"`
	TabID         string   `json:"tab_id,omitempty"`
	URL           string   `json:"url,omitempty"`
	NewTab        bool     `json:"new_tab,omitempty"`
	Ref           string   `json:"ref,omitempty"`
	Selector      string   `json:"selector,omitempty"`
	Text          string   `json:"text,omitempty"`
	X             *float64 `json:"x,omitempty"`
	Y             *float64 `json:"y,omitempty"`
	From          *TargetSpec `json:"from,omitempty"`
	To            *TargetSpec `json:"to,omitempty"`
	Button        string   `json:"button,omitempty"`
	ClearFirst    *bool    `json:"clear_first,omitempty"`
	Sensitive     bool     `json:"sensitive,omitempty"`
	Key           string   `json:"key,omitempty"`
	Values        []string `json:"values,omitempty"`
	Checked       *bool    `json:"checked,omitempty"`
	Direction     string   `json:"direction,omitempty"`
	DeltaX        float64  `json:"delta_x,omitempty"`
	DeltaY        float64  `json:"delta_y,omitempty"`
	Paths         []string `json:"paths,omitempty"`
	Accept        *bool    `json:"accept,omitempty"`
	PromptText    string   `json:"prompt_text,omitempty"`
	For           string   `json:"for,omitempty"`
	Value         string   `json:"value,omitempty"`
	TimeoutMS     int      `json:"timeout_ms,omitempty"`
	Expression    string   `json:"expression,omitempty"`
	Width         int      `json:"width,omitempty"`
	Height        int      `json:"height,omitempty"`
	FullPage      bool     `json:"full_page,omitempty"`
	MaxElements   int      `json:"max_elements,omitempty"`
	IncludeHidden bool     `json:"include_hidden,omitempty"`
	Level         string   `json:"level,omitempty"`
	FailedOnly    bool     `json:"failed_only,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	Message       string   `json:"message,omitempty"`
}

func (b *BrowserTool) IsReadOnly(rawArgs json.RawMessage) bool {
	var args browserArgs
	if json.Unmarshal(rawArgs, &args) != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "open", "list_tabs", "switch_tab", "back", "forward", "reload",
		"snapshot", "screenshot", "get_state", "console", "network",
		"scroll", "wait", "request_handoff":
		return true
	default:
		return false
	}
}

func (b *BrowserTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:     "browser",
		Category: "system",
		Description: "Control a real Chromium browser managed by the Runner.\n\n" +
			"- Navigation/Tabs: `open` (reuses same-host tab by default), `new_tab`, `list_tabs`, `switch_tab`, `close_tab`, `back`, `forward`, `reload`.\n" +
			"- Observation: `snapshot` (PRIMARY way to read pages & obtain `e1..eN` refs), `get_state`, `console`, `network`, `screenshot` (use ONLY when visual layout matters, user requested a screenshot, or target is canvas/non-DOM).\n" +
			"- Interaction: `click`, `dblclick`, `hover`, `fill` (supports `sensitive: true` for passwords), `type`, `press`, `select`, `check`, `scroll`, `drag`, `upload`, `handle_dialog`.\n" +
			"- Wait/Eval/Viewport/Handoff: `wait`, `eval`, `set_viewport`, `request_handoff`.\n" +
			"- On login walls, 2FA, CAPTCHAs, or payments, ALWAYS call `request_handoff` so the user can complete it directly in the UI.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["open","new_tab","list_tabs","switch_tab","close_tab","back","forward","reload","snapshot","screenshot","get_state","console","network","click","dblclick","hover","fill","type","press","select","check","scroll","drag","upload","handle_dialog","wait","eval","set_viewport","request_handoff"]
				},
				"tab_id": {"type": "string"},
				"url": {"type": "string"},
				"new_tab": {"type": "boolean"},
				"ref": {"type": "string", "description": "Element ref from snapshot (e.g. e1)"},
				"selector": {"type": "string"},
				"text": {"type": "string"},
				"x": {"type": "number"},
				"y": {"type": "number"},
				"from": {"type": "object"},
				"to": {"type": "object"},
				"button": {"type": "string", "enum": ["left","right","middle"]},
				"clear_first": {"type": "boolean"},
				"sensitive": {"type": "boolean"},
				"key": {"type": "string"},
				"values": {"type": "array", "items": {"type": "string"}},
				"checked": {"type": "boolean"},
				"direction": {"type": "string", "enum": ["up","down","left","right"]},
				"delta_x": {"type": "number"},
				"delta_y": {"type": "number"},
				"paths": {"type": "array", "items": {"type": "string"}},
				"accept": {"type": "boolean"},
				"prompt_text": {"type": "string"},
				"for": {"type": "string", "enum": ["load","selector","text","url","timeout"]},
				"value": {"type": "string"},
				"timeout_ms": {"type": "integer"},
				"expression": {"type": "string"},
				"width": {"type": "integer"},
				"height": {"type": "integer"},
				"full_page": {"type": "boolean"},
				"max_elements": {"type": "integer"},
				"include_hidden": {"type": "boolean"},
				"level": {"type": "string"},
				"failed_only": {"type": "boolean"},
				"reason": {"type": "string"},
				"message": {"type": "string"}
			},
			"required": ["action"]
		}`),
	}
}

func (b *BrowserTool) Execute(ctx context.Context, workDir string, rawArgs json.RawMessage, _ tools.UpdateFunc) (tools.Result, error) {
	if workDir == "" {
		workDir = b.workDir
	}
	var args browserArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return errResult(newError(ErrExecutionError, "none", "invalid browser arguments: "+err.Error())), nil
	}

	action := strings.ToLower(strings.TrimSpace(args.Action))
	switch action {
	case "open":
		if strings.TrimSpace(args.URL) == "" {
			return errResult(newError(ErrExecutionError, "none", "url is required for open")), nil
		}
		_, st, err := b.mgr.openURL(ctx, b.sessionID, args.URL, args.NewTab)
		if err != nil {
			return errResult(err), nil
		}
		return jsonResult("page_state", st, false), nil

	case "new_tab":
		t, err := b.mgr.createTab(ctx, b.sessionID)
		if err != nil {
			return errResult(err), nil
		}
		if args.URL != "" {
			st, err := t.navigate(ctx, args.URL, b.mgr.Config())
			if err != nil {
				return errResult(err), nil
			}
			b.mgr.emitSessionState(b.sessionID)
			return jsonResult("page_state", st, false), nil
		}
		return jsonResult("tab", t.Info(true), false), nil

	case "list_tabs":
		return jsonResult("tabs", b.mgr.listTabs(b.sessionID), false), nil

	case "switch_tab":
		t, err := b.mgr.switchTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		st, err := t.getState(ctx)
		if err != nil {
			return jsonResult("tab", t.Info(true), false), nil
		}
		return jsonResult("page_state", st, false), nil

	case "close_tab":
		tid := args.TabID
		if tid == "" {
			t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, "")
			if err != nil {
				return errResult(err), nil
			}
			tid = t.TabID
		}
		if err := b.mgr.closeTab(ctx, b.sessionID, tid); err != nil {
			return errResult(err), nil
		}
		return tools.Result{Output: fmt.Sprintf("closed tab %s", tid)}, nil

	case "back", "forward", "reload", "get_state":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		var st PageState
		switch action {
		case "back":
			st, err = t.back(ctx)
		case "forward":
			st, err = t.forward(ctx)
		case "reload":
			st, err = t.reload(ctx)
		default:
			st, err = t.getState(ctx)
		}
		if err != nil {
			return errResult(err), nil
		}
		b.mgr.emitSessionState(b.sessionID)
		return jsonResult("page_state", st, false), nil

	case "snapshot":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		snap, err := t.snapshot(ctx, args.Selector, args.MaxElements, args.IncludeHidden)
		if err != nil {
			return errResult(err), nil
		}
		b.mgr.emitSessionState(b.sessionID)
		return jsonResult("snapshot", snap, true), nil

	case "screenshot":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		b64, mime, err := t.captureScreenshot(ctx, args.FullPage)
		if err != nil {
			return errResult(err), nil
		}
		info := t.Info(true)
		meta, _ := json.Marshal(map[string]any{
			"tab_id":   t.TabID,
			"url":      info.URL,
			"viewport": info.Viewport,
		})
		return tools.Result{
			Output:      string(meta),
			ExtraBlocks: []ai.ContentBlock{ai.ImageBase64Block(mime, b64)},
			Metadata: map[string]any{
				"tab_id": t.TabID,
				"url":    info.URL,
			},
		}, nil

	case "console":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		return jsonResult("console", t.consoleEntries(args.Level), true), nil

	case "network":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		return jsonResult("network", t.networkEntries(args.FailedOnly), true), nil

	case "click", "dblclick":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		clicks := 1
		if action == "dblclick" {
			clicks = 2
		}
		msg, err := t.click(ctx, targetFromArgs(args), args.Button, clicks)
		if err != nil {
			return errResult(err), nil
		}
		st, _ := t.getState(ctx)
		b.mgr.emitSessionState(b.sessionID)
		return tools.Result{Output: fmt.Sprintf("%s; url=%s title=%q", msg, st.URL, st.Title)}, nil

	case "hover":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		if err := t.hover(ctx, targetFromArgs(args)); err != nil {
			return errResult(err), nil
		}
		return tools.Result{Output: "hovered target"}, nil

	case "fill":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		clearFirst := true
		if args.ClearFirst != nil {
			clearFirst = *args.ClearFirst
		}
		if err := t.fill(ctx, targetFromArgs(args), args.Text, clearFirst); err != nil {
			return errResult(err), nil
		}
		if args.Sensitive {
			return tools.Result{Output: "filled sensitive value [REDACTED]"}, nil
		}
		return tools.Result{Output: fmt.Sprintf("filled %d chars", len([]rune(args.Text)))}, nil

	case "type":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		if err := t.typeText(ctx, args.Text); err != nil {
			return errResult(err), nil
		}
		if args.Sensitive {
			return tools.Result{Output: "typed sensitive text [REDACTED]"}, nil
		}
		return tools.Result{Output: fmt.Sprintf("typed %d chars", len([]rune(args.Text)))}, nil

	case "press":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		if err := t.pressKey(ctx, args.Key); err != nil {
			return errResult(err), nil
		}
		return tools.Result{Output: fmt.Sprintf("pressed %s", args.Key)}, nil

	case "select":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		if err := t.selectOption(ctx, targetFromArgs(args), args.Values); err != nil {
			return errResult(err), nil
		}
		return tools.Result{Output: "updated select option(s)"}, nil

	case "check":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		want := true
		if args.Checked != nil {
			want = *args.Checked
		}
		if err := t.setChecked(ctx, targetFromArgs(args), want); err != nil {
			return errResult(err), nil
		}
		return tools.Result{Output: fmt.Sprintf("set checked=%v", want)}, nil

	case "scroll":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		if err := t.scroll(ctx, targetFromArgs(args), args.Direction, args.DeltaX, args.DeltaY); err != nil {
			return errResult(err), nil
		}
		st, _ := t.getState(ctx)
		return jsonResult("page_state", st, false), nil

	case "drag":
		if args.From == nil || args.To == nil {
			return errResult(newError(ErrExecutionError, "none", "from and to targets are required for drag")), nil
		}
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		if err := t.drag(ctx, *args.From, *args.To); err != nil {
			return errResult(err), nil
		}
		return tools.Result{Output: "drag completed"}, nil

	case "upload":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		if err := t.uploadFiles(ctx, workDir, targetFromArgs(args), args.Paths); err != nil {
			return errResult(err), nil
		}
		return tools.Result{Output: fmt.Sprintf("uploaded %d file(s)", len(args.Paths))}, nil

	case "handle_dialog":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		accept := true
		if args.Accept != nil {
			accept = *args.Accept
		}
		if err := t.handleDialog(ctx, accept, args.PromptText); err != nil {
			return errResult(err), nil
		}
		return tools.Result{Output: fmt.Sprintf("dialog handled (accept=%v)", accept)}, nil

	case "wait":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		if err := t.waitFor(ctx, args.For, args.Value, args.TimeoutMS); err != nil {
			return errResult(err), nil
		}
		return tools.Result{Output: "wait condition satisfied"}, nil

	case "eval":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		var out any
		if err := t.evalValue(ctx, args.Expression, &out); err != nil {
			return errResult(err), nil
		}
		return jsonResult("eval", out, true), nil

	case "set_viewport":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		if err := t.setViewport(ctx, args.Width, args.Height); err != nil {
			return errResult(err), nil
		}
		b.mgr.emitSessionState(b.sessionID)
		return tools.Result{Output: fmt.Sprintf("viewport set to %dx%d", args.Width, args.Height)}, nil

	case "request_handoff":
		t, err := b.mgr.getOrCreateTab(ctx, b.sessionID, args.TabID)
		if err != nil {
			return errResult(err), nil
		}
		reason := strings.TrimSpace(args.Reason)
		if reason == "" {
			reason = "Human action required in browser"
		}
		t.mu.Lock()
		t.humanControl = true
		t.mu.Unlock()
		resumed, hErr := b.mgr.handoff.RequestHandoff(ctx, b.sessionID, t.TabID, reason, args.Message)
		t.mu.Lock()
		t.humanControl = false
		t.mu.Unlock()
		if hErr != nil {
			return errResult(hErr), nil
		}
		if !resumed {
			return errResult(newError(ErrHandoffCancelled, "none", "user cancelled browser handoff")), nil
		}
		st, _ := t.getState(ctx)
		b.mgr.emitSessionState(b.sessionID)
		return jsonResult("handoff_resumed", st, false), nil

	default:
		return errResult(newError(ErrExecutionError, "none", fmt.Sprintf("unsupported browser action %q", args.Action))), nil
	}
}

func targetFromArgs(args browserArgs) TargetSpec {
	return TargetSpec{
		Ref:      args.Ref,
		Selector: args.Selector,
		Text:     args.Text,
		X:        args.X,
		Y:        args.Y,
	}
}

func errResult(err error) tools.Result {
	if be, ok := err.(*BrowserError); ok {
		b, _ := json.Marshal(map[string]any{"ok": false, "error": be})
		return tools.Result{Output: string(b), IsError: true}
	}
	b, _ := json.Marshal(map[string]any{"ok": false, "error": newError(ErrExecutionError, "uncertain", err.Error())})
	return tools.Result{Output: string(b), IsError: true}
}

func jsonResult(kind string, val any, untrusted bool) tools.Result {
	b, _ := json.Marshal(val)
	txt := string(b)
	if untrusted {
		txt = WrapUntrustedContent(kind, txt)
	}
	return tools.Result{Output: txt}
}
