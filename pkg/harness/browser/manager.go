package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/protocol"
)

type StateCallback func(SessionStatePayload)
type BinaryCallback func(frame []byte)

// Manager owns Chromium lifecycle, CDP multiplexing, per-session tabs, screencast streaming,
// and human handoff coordination for a Runner node.
type Manager struct {
	dataDir  string
	runnerID string
	hc       *http.Client

	mu                  sync.Mutex
	cfg                 Config
	proc                *ManagedProcess
	cdp                 *CDPClient
	bridge              *BridgeServer
	tabsBySession       map[string][]*TabSession
	activeTabBySession  map[string]string
	tabsByID            map[string]*TabSession
	tabsByCDP           map[string]*TabSession
	browserCtxBySession map[string]string
	lastUsedAt          time.Time

	handoff  *HandoffController
	onState  StateCallback
	onBinary BinaryCallback
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewManager(dataDir, runnerID string, hc *http.Client, onState StateCallback, onHandoff func(HandoffState), onBinary BinaryCallback) *Manager {
	m := &Manager{
		dataDir:             dataDir,
		runnerID:            runnerID,
		hc:                  hc,
		cfg:                 DefaultConfig(),
		tabsBySession:       make(map[string][]*TabSession),
		activeTabBySession:  make(map[string]string),
		tabsByID:            make(map[string]*TabSession),
		tabsByCDP:           make(map[string]*TabSession),
		browserCtxBySession: make(map[string]string),
		lastUsedAt:          time.Now(),
		onState:             onState,
		onBinary:            onBinary,
		stopCh:              make(chan struct{}),
	}
	m.handoff = NewHandoffController(func(st HandoffState) {
		if onHandoff != nil {
			onHandoff(st)
		}
		m.emitSessionState(st.SessionID)
	})
	go m.idleReapLoop()
	return m
}

func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Enabled
}

func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *Manager) UpdateConfig(next Config) {
	if next.Mode == "" {
		next.Mode = ModeManagedCDP
	}
	if next.ProfileMode == "" {
		next.ProfileMode = ProfilePersistent
	}
	if next.IdleTimeout <= 0 {
		next.IdleTimeout = DefaultIdleTimeout
	}

	m.mu.Lock()
	restartNeeded := !next.Enabled ||
		m.cfg.Mode != next.Mode ||
		m.cfg.Headless != next.Headless ||
		m.cfg.ProfileMode != next.ProfileMode ||
		m.cfg.AttachURL != next.AttachURL ||
		m.cfg.ExecutablePath != next.ExecutablePath
	m.cfg = next
	m.mu.Unlock()

	if restartNeeded {
		m.shutdownBackend()
	}
}

func (m *Manager) SessionTool(sessionID, workDir string) tools.Tool {
	return &BrowserTool{
		mgr:       m,
		sessionID: sessionID,
		workDir:   workDir,
	}
}

func (m *Manager) Close() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.mu.Lock()
	br := m.bridge
	m.bridge = nil
	m.mu.Unlock()
	if br != nil {
		br.Close()
	}
	m.shutdownBackend()
}

// ensureBridge lazily starts the local WebSocket BridgeServer for extension_bridge mode.
func (m *Manager) ensureBridge() (*BridgeServer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bridge != nil {
		return m.bridge, nil
	}
	addr := strings.TrimSpace(os.Getenv("NEX_BROWSER_BRIDGE_ADDR"))
	if addr == "" {
		addr = DefaultBridgeListenAddr
	}
	br, err := NewBridgeServer(addr)
	if err != nil {
		return nil, err
	}
	m.bridge = br
	return br, nil
}

func (m *Manager) idleReapLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.mu.Lock()
			hasProc := m.proc != nil || m.cdp != nil
			idleLimit := m.cfg.IdleTimeout
			if idleLimit <= 0 {
				idleLimit = DefaultIdleTimeout
			}
			stale := hasProc && time.Since(m.lastUsedAt) > idleLimit && !m.hasActiveStreamsOrHandoffLocked()
			m.mu.Unlock()
			if stale {
				m.shutdownBackend()
			}
		}
	}
}

