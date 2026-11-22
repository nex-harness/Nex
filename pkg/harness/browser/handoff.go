package browser

import (
	"context"
	"sync"
)

type handoffWaiter struct {
	state HandoffState
	ch    chan bool // true = resumed, false = cancelled
}

// HandoffController coordinates pausing an Agent browser action until a human user
// finishes authentication / 2FA / CAPTCHA / payment and returns control.
type HandoffController struct {
	mu      sync.Mutex
	waiters map[string]*handoffWaiter
	onEmit  func(HandoffState)
}

func NewHandoffController(onEmit func(HandoffState)) *HandoffController {
	return &HandoffController{
		waiters: make(map[string]*handoffWaiter),
		onEmit:  onEmit,
	}
}

func (h *HandoffController) Current(sessionID string) *HandoffState {
	h.mu.Lock()
	defer h.mu.Unlock()
	w := h.waiters[sessionID]
	if w == nil {
		return nil
	}
	cp := w.state
	return &cp
}

func (h *HandoffController) RequestHandoff(ctx context.Context, sessionID, tabID, reason, message string) (bool, error) {
	st := HandoffState{
		SessionID: sessionID,
		TabID:     tabID,
		State:     "awaiting_human",
		Reason:    reason,
		Message:   message,
	}
	ch := make(chan bool, 1)

	h.mu.Lock()
	if prev := h.waiters[sessionID]; prev != nil {
		close(prev.ch)
	}
	h.waiters[sessionID] = &handoffWaiter{state: st, ch: ch}
	h.mu.Unlock()

	if h.onEmit != nil {
		h.onEmit(st)
	}

	defer func() {
		h.mu.Lock()
		if cur := h.waiters[sessionID]; cur != nil && cur.ch == ch {
			delete(h.waiters, sessionID)
		}
		h.mu.Unlock()
	}()

	select {
	case resumed, ok := <-ch:
		if !ok || !resumed {
			return false, newError(ErrHandoffCancelled, "none", "human handoff was cancelled")
		}
		return true, nil
	case <-ctx.Done():
		st.State = "cancelled"
		if h.onEmit != nil {
			h.onEmit(st)
		}
		return false, ctx.Err()
	}
}

func (h *HandoffController) Resolve(sessionID string, resumed bool) bool {
	h.mu.Lock()
	w := h.waiters[sessionID]
	if w == nil {
		h.mu.Unlock()
		return false
	}
	delete(h.waiters, sessionID)
	st := w.state
	if resumed {
		st.State = "resumed"
	} else {
		st.State = "cancelled"
	}
	select {
	case w.ch <- resumed:
	default:
	}
	h.mu.Unlock()

	if h.onEmit != nil {
		h.onEmit(st)
	}
	return true
}
