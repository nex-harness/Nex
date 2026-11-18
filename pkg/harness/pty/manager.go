package pty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/protocol"
)

const defaultRingBufferBytes = 128 * 1024

type platformPTY interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
	Wait() error
}

// SessionInfo describes an active remote PTY session on the Runner.
type SessionInfo struct {
	PTYID     string `json:"pty_id"`
	Title     string `json:"title"`
	Cwd       string `json:"cwd"`
	Shell     string `json:"shell"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
	CreatedAt int64  `json:"created_at"`
}

type ptySession struct {
	info    SessionInfo
	handle  platformPTY
	ring    *RingBuffer
	closed  chan struct{}
	closeMu sync.Once
}

// Manager manages persistent interactive PTY sessions on a Runner.
type Manager struct {
	mu          sync.RWMutex
	sessions    map[string]*ptySession
	defaultWork string
}

// NewManager creates a remote PTY Manager.
func NewManager(defaultWorkDir string) *Manager {
	return &Manager{
		sessions:    make(map[string]*ptySession),
		defaultWork: defaultWorkDir,
	}
}

type openPayload struct {
	PTYID        string `json:"pty_id,omitempty"` // If set and exists, re-attaches and replays ring buffer!
	WorkspaceCwd string `json:"workspace_cwd,omitempty"`
	Shell        string `json:"shell,omitempty"`
	Title        string `json:"title,omitempty"`
	Cols         uint16 `json:"cols"`
	Rows         uint16 `json:"rows"`
}

type openedPayload struct {
	SessionInfo
	History []byte `json:"history,omitempty"`
}

type resizePayload struct {
	PTYID string `json:"pty_id"`
	Cols  uint16 `json:"cols"`
	Rows  uint16 `json:"rows"`
}

type closePayload struct {
	PTYID string `json:"pty_id"`
}

// HandleControl processes pty.open, pty.resize, pty.close, and pty.list envelopes.
func (m *Manager) HandleControl(
	_ context.Context,
	env protocol.Envelope,
	sendBinary func(frame []byte),
	sendClosed func(ptyID string),
) (protocol.Envelope, error) {
	switch env.Type {
	case protocol.TypePTYOpen:
		var req openPayload
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			return protocol.Envelope{}, err
		}
		if req.Cols == 0 {
			req.Cols = 100
		}
		if req.Rows == 0 {
			req.Rows = 30
		}

		// Re-attach to existing PTY session if requested and still alive!
		if req.PTYID != "" {
			m.mu.RLock()
			existing := m.sessions[req.PTYID]
			m.mu.RUnlock()
			if existing != nil {
				_ = existing.handle.Resize(req.Cols, req.Rows)
				return protocol.NewEnvelope(protocol.TypePTYOpened, openedPayload{
					SessionInfo: existing.info,
					History:     existing.ring.Snapshot(),
				})
			}
		}

		cwd := req.WorkspaceCwd
		if cwd == "" {
			cwd = m.defaultWork
		}
		ptyID := uuid.NewString() // 36-char UUID fits fixed 36-byte binary header cleanly!
		h, shellUsed, err := startPlatformPTY(cwd, req.Shell, req.Cols, req.Rows)
		if err != nil {
			return protocol.Envelope{}, fmt.Errorf("pty open failed: %w", err)
		}

		title := req.Title
		if title == "" {
			title = "Nex"
		}
		sess := &ptySession{
			info: SessionInfo{
				PTYID:     ptyID,
				Title:     title,
				Cwd:       cwd,
				Shell:     shellUsed,
				Cols:      req.Cols,
				Rows:      req.Rows,
				CreatedAt: time.Now().UnixMilli(),
			},
			handle: h,
			ring:   NewRingBuffer(defaultRingBufferBytes),
			closed: make(chan struct{}),
		}

		m.mu.Lock()
		m.sessions[ptyID] = sess
		m.mu.Unlock()

		// Stream stdout/stderr bytes over binary fast-path and record into RingBuffer
		go func() {
			buf := make([]byte, 16*1024)
			for {
				n, rErr := h.Read(buf)
				if n > 0 {
					chunk := buf[:n]
					_, _ = sess.ring.Write(chunk)
					if sendBinary != nil {
						sendBinary(protocol.EncodePTYBinaryFrame(protocol.PTYBinaryStdout, ptyID, chunk))
					}
				}
				if rErr != nil {
					break
				}
			}
			_ = h.Wait()
			m.removeAndClose(ptyID)
			if sendClosed != nil {
				sendClosed(ptyID)
			}
		}()

		return protocol.NewEnvelope(protocol.TypePTYOpened, openedPayload{
			SessionInfo: sess.info,
		})

	case protocol.TypePTYResize:
		var req resizePayload
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			return protocol.Envelope{}, err
		}
		m.mu.Lock()
		sess := m.sessions[req.PTYID]
		if sess != nil && req.Cols > 0 && req.Rows > 0 {
			sess.info.Cols = req.Cols
			sess.info.Rows = req.Rows
			_ = sess.handle.Resize(req.Cols, req.Rows)
		}
		m.mu.Unlock()
		return protocol.NewEnvelope(protocol.TypePTYResize, map[string]any{"ok": true})

	case protocol.TypePTYClose:
		var req closePayload
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			return protocol.Envelope{}, err
		}
		m.removeAndClose(req.PTYID)
		return protocol.NewEnvelope(protocol.TypePTYClosed, map[string]string{"pty_id": req.PTYID})

	case protocol.TypePTYList:
		m.mu.RLock()
		list := make([]SessionInfo, 0, len(m.sessions))
		for _, s := range m.sessions {
			list = append(list, s.info)
		}
		m.mu.RUnlock()
		return protocol.NewEnvelope(protocol.TypePTYListResp, map[string]any{"sessions": list})

	default:
		return protocol.Envelope{}, fmt.Errorf("unsupported PTY control type %q", env.Type)
	}
}

// WriteStdin writes raw terminal keystrokes into an active PTY session.
func (m *Manager) WriteStdin(ptyID string, data []byte) error {
	m.mu.RLock()
	sess := m.sessions[ptyID]
	m.mu.RUnlock()
	if sess == nil {
		return errors.New("pty session not found: " + ptyID)
	}
	_, err := sess.handle.Write(data)
	return err
}

func (m *Manager) removeAndClose(ptyID string) {
	m.mu.Lock()
	sess := m.sessions[ptyID]
	delete(m.sessions, ptyID)
	m.mu.Unlock()
	if sess != nil {
		sess.closeMu.Do(func() {
			close(sess.closed)
			_ = sess.handle.Close()
		})
	}
}

// CloseAll terminates all active PTY sessions on Runner shutdown.
func (m *Manager) CloseAll() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		m.removeAndClose(id)
	}
}