func (m *Manager) hasActiveStreamsOrHandoffLocked() bool {
	for _, tab := range m.tabsByID {
		tab.mu.Lock()
		active := tab.streamActive
		tab.mu.Unlock()
		if active {
			return true
		}
	}
	for sid := range m.tabsBySession {
		if st := m.handoff.Current(sid); st != nil && st.State == "awaiting_human" {
			return true
		}
	}
	return false
}

func (m *Manager) shutdownBackend() {
	m.mu.Lock()
	cdp := m.cdp
	proc := m.proc
	m.cdp = nil
	m.proc = nil
	m.tabsBySession = make(map[string][]*TabSession)
	m.activeTabBySession = make(map[string]string)
	m.tabsByID = make(map[string]*TabSession)
	m.tabsByCDP = make(map[string]*TabSession)
	m.browserCtxBySession = make(map[string]string)
	m.mu.Unlock()

	if cdp != nil {
		cdp.Close()
	}
	if proc != nil {
		proc.Close()
	}
}

func (m *Manager) profileDir() string {
	if m.dataDir == "" {
		return ""
	}
	return filepath.Join(m.dataDir, "browser-profile")
}

func (m *Manager) ensureBackend(ctx context.Context) (*CDPClient, Config, error) {
	m.mu.Lock()
	cfg := m.cfg
	if !cfg.Enabled {
		m.mu.Unlock()
		return nil, cfg, newError(ErrCapabilityUnsupported, "none", "Browser Use is disabled in Settings -> Browser")
	}
	if m.cdp != nil {
		select {
		case <-m.cdp.Closed():
			m.cdp = nil
			if m.proc != nil {
				m.proc.Close()
				m.proc = nil
			}
		default:
			m.lastUsedAt = time.Now()
			cdp := m.cdp
			m.mu.Unlock()
			return cdp, cfg, nil
		}
	}
	m.mu.Unlock()

	var wsURL string
	var proc *ManagedProcess
	var err error

	if cfg.Mode == ModeAttachCDP {
		wsURL, err = DiscoverAttachWS(ctx, cfg.AttachURL)
		if err != nil {
			return nil, cfg, err
		}
	} else {
		execPath, _ := DiscoverBrowser(m.dataDir, cfg.ExecutablePath)
		if execPath == "" {
			return nil, cfg, newError(ErrBackendUnavailable, "none",
				"no Chrome/Chromium binary found on this Runner; install one in Settings -> Browser")
		}
		proc, wsURL, err = LaunchManaged(ctx, execPath, m.profileDir(), cfg.Headless)
		if err != nil {
			return nil, cfg, err
		}
	}

	cdp, err := DialCDP(ctx, wsURL)
	if err != nil {
		if proc != nil {
			proc.Close()
		}
		return nil, cfg, err
	}
	m.wireCDPEvents(cdp)

	m.mu.Lock()
	m.cdp = cdp
	m.proc = proc
	m.lastUsedAt = time.Now()
	m.mu.Unlock()
	return cdp, cfg, nil
}

