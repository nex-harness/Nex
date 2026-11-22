package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"
)

// StartScreencast activates CDP Page.startScreencast on the tab and streams compressed
// binary frames until StopScreencast is called.
func (t *TabSession) StartScreencast(ctx context.Context, quality, maxWidth, maxHeight int) error {
	t.touch()
	t.mu.Lock()
	if quality <= 0 || quality > 100 {
		quality = 72
	}
	if maxWidth <= 0 {
		maxWidth = t.viewport.Width
	}
	if maxHeight <= 0 {
		maxHeight = t.viewport.Height
	}
	t.streamActive = true
	t.mu.Unlock()

	return t.cdp.Call(ctx, t.CDPSessionID, "Page.startScreencast", map[string]any{
		"format":        "jpeg",
		"quality":       quality,
		"maxWidth":      maxWidth,
		"maxHeight":     maxHeight,
		"everyNthFrame": 1,
	}, nil)
}

func (t *TabSession) StopScreencast(ctx context.Context) error {
	t.mu.Lock()
	wasActive := t.streamActive
	t.streamActive = false
	t.mu.Unlock()
	if !wasActive {
		return nil
	}
	sCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return t.cdp.Call(sCtx, t.CDPSessionID, "Page.stopScreencast", nil, nil)
}

// HandleScreencastFrame decodes a CDP Page.screencastFrame event, enforces single-frame
// ack backpressure, and forwards the raw JPEG bytes to emitRawFrame.
func (t *TabSession) HandleScreencastFrame(rawParams json.RawMessage, emitRawFrame func(tabID string, jpeg []byte)) {
	var ev struct {
		Data      string `json:"data"`
		SessionID int64  `json:"sessionId"`
	}
	if json.Unmarshal(rawParams, &ev) != nil || ev.Data == "" {
		return
	}

	ack := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = t.cdp.Call(ctx, t.CDPSessionID, "Page.screencastFrameAck", map[string]any{
			"sessionId": ev.SessionID,
		}, nil)
	}

	t.mu.Lock()
	active := t.streamActive
	t.mu.Unlock()
	if !active {
		go ack()
		return
	}

	jpegBytes, err := base64.StdEncoding.DecodeString(ev.Data)
	if err == nil && len(jpegBytes) > 0 && emitRawFrame != nil {
		emitRawFrame(t.TabID, jpegBytes)
	}
	go ack()
}

// DispatchInput injects remote Web/mygo mouse, wheel, or keyboard events directly via CDP Input.
func (t *TabSession) DispatchInput(ctx context.Context, ev InputEvent) error {
	t.touch()
	switch ev.Kind {
	case "mouse":
		typ := ev.Type
		if typ == "" {
			typ = "mouseMoved"
		}
		btn := ev.Button
		if btn == "" {
			if typ == "mouseMoved" {
				btn = "none"
			} else {
				btn = "left"
			}
		}
		clicks := ev.ClickCount
		if clicks == 0 && (typ == "mousePressed" || typ == "mouseReleased") {
			clicks = 1
		}
		return t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
			"type":       typ,
			"x":          ev.X,
			"y":          ev.Y,
			"button":     btn,
			"clickCount": clicks,
			"modifiers":  ev.Modifiers,
		}, nil)

	case "wheel":
		return t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchMouseEvent", map[string]any{
			"type":      "mouseWheel",
			"x":         ev.X,
			"y":         ev.Y,
			"deltaX":    ev.DeltaX,
			"deltaY":    ev.DeltaY,
			"modifiers": ev.Modifiers,
		}, nil)

	case "key":
		typ := ev.Type
		if typ == "" {
			typ = "keyDown"
		}
		params := map[string]any{
			"type":      typ,
			"modifiers": ev.Modifiers,
		}
		if ev.Key != "" {
			params["key"] = ev.Key
			params["code"] = ev.Code
			if params["code"] == "" {
				params["code"] = keyCodeGuess(ev.Key)
			}
			if vk := virtualKeyCode(ev.Key); vk != 0 {
				params["windowsVirtualKeyCode"] = vk
			}
		}
		if ev.Text != "" {
			params["text"] = ev.Text
		}
		return t.cdp.Call(ctx, t.CDPSessionID, "Input.dispatchKeyEvent", params, nil)

	default:
		return newError(ErrExecutionError, "none", "unsupported input kind: "+ev.Kind)
	}
}