func (m *Manager) wireCDPEvents(cdp *CDPClient) {
	cdp.OnEvent("Page.screencastFrame", func(cdpSessionID string, raw json.RawMessage) {
		m.mu.Lock()
		tab := m.tabsByCDP[cdpSessionID]
		m.mu.Unlock()
		if tab == nil {
			return
		}
		tab.HandleScreencastFrame(raw, func(tabID string, jpeg []byte) {
			if m.onBinary != nil {
				m.onBinary(protocol.EncodeBrowserBinaryFrame(tabID, jpeg))
			}
		})
	})

	cdp.OnEvent("Runtime.consoleAPICalled", func(cdpSessionID string, raw json.RawMessage) {
		m.mu.Lock()
		tab := m.tabsByCDP[cdpSessionID]
		m.mu.Unlock()
		if tab == nil {
			return
		}
		var ev struct {
			Type string `json:"type"`
			Args []struct {
				Value       any    `json:"value"`
				Description string `json:"description"`
			} `json:"args"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		var parts []string
		for _, a := range ev.Args {
			if a.Description != "" {
				parts = append(parts, a.Description)
			} else if a.Value != nil {
				parts = append(parts, fmt.Sprint(a.Value))
			}
		}
		tab.pushConsole(ConsoleEntry{
			Level:     ev.Type,
			Text:      strings.Join(parts, " "),
			Timestamp: time.Now().UnixMilli(),
		})
	})

	cdp.OnEvent("Network.responseReceived", func(cdpSessionID string, raw json.RawMessage) {
		m.mu.Lock()
		tab := m.tabsByCDP[cdpSessionID]
		m.mu.Unlock()
		if tab == nil {
			return
		}
		var ev struct {
			Type     string `json:"type"`
			Response struct {
				URL    string `json:"url"`
				Status int    `json:"status"`
			} `json:"response"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		tab.pushNetwork(NetworkEntry{
			URL:       ev.Response.URL,
			Status:    ev.Response.Status,
			Type:      ev.Type,
			Timestamp: time.Now().UnixMilli(),
		})
	})

	cdp.OnEvent("Network.loadingFailed", func(cdpSessionID string, raw json.RawMessage) {
		m.mu.Lock()
		tab := m.tabsByCDP[cdpSessionID]
		m.mu.Unlock()
		if tab == nil {
			return
		}
		var ev struct {
			Type      string `json:"type"`
			ErrorText string `json:"errorText"`
		}
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		tab.pushNetwork(NetworkEntry{
			ErrorText: ev.ErrorText,
			Type:      ev.Type,
			Timestamp: time.Now().UnixMilli(),
		})
	})

	cdp.OnEvent("Page.javascriptDialogOpening", func(cdpSessionID string, raw json.RawMessage) {
		m.mu.Lock()
		tab := m.tabsByCDP[cdpSessionID]
		m.mu.Unlock()
		if tab == nil {
			return
		}
		var ev struct {
			Type          string `json:"type"`
			Message       string `json:"message"`
			DefaultPrompt string `json:"defaultPrompt"`
		}
		if json.Unmarshal(raw, &ev) == nil {
			tab.mu.Lock()
			tab.dialog = &DialogInfo{Type: ev.Type, Message: ev.Message, DefaultPrompt: ev.DefaultPrompt}
			tab.mu.Unlock()
		}
	})

	cdp.OnEvent("Page.frameNavigated", func(cdpSessionID string, raw json.RawMessage) {
		m.mu.Lock()
		tab := m.tabsByCDP[cdpSessionID]
		m.mu.Unlock()
		if tab == nil {
			return
		}
		var ev struct {
			Frame struct {
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
			} `json:"frame"`
		}
		if json.Unmarshal(raw, &ev) == nil && ev.Frame.ParentID == "" && ev.Frame.URL != "" {
			tab.mu.Lock()
			tab.url = ev.Frame.URL
			tab.mu.Unlock()
			m.emitSessionState(tab.OwnerSessionID)
		}
	})
}

func (m *Manager) createTab(ctx context.Context, sessionID string) (*TabSession, error) {
	cdp, cfg, err := m.ensureBackend(ctx)
	if err != nil {
		return nil, err
	}

	var bctxID string
	if cfg.ProfileMode == ProfileEphemeral {
		m.mu.Lock()
		bctxID = m.browserCtxBySession[sessionID]
		m.mu.Unlock()
		if bctxID == "" {
			var bRes struct {
				BrowserContextID string `json:"browserContextId"`
			}
			if cErr := cdp.Call(ctx, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": true}, &bRes); cErr == nil && bRes.BrowserContextID != "" {
				bctxID = bRes.BrowserContextID
				m.mu.Lock()
				m.browserCtxBySession[sessionID] = bctxID
				m.mu.Unlock()
			}
		}
	}

	createParams := map[string]any{
		"url": "about:blank",
	}
	if bctxID != "" {
		createParams["browserContextId"] = bctxID
	}
	var tRes struct {
		TargetID string `json:"targetId"`
	}
	if err := cdp.Call(ctx, "", "Target.createTarget", createParams, &tRes); err != nil {
		return nil, err
	}

	var aRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := cdp.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": tRes.TargetID,
		"flatten":  true,
	}, &aRes); err != nil {
		return nil, err
	}

	tabID := "bt_" + uuid.NewString()[:28]
	tab := newTabSession(tabID, tRes.TargetID, aRes.SessionID, bctxID, sessionID, cdp)

	m.mu.Lock()
	m.tabsBySession[sessionID] = append(m.tabsBySession[sessionID], tab)
	m.activeTabBySession[sessionID] = tabID
	m.tabsByID[tabID] = tab
	m.tabsByCDP[aRes.SessionID] = tab
	m.mu.Unlock()

	if err := tab.initDomains(ctx); err != nil {
		_ = m.closeTab(ctx, sessionID, tabID)
		return nil, err
	}
	m.emitSessionState(sessionID)
	return tab, nil
}

func (m *Manager) getOrCreateTab(ctx context.Context, sessionID, tabID string) (*TabSession, error) {
	m.mu.Lock()
	if tabID != "" {
		if t := m.tabsByID[tabID]; t != nil && t.OwnerSessionID == sessionID {
			m.lastUsedAt = time.Now()
			m.mu.Unlock()
			return t, nil
		}
		m.mu.Unlock()
		return nil, newError(ErrExecutionError, "none", fmt.Sprintf("tab %q not found in session", tabID))
	}
	if activeID := m.activeTabBySession[sessionID]; activeID != "" {
		if t := m.tabsByID[activeID]; t != nil {
			m.lastUsedAt = time.Now()
			m.mu.Unlock()
			return t, nil
		}
	}
	list := m.tabsBySession[sessionID]
	if len(list) > 0 {
		t := list[len(list)-1]
		m.activeTabBySession[sessionID] = t.TabID
		m.lastUsedAt = time.Now()
		m.mu.Unlock()
		return t, nil
	}
	m.mu.Unlock()

	return m.createTab(ctx, sessionID)
}

func (m *Manager) findReusableSameSiteTab(sessionID, targetURL string) *TabSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tabsBySession[sessionID] {
		t.mu.Lock()
		u := t.url
		t.mu.Unlock()
		if isSameSiteURL(u, targetURL) {
			return t
		}
	}
	return nil
}

func (m *Manager) openURL(ctx context.Context, sessionID, rawURL string, forceNewTab bool) (*TabSession, PageState, error) {
	_, cfg, err := m.ensureBackend(ctx)
	if err != nil {
		return nil, PageState{}, err
	}
	if err := ValidateNavigationURL(rawURL, cfg); err != nil {
		return nil, PageState{}, err
	}

	var tab *TabSession
	if !forceNewTab {
		tab = m.findReusableSameSiteTab(sessionID, rawURL)
	}
	if tab == nil {
		if forceNewTab {
			tab, err = m.createTab(ctx, sessionID)
		} else {
			tab, err = m.getOrCreateTab(ctx, sessionID, "")
		}
		if err != nil {
			return nil, PageState{}, err
		}
	}
	m.mu.Lock()
	m.activeTabBySession[sessionID] = tab.TabID
	m.mu.Unlock()

	state, err := tab.navigate(ctx, rawURL, cfg)
	m.emitSessionState(sessionID)
	return tab, state, err
}

func (m *Manager) listTabs(sessionID string) []TabInfo {
	m.mu.Lock()
	activeID := m.activeTabBySession[sessionID]
	list := append([]*TabSession(nil), m.tabsBySession[sessionID]...)
	m.mu.Unlock()

	out := make([]TabInfo, 0, len(list))
	for _, t := range list {
		out = append(out, t.Info(t.TabID == activeID))
	}
	return out
}

func (m *Manager) switchTab(ctx context.Context, sessionID, tabID string) (*TabSession, error) {
	m.mu.Lock()
	t := m.tabsByID[tabID]
	if t == nil || t.OwnerSessionID != sessionID {
		m.mu.Unlock()
		return nil, newError(ErrExecutionError, "none", fmt.Sprintf("tab %q not found", tabID))
	}
	m.activeTabBySession[sessionID] = tabID
	cdp := m.cdp
	m.mu.Unlock()

	if cdp != nil {
		_ = cdp.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": t.TargetID}, nil)
	}
	m.emitSessionState(sessionID)
	return t, nil
}

func (m *Manager) closeTab(ctx context.Context, sessionID, tabID string) error {
	m.mu.Lock()
	t := m.tabsByID[tabID]
	if t == nil || (sessionID != "" && t.OwnerSessionID != sessionID) {
		m.mu.Unlock()
		return newError(ErrExecutionError, "none", fmt.Sprintf("tab %q not found", tabID))
	}
	owner := t.OwnerSessionID
	delete(m.tabsByID, tabID)
	delete(m.tabsByCDP, t.CDPSessionID)

	oldList := m.tabsBySession[owner]
	nextList := make([]*TabSession, 0, len(oldList))
	for _, item := range oldList {
		if item.TabID != tabID {
			nextList = append(nextList, item)
		}
	}
	m.tabsBySession[owner] = nextList
	if m.activeTabBySession[owner] == tabID {
		if len(nextList) > 0 {
			m.activeTabBySession[owner] = nextList[len(nextList)-1].TabID
		} else {
			delete(m.activeTabBySession, owner)
		}
	}
	cdp := m.cdp
	m.mu.Unlock()

	_ = t.StopScreencast(ctx)
	if cdp != nil {
		_ = cdp.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": t.TargetID}, nil)
	}
	m.emitSessionState(owner)
	return nil
}

func (m *Manager) SessionState(sessionID string) SessionStatePayload {
	tabs := m.listTabs(sessionID)
	m.mu.Lock()
	activeID := m.activeTabBySession[sessionID]
	m.mu.Unlock()

	human := false
	for _, t := range tabs {
		if t.TabID == activeID && t.HumanControl {
			human = true
			break
		}
	}
	return SessionStatePayload{
		SessionID:    sessionID,
		RunnerID:     m.runnerID,
		Tabs:         tabs,
		ActiveTabID:  activeID,
		HumanControl: human,
		Handoff:      m.handoff.Current(sessionID),
	}
}

func (m *Manager) emitSessionState(sessionID string) {
	if m.onState == nil || sessionID == "" {
		return
	}
	m.onState(m.SessionState(sessionID))
}

func (m *Manager) Probe(ctx context.Context) ProbeResult {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	return Probe(ctx, m.dataDir, cfg)
}

func (m *Manager) Install(ctx context.Context, mirrorOverride string) (ProbeResult, error) {
	m.mu.Lock()
	mirror := strings.TrimSpace(mirrorOverride)
	if mirror == "" {
		mirror = m.cfg.DownloadMirror
	}
	m.mu.Unlock()
	return InstallHeadlessShell(ctx, m.dataDir, mirror, m.hc, nil)
}

func (m *Manager) ClearData(mode string) error {
	pdir := m.profileDir()
	if pdir == "" {
		return nil
	}
	if mode == "cache" {
		for _, sub := range []string{
			filepath.Join(pdir, "Default", "Cache"),
			filepath.Join(pdir, "Default", "Code Cache"),
			filepath.Join(pdir, "Default", "GPUCache"),
		} {
			_ = os.RemoveAll(sub)
		}
		return nil
	}
	m.shutdownBackend()
	return os.RemoveAll(pdir)
}

func (m *Manager) HandleControl(ctx context.Context, req ControlRequest) (SessionStatePayload, error) {
	sid := strings.TrimSpace(req.SessionID)
	if sid == "" {
		return SessionStatePayload{}, fmt.Errorf("session_id is required")
	}

	switch req.Action {
	case "navigate":
		if _, _, err := m.openURL(ctx, sid, req.URL, false); err != nil {
			return SessionStatePayload{}, err
		}
	case "new_tab":
		t, err := m.createTab(ctx, sid)
		if err != nil {
			return SessionStatePayload{}, err
		}
		if req.URL != "" {
			if _, err := t.navigate(ctx, req.URL, m.Config()); err != nil {
				return SessionStatePayload{}, err
			}
		}
	case "switch_tab":
		if _, err := m.switchTab(ctx, sid, req.TabID); err != nil {
			return SessionStatePayload{}, err
		}
	case "close_tab":
		if err := m.closeTab(ctx, sid, req.TabID); err != nil {
			return SessionStatePayload{}, err
		}
	case "back":
		t, err := m.getOrCreateTab(ctx, sid, req.TabID)
		if err != nil {
			return SessionStatePayload{}, err
		}
		if _, err := t.back(ctx); err != nil {
			return SessionStatePayload{}, err
		}
	case "forward":
		t, err := m.getOrCreateTab(ctx, sid, req.TabID)
		if err != nil {
			return SessionStatePayload{}, err
		}
		if _, err := t.forward(ctx); err != nil {
			return SessionStatePayload{}, err
		}
	case "reload":
		t, err := m.getOrCreateTab(ctx, sid, req.TabID)
		if err != nil {
			return SessionStatePayload{}, err
		}
		if _, err := t.reload(ctx); err != nil {
			return SessionStatePayload{}, err
		}
	case "set_viewport":
		t, err := m.getOrCreateTab(ctx, sid, req.TabID)
		if err != nil {
			return SessionStatePayload{}, err
		}
		if err := t.setViewport(ctx, req.Width, req.Height); err != nil {
			return SessionStatePayload{}, err
		}
	case "stream_start":
		t, err := m.getOrCreateTab(ctx, sid, req.TabID)
		if err != nil {
			return SessionStatePayload{}, err
		}
		if err := t.StartScreencast(ctx, req.Quality, req.MaxWidth, req.MaxHeight); err != nil {
			return SessionStatePayload{}, err
		}
	case "stream_stop":
		m.mu.Lock()
		t := m.tabsByID[req.TabID]
		m.mu.Unlock()
		if t != nil {
			_ = t.StopScreencast(ctx)
		}
	case "takeover":
		t, err := m.getOrCreateTab(ctx, sid, req.TabID)
		if err != nil {
			return SessionStatePayload{}, err
		}
		t.mu.Lock()
		t.humanControl = true
		t.mu.Unlock()
	case "handback":
		t, err := m.getOrCreateTab(ctx, sid, req.TabID)
		if err != nil {
			return SessionStatePayload{}, err
		}
		t.mu.Lock()
		t.humanControl = false
		t.mu.Unlock()
		m.handoff.Resolve(sid, true)
	default:
		return SessionStatePayload{}, fmt.Errorf("unknown browser control action %q", req.Action)
	}

	st := m.SessionState(sid)
	m.emitSessionState(sid)
	return st, nil
}

func (m *Manager) HandleInput(ctx context.Context, ev InputEvent) error {
	m.mu.Lock()
	var t *TabSession
	if ev.TabID != "" {
		t = m.tabsByID[ev.TabID]
	} else if activeID := m.activeTabBySession[ev.SessionID]; activeID != "" {
		t = m.tabsByID[activeID]
	}
	m.mu.Unlock()
	if t == nil {
		return nil
	}
	return t.DispatchInput(ctx, ev)
}

func (m *Manager) HandleHandoffResolution(sessionID string, resumed bool) bool {
	return m.handoff.Resolve(sessionID, resumed)
}
