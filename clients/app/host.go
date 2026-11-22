package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/nex-harness/nex/pkg/remote"
)

// Typed events emitted from the native mygo host to the web frontend.
var (
	EventOpenSession   = mygo.NewEvent[string]("nex:open-session")
	EventAppResume     = mygo.NewEvent[int64]("nex:app-resume")
	EventOpenSettings  = mygo.NewEvent[string]("nex:open-settings")
	EventNewTask       = mygo.NewEvent[bool]("nex:new-task")
	EventServerChanged = mygo.NewEvent[HostInfo]("nex:server-changed")
	EventRemoteStatus  = mygo.NewEvent[remote.TransportStatus]("nex:remote-status")
	EventPairInvite    = mygo.NewEvent[string]("nex:pair-invite")
)

// HostInfo describes the active mygo runtime container and connection target.
type HostInfo struct {
	Platform               string                     `json:"platform"`
	FormFactor             string                     `json:"form_factor"`
	Version                string                     `json:"version"`
	ConnectionMode         string                     `json:"connection_mode"`
	ServerURL              string                     `json:"server_url"`
	LocalURL               string                     `json:"local_url,omitempty"`
	ActiveNodeID           string                     `json:"active_node_id,omitempty"`
	ActiveHostName         string                     `json:"active_host_name,omitempty"`
	ActiveTransport        string                     `json:"active_transport,omitempty"`
	PairedHosts            []remote.PairedHostProfile `json:"paired_hosts"`
	SupportsLocalMode      bool                       `json:"supports_local_mode"`
	NotificationsSupported bool                       `json:"notifications_supported"`
	RecentServers          []string                   `json:"recent_servers"`
}

// SecretStore persists sensitive per-server tokens.
type SecretStore interface {
	Get(key string) ([]byte, error)
	Set(key string, val []byte) error
	Delete(key string) error
}

type localInstance interface {
	Close() error
}

// HostServiceOptions configures a HostService instance.
type HostServiceOptions struct {
	ConfigDir    string
	WorkspaceDir string
	LocalAddr    string
	Secrets      SecretStore
}

type persistedHostConfig struct {
	ConnectionMode string                     `json:"connection_mode"`
	ServerURL      string                     `json:"server_url"`
	ActiveNodeID   string                     `json:"active_node_id,omitempty"`
	PairedHosts    []remote.PairedHostProfile `json:"paired_hosts,omitempty"`
	RecentServers  []string                   `json:"recent_servers,omitempty"`
}

const maxRecentServers = 8

// HostService exposes typed IPC methods bound as "NexHost" via mygo.BindAs
// across Linux, macOS, Windows, iOS, and Android.
type HostService struct {
	mu           sync.Mutex
	configDir    string
	workspaceDir string
	localAddr    string
	secrets      SecretStore
	cfg          persistedHostConfig
	localInst    localInstance
	localURL     string
	remoteProxy  *remote.LocalProxy
	pushToken    string
	appReady     bool
}

// NewHostService loads persisted host configuration and initializes secret storage.
func NewHostService(opts HostServiceOptions) *HostService {
	cfgDir := strings.TrimSpace(opts.ConfigDir)
	if cfgDir == "" {
		home, _ := os.UserHomeDir()
		cfgDir = filepath.Join(home, ".nex-client")
	}
	_ = os.MkdirAll(cfgDir, 0o700)

	sec := opts.Secrets
	if sec == nil {
		sec = &mygoSecretStore{
			fallback: &fileSecretStore{path: filepath.Join(cfgDir, "secrets.json")},
		}
	}

	localAddr := strings.TrimSpace(opts.LocalAddr)
	if localAddr == "" {
		localAddr = "127.0.0.1:0"
	}

	h := &HostService{
		configDir:    cfgDir,
		workspaceDir: strings.TrimSpace(opts.WorkspaceDir),
		localAddr:    localAddr,
		secrets:      sec,
	}
	h.loadConfigLocked()
	return h
}

func (h *HostService) configFilePath() string {
	return filepath.Join(h.configDir, "host_config.json")
}

func (h *HostService) loadConfigLocked() {
	raw, err := os.ReadFile(h.configFilePath())
	if err == nil {
		var parsed persistedHostConfig
		if json.Unmarshal(raw, &parsed) == nil {
			if parsed.ServerURL != "" {
				if norm, nErr := NormalizeServerURL(parsed.ServerURL); nErr == nil {
					parsed.ServerURL = norm
				} else {
					parsed.ServerURL = ""
				}
			}
			parsed.RecentServers = cleanRecentServers(parsed.RecentServers)
			switch parsed.ConnectionMode {
			case "local":
				if !supportsLocalMode {
					parsed.ConnectionMode = "unconfigured"
				}
			case "remote":
				if parsed.ServerURL == "" {
					parsed.ConnectionMode = "unconfigured"
				}
			case "paired":
				if parsed.ActiveNodeID == "" || findPairedHost(parsed.PairedHosts, parsed.ActiveNodeID) == nil {
					parsed.ActiveNodeID = ""
					if parsed.ServerURL != "" {
						parsed.ConnectionMode = "remote"
					} else if supportsLocalMode {
						parsed.ConnectionMode = "local"
					} else {
						parsed.ConnectionMode = "unconfigured"
					}
				}
			default:
				if parsed.ActiveNodeID != "" && findPairedHost(parsed.PairedHosts, parsed.ActiveNodeID) != nil {
					parsed.ConnectionMode = "paired"
				} else if parsed.ServerURL != "" {
					parsed.ConnectionMode = "remote"
				} else if supportsLocalMode {
					parsed.ConnectionMode = "local"
				} else {
					parsed.ConnectionMode = "unconfigured"
				}
			}
			h.cfg = parsed
			return
		}
	}

	if supportsLocalMode {
		h.cfg = persistedHostConfig{ConnectionMode: "local"}
	} else {
		h.cfg = persistedHostConfig{ConnectionMode: "unconfigured"}
	}
}

func (h *HostService) saveConfigLocked() error {
	raw, err := json.MarshalIndent(h.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(h.configFilePath(), raw, 0o600)
}

// startupMode returns the initial connection mode before the window opens.
func (h *HostService) startupMode() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.ConnectionMode
}

// ensureStartupConnected boots the embedded local mode or auto-connects the active paired remote host.
func (h *HostService) ensureStartupConnected(ctx context.Context) {
	switch h.startupMode() {
	case "local":
		if _, err := h.ensureLocalStarted(ctx); err != nil {
			// Logged by caller if needed; keep window responsive even if startup fails.
			_ = err
		}
	case "paired":
		h.mu.Lock()
		nid := h.cfg.ActiveNodeID
		h.mu.Unlock()
		if nid != "" {
			_, _ = h.ConnectPairedHost(ctx, nid)
		}
	}
}

// ensureLocalStarted boots the desktop All-in-One embedded server + runner if configured for local mode.
func (h *HostService) ensureLocalStarted(ctx context.Context) (HostInfo, error) {
	h.mu.Lock()
	if !supportsLocalMode {
		info := h.infoLocked()
		h.mu.Unlock()
		return info, errors.ErrUnsupported
	}
	oldProxy := h.detachRemoteProxyLocked()
	if _, err := h.startLocalBackendLocked(ctx); err != nil {
		info := h.infoLocked()
		h.mu.Unlock()
		closeRemoteProxy(oldProxy)
		return info, err
	}
	h.cfg.ConnectionMode = "local"
	h.cfg.ActiveNodeID = ""
	_ = h.saveConfigLocked()
	info := h.infoLocked()
	h.mu.Unlock()
	closeRemoteProxy(oldProxy)
	_ = EventServerChanged.Broadcast(info)
	return info, nil
}

// Info returns runtime host metadata and current connection state.
func (h *HostService) Info() HostInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.infoLocked()
}

func (h *HostService) infoLocked() HostInfo {
	effectiveURL := h.cfg.ServerURL
	activeHostName := ""
	activeTransport := ""
	switch h.cfg.ConnectionMode {
	case "local":
		effectiveURL = h.localURL
	case "paired":
		if prof := findPairedHost(h.cfg.PairedHosts, h.cfg.ActiveNodeID); prof != nil {
			activeHostName = prof.HostName
		}
		if h.remoteProxy != nil {
			effectiveURL = h.remoteProxy.LocalURL()
			activeTransport = h.remoteProxy.Status().Transport
		} else {
			effectiveURL = ""
			activeTransport = "disconnected"
		}
	}
	ver := mygo.App.Version()
	if ver == "" {
		ver = "0.3.0"
	}
	recent := make([]string, len(h.cfg.RecentServers))
	copy(recent, h.cfg.RecentServers)
	paired := make([]remote.PairedHostProfile, len(h.cfg.PairedHosts))
	copy(paired, h.cfg.PairedHosts)
	notifSupported := false
	if h.appReady {
		notifSupported = mygo.NotificationsSupported()
	}
	return HostInfo{
		Platform:               PlatformName(),
		FormFactor:             FormFactorName(),
		Version:                ver,
		ConnectionMode:         h.cfg.ConnectionMode,
		ServerURL:              effectiveURL,
		LocalURL:               h.localURL,
		ActiveNodeID:           h.cfg.ActiveNodeID,
		ActiveHostName:         activeHostName,
		ActiveTransport:        activeTransport,
		PairedHosts:            paired,
		SupportsLocalMode:      supportsLocalMode,
		NotificationsSupported: notifSupported,
		RecentServers:          recent,
	}
}

// GetServerURL returns the active Nex server base URL, or "" if unconfigured.
func (h *HostService) GetServerURL() string {
	return h.Info().ServerURL
}

// SetServerURL validates rawURL, persists remote mode, stops any local embedded backend, and notifies windows.
func (h *HostService) SetServerURL(rawURL string) (HostInfo, error) {
	norm, err := NormalizeServerURL(rawURL)
	if err != nil {
		return HostInfo{}, err
	}
	h.mu.Lock()
	h.stopLocalBackendLocked()
	oldProxy := h.detachRemoteProxyLocked()
	h.cfg.ConnectionMode = "remote"
	h.cfg.ServerURL = norm
	h.cfg.ActiveNodeID = ""
	h.cfg.RecentServers = prependRecentServer(h.cfg.RecentServers, norm)
	saveErr := h.saveConfigLocked()
	info := h.infoLocked()
	h.mu.Unlock()
	closeRemoteProxy(oldProxy)
	if saveErr != nil {
		return HostInfo{}, saveErr
	}
	_ = EventServerChanged.Broadcast(info)
	return info, nil
}

// SwitchMode switches between "local" (desktop embedded all-in-one) and "remote" (external nex-server).
func (h *HostService) SwitchMode(ctx context.Context, mode string, serverURL string) (HostInfo, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "local":
		if !supportsLocalMode {
			return HostInfo{}, errors.ErrUnsupported
		}
		h.mu.Lock()
		oldProxy := h.detachRemoteProxyLocked()
		if _, err := h.startLocalBackendLocked(ctx); err != nil {
			h.mu.Unlock()
			closeRemoteProxy(oldProxy)
			return HostInfo{}, err
		}
		h.cfg.ConnectionMode = "local"
		h.cfg.ActiveNodeID = ""
		err := h.saveConfigLocked()
		info := h.infoLocked()
		h.mu.Unlock()
		closeRemoteProxy(oldProxy)
		if err != nil {
			return HostInfo{}, err
		}
		_ = EventServerChanged.Broadcast(info)
		return info, nil
	case "remote":
		trimmed := strings.TrimSpace(serverURL)
		if trimmed != "" {
			return h.SetServerURL(trimmed)
		}
		h.mu.Lock()
		h.stopLocalBackendLocked()
		oldProxy := h.detachRemoteProxyLocked()
		if h.cfg.ServerURL != "" {
			h.cfg.ConnectionMode = "remote"
		} else {
			h.cfg.ConnectionMode = "unconfigured"
		}
		h.cfg.ActiveNodeID = ""
		err := h.saveConfigLocked()
		info := h.infoLocked()
		h.mu.Unlock()
		closeRemoteProxy(oldProxy)
		if err != nil {
			return HostInfo{}, err
		}
		_ = EventServerChanged.Broadcast(info)
		return info, nil
	default:
		return HostInfo{}, fmt.Errorf("unsupported connection mode %q", mode)
	}
}

// GetAuthToken retrieves the stored bearer token for serverURL (or default if empty).
func (h *HostService) GetAuthToken(serverURL string) (string, error) {
	key := tokenSecretKey(serverURL)
	raw, err := h.secrets.Get(key)
	if err != nil {
		return "", nil
	}
	return string(raw), nil
}

// SetAuthToken persists the bearer token for serverURL.
func (h *HostService) SetAuthToken(serverURL, token string) error {
	key := tokenSecretKey(serverURL)
	if strings.TrimSpace(token) == "" {
		return h.secrets.Delete(key)
	}
	return h.secrets.Set(key, []byte(token))
}

// ClearAuthToken deletes any stored bearer token for serverURL.
func (h *HostService) ClearAuthToken(serverURL string) error {
	return h.secrets.Delete(tokenSecretKey(serverURL))
}

// parseNotificationSessionID extracts a session ID from a notification identifier,
// returning "" for test notifications or auto-generated IDs so clicks only focus the window.
func parseNotificationSessionID(id string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "session:") {
		return strings.TrimSpace(strings.TrimPrefix(id, "session:"))
	}
	if id == "" || strings.HasPrefix(id, "mygo-") || strings.HasPrefix(id, "test:") {
		return ""
	}
	return id
}

// Notify displays a native desktop or mobile notification for approval alerts or task events.
func (h *HostService) Notify(title, body, sessionID string) error {
	h.mu.Lock()
	ready := h.appReady
	h.mu.Unlock()
	if !ready || !mygo.NotificationsSupported() {
		return errors.ErrUnsupported
	}
	sid := strings.TrimSpace(sessionID)
	notifID := ""
	group := ""
	if sid != "" && !strings.HasPrefix(sid, "test:") {
		notifID = "session:" + sid
		group = sid
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    notifID,
		Group: group,
		Title: title,
		Body:  body,
	})
	return n.Show()
}

// Haptic triggers tactile feedback on supported mobile hosts.
func (h *HostService) Haptic(style string) {
	h.mu.Lock()
	ready := h.appReady
	h.mu.Unlock()
	if !ready {
		return
	}
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "selection":
		mygo.Haptics.Selection()
	case "medium":
		mygo.Haptics.Medium()
	case "heavy":
		mygo.Haptics.Heavy()
	case "success":
		mygo.Haptics.Success()
	case "warning":
		mygo.Haptics.Warning()
	case "error":
		mygo.Haptics.Error()
	default:
		mygo.Haptics.Light()
	}
}

// PickDirectory opens a native OS folder picker and returns the chosen path, or "" if canceled.
func (h *HostService) PickDirectory(ctx context.Context) (string, error) {
	h.mu.Lock()
	ready := h.appReady
	h.mu.Unlock()
	if !ready {
		return "", errors.ErrUnsupported
	}
	if FormFactorName() == "mobile" {
		return "", errors.ErrUnsupported
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:            mygo.CallerWindow(ctx),
		Directory:         true,
		CreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	return paths[0], nil
}

// OpenExternal opens an http/https URL in the system default browser.
func (h *HostService) OpenExternal(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("disallowed URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("external URL must include host")
	}
	h.mu.Lock()
	ready := h.appReady
	h.mu.Unlock()
	if !ready {
		return errors.ErrUnsupported
	}
	return mygo.Shell.OpenExternal(u.String())
}

// ShareText opens the native OS share sheet when supported (iOS/Android).
func (h *HostService) ShareText(text, rawURL string) error {
	h.mu.Lock()
	ready := h.appReady
	h.mu.Unlock()
	if !ready {
		return errors.ErrUnsupported
	}
	return mygo.Share(mygo.ShareOptions{Text: text, URL: rawURL})
}

// SetMobilePushToken records the APNs/FCM token reported by the mobile host.
func (h *HostService) SetMobilePushToken(token string) {
	h.mu.Lock()
	h.pushToken = token
	h.mu.Unlock()
}

// GetMobilePushToken returns the stored APNs/FCM device push token.
func (h *HostService) GetMobilePushToken() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pushToken
}

func (h *HostService) setAppReady(ready bool) {
	h.mu.Lock()
	h.appReady = ready
	h.mu.Unlock()
}

// closeHost shuts down any embedded local backend and active remote tunnel cleanly.
func (h *HostService) closeHost() {
	h.mu.Lock()
	h.stopLocalBackendLocked()
	lp := h.detachRemoteProxyLocked()
	h.mu.Unlock()
	closeRemoteProxy(lp)
}

func (h *HostService) detachRemoteProxyLocked() *remote.LocalProxy {
	lp := h.remoteProxy
	h.remoteProxy = nil
	return lp
}

func closeRemoteProxy(lp *remote.LocalProxy) {
	if lp != nil {
		_ = lp.Close()
	}
}

func (h *HostService) ensureClientIdentity() (*remote.Identity, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	certRaw, err1 := h.secrets.Get("client_identity_cert")
	keyRaw, err2 := h.secrets.Get("client_identity_key")
	if err1 == nil && err2 == nil && len(certRaw) > 0 && len(keyRaw) > 0 {
		if id, pErr := remote.ParseIdentityPEM(string(certRaw), string(keyRaw)); pErr == nil {
			return id, nil
		}
	}

	name, _ := os.Hostname()
	if strings.TrimSpace(name) == "" {
		name = "Nex Client"
	}
	id, err := remote.GenerateIdentity(name)
	if err != nil {
		return nil, err
	}
	if err := h.secrets.Set("client_identity_cert", []byte(id.CertPEM)); err != nil {
		return nil, err
	}
	if err := h.secrets.Set("client_identity_key", []byte(id.KeyPEM)); err != nil {
		return nil, err
	}
	return id, nil
}

// PairWithURI parses a nex://pair?... invite, pairs with the intranet host over LAN/Direct/Relay,
// saves the paired profile + device token in Keychain, and connects the local E2EE tunnel proxy.
func (h *HostService) PairWithURI(ctx context.Context, rawURI string, deviceName string) (HostInfo, error) {
	invite, err := remote.ParsePairingURI(rawURI)
	if err != nil {
		return HostInfo{}, err
	}
	if strings.TrimSpace(deviceName) == "" {
		if hn, hErr := os.Hostname(); hErr == nil && strings.TrimSpace(hn) != "" {
			deviceName = hn
		} else {
			deviceName = "Nex " + PlatformName()
		}
	}
	clientID, err := h.ensureClientIdentity()
	if err != nil {
		return HostInfo{}, err
	}
	profile, deviceToken, _, err := remote.PairWithHost(ctx, clientID, invite, deviceName, PlatformName())
	if err != nil {
		return HostInfo{}, err
	}
	if err := h.secrets.Set(pairedDeviceSecretKey(profile.NodeID), []byte(deviceToken)); err != nil {
		return HostInfo{}, err
	}

	h.mu.Lock()
	h.cfg.PairedHosts = upsertPairedHost(h.cfg.PairedHosts, profile)
	_ = h.saveConfigLocked()
	h.mu.Unlock()

	return h.ConnectPairedHost(ctx, profile.NodeID)
}

// ConnectPairedHost starts the loopback E2EE proxy for nodeID and switches connection mode to "paired".
func (h *HostService) ConnectPairedHost(ctx context.Context, nodeID string) (HostInfo, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return HostInfo{}, errors.New("node_id is required")
	}

	h.mu.Lock()
	prof := findPairedHost(h.cfg.PairedHosts, nodeID)
	if prof == nil {
		h.mu.Unlock()
		return HostInfo{}, fmt.Errorf("paired host %q not found", nodeID)
	}
	profCopy := *prof
	h.mu.Unlock()

	clientID, err := h.ensureClientIdentity()
	if err != nil {
		return HostInfo{}, err
	}
	tokBytes, _ := h.secrets.Get(pairedDeviceSecretKey(nodeID))
	devToken := string(tokBytes)

	lp, err := remote.NewLocalProxy(clientID, profCopy, devToken, remote.LocalProxyOptions{
		OnStatusChanged: func(st remote.TransportStatus) {
			_ = EventRemoteStatus.Broadcast(st)
		},
	})
	if err != nil {
		return HostInfo{}, err
	}
	st, connErr := lp.Connect(ctx)
	if st.Connected && st.Transport != "" && st.Transport != "disconnected" {
		profCopy.LastTransport = st.Transport
	}

	h.mu.Lock()
	h.stopLocalBackendLocked()
	oldProxy := h.detachRemoteProxyLocked()
	h.remoteProxy = lp
	h.cfg.ConnectionMode = "paired"
	h.cfg.ActiveNodeID = nodeID
	h.cfg.PairedHosts = upsertPairedHost(h.cfg.PairedHosts, profCopy)
	_ = h.saveConfigLocked()
	info := h.infoLocked()
	h.mu.Unlock()

	closeRemoteProxy(oldProxy)
	_ = EventRemoteStatus.Broadcast(st)
	_ = EventServerChanged.Broadcast(info)
	if connErr != nil {
		return info, connErr
	}
	return info, nil
}

// RemovePairedHost disconnects (if active) and deletes nodeID from persisted paired hosts & secrets.
func (h *HostService) RemovePairedHost(nodeID string) (HostInfo, error) {
	nodeID = strings.TrimSpace(nodeID)
	_ = h.secrets.Delete(pairedDeviceSecretKey(nodeID))

	h.mu.Lock()
	filtered := make([]remote.PairedHostProfile, 0, len(h.cfg.PairedHosts))
	for _, p := range h.cfg.PairedHosts {
		if p.NodeID != nodeID {
			filtered = append(filtered, p)
		}
	}
	h.cfg.PairedHosts = filtered

	var oldProxy *remote.LocalProxy
	if h.cfg.ConnectionMode == "paired" && h.cfg.ActiveNodeID == nodeID {
		oldProxy = h.detachRemoteProxyLocked()
		h.cfg.ActiveNodeID = ""
		if h.cfg.ServerURL != "" {
			h.cfg.ConnectionMode = "remote"
		} else if supportsLocalMode {
			h.cfg.ConnectionMode = "local"
		} else {
			h.cfg.ConnectionMode = "unconfigured"
		}
	}
	err := h.saveConfigLocked()
	info := h.infoLocked()
	h.mu.Unlock()

	closeRemoteProxy(oldProxy)
	if err != nil {
		return HostInfo{}, err
	}
	_ = EventServerChanged.Broadcast(info)
	return info, nil
}

// ListPairedHosts returns all paired intranet host profiles saved on this client.
func (h *HostService) ListPairedHosts() []remote.PairedHostProfile {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]remote.PairedHostProfile, len(h.cfg.PairedHosts))
	copy(out, h.cfg.PairedHosts)
	return out
}

// RemoteStatus returns the live transport status of the active paired-host LocalProxy.
func (h *HostService) RemoteStatus() remote.TransportStatus {
	h.mu.Lock()
	lp := h.remoteProxy
	nodeID := h.cfg.ActiveNodeID
	h.mu.Unlock()
	if lp != nil {
		return lp.Status()
	}
	return remote.TransportStatus{
		NodeID:    nodeID,
		Transport: "disconnected",
	}
}

// ReconnectRemote re-probes network paths (LAN -> Direct -> Relay) on the active paired tunnel.
func (h *HostService) ReconnectRemote(ctx context.Context) (remote.TransportStatus, error) {
	h.mu.Lock()
	lp := h.remoteProxy
	h.mu.Unlock()
	if lp == nil {
		return remote.TransportStatus{Transport: "disconnected"}, nil
	}
	st, err := lp.Reconnect(ctx)
	info := h.Info()
	_ = EventServerChanged.Broadcast(info)
	return st, err
}

func pairedDeviceSecretKey(nodeID string) string {
	return "remote_device_token:" + strings.TrimSpace(nodeID)
}

func findPairedHost(list []remote.PairedHostProfile, nodeID string) *remote.PairedHostProfile {
	for i := range list {
		if list[i].NodeID == nodeID {
			return &list[i]
		}
	}
	return nil
}

func upsertPairedHost(list []remote.PairedHostProfile, prof remote.PairedHostProfile) []remote.PairedHostProfile {
	out := make([]remote.PairedHostProfile, 0, len(list)+1)
	out = append(out, prof)
	for _, existing := range list {
		if existing.NodeID != prof.NodeID {
			out = append(out, existing)
		}
	}
	return out
}

func configureWindowPermissions(w *mygo.Window) {
	if w == nil {
		return
	}
	if p := w.Page(); p != nil {
		p.SetPermissionHandler(func(req mygo.PermissionRequest) bool {
			for _, perm := range req.Permissions {
				if perm != mygo.PermissionCamera && perm != mygo.PermissionNotifications {
					return false
				}
			}
			return len(req.Permissions) > 0
		})
	}
}

// DeepLinkAction represents a parsed nex:// deep-link action.
type DeepLinkAction struct {
	Kind    string
	Value   string
	Section string
}

// ParseDeepLink parses nex://session/<id>, nex://connect?url=<url>, and nex://settings/<section>.
func ParseDeepLink(rawURL string) (DeepLinkAction, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(u.Scheme, "nex") {
		return DeepLinkAction{}, false
	}
	host := strings.ToLower(strings.TrimSpace(u.Host))
	trimmedPath := strings.Trim(u.Path, "/")
	switch host {
	case "session":
		if trimmedPath == "" {
			return DeepLinkAction{}, false
		}
		return DeepLinkAction{Kind: "session", Value: trimmedPath}, true
	case "connect":
		target := strings.TrimSpace(u.Query().Get("url"))
		if target == "" {
			return DeepLinkAction{}, false
		}
		norm, err := NormalizeServerURL(target)
		if err != nil {
			return DeepLinkAction{}, false
		}
		return DeepLinkAction{Kind: "connect", Value: norm}, true
	case "settings":
		sec := trimmedPath
		if sec == "" {
			sec = strings.TrimSpace(u.Query().Get("section"))
		}
		if sec == "" {
			sec = "general"
		}
		return DeepLinkAction{Kind: "settings", Section: sec}, true
	case "pair":
		if _, pErr := remote.ParsePairingURI(rawURL); pErr != nil {
			return DeepLinkAction{}, false
		}
		return DeepLinkAction{Kind: "pair", Value: strings.TrimSpace(rawURL)}, true
	default:
		return DeepLinkAction{}, false
	}
}

// handleDeepLink applies a nex:// URL and broadcasts corresponding events.
func (h *HostService) handleDeepLink(rawURL string) bool {
	act, ok := ParseDeepLink(rawURL)
	if !ok {
		return false
	}
	switch act.Kind {
	case "session":
		_ = EventOpenSession.Broadcast(act.Value)
		return true
	case "connect":
		_, err := h.SetServerURL(act.Value)
		return err == nil
	case "settings":
		_ = EventOpenSettings.Broadcast(act.Section)
		return true
	case "pair":
		_ = EventPairInvite.Broadcast(act.Value)
		return true
	default:
		return false
	}
}

// NormalizeServerURL validates and normalizes an http/https Nex server base URL.
func NormalizeServerURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("server URL is required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("server URL must use http:// or https:// (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("server URL must include a host")
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

// PlatformName returns "win32" on Windows or runtime.GOOS elsewhere.
func PlatformName() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

// FormFactorName returns "mobile" on iOS/Android or "desktop" elsewhere.
func FormFactorName() string {
	if runtime.GOOS == "ios" || runtime.GOOS == "android" {
		return "mobile"
	}
	return "desktop"
}

func tokenSecretKey(serverURL string) string {
	norm, err := NormalizeServerURL(serverURL)
	if err != nil || norm == "" {
		return "token:default"
	}
	sum := sha256.Sum256([]byte(norm))
	return "token:" + hex.EncodeToString(sum[:16])
}

func prependRecentServer(existing []string, urlStr string) []string {
	out := make([]string, 0, maxRecentServers)
	out = append(out, urlStr)
	for _, item := range existing {
		if item != "" && item != urlStr {
			out = append(out, item)
			if len(out) >= maxRecentServers {
				break
			}
		}
	}
	return out
}

func cleanRecentServers(list []string) []string {
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, raw := range list {
		norm, err := NormalizeServerURL(raw)
		if err != nil {
			continue
		}
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
		if len(out) >= maxRecentServers {
			break
		}
	}
	return out
}

type mygoSecretStore struct {
	fallback *fileSecretStore
}

func (m *mygoSecretStore) Get(key string) ([]byte, error) {
	v, err := mygo.Secrets.Service("com.nex.app").Get(key)
	if err == nil && len(v) > 0 {
		return v, nil
	}
	if errors.Is(err, mygo.ErrSecretNotFound) {
		if fb, fbErr := m.fallback.Get(key); fbErr == nil {
			return fb, nil
		}
		return nil, err
	}
	return m.fallback.Get(key)
}

func (m *mygoSecretStore) Set(key string, val []byte) error {
	if err := mygo.Secrets.Service("com.nex.app").Set(key, val); err == nil {
		return nil
	}
	return m.fallback.Set(key, val)
}

func (m *mygoSecretStore) Delete(key string) error {
	errNative := mygo.Secrets.Service("com.nex.app").Delete(key)
	errFB := m.fallback.Delete(key)
	if errNative == nil {
		return nil
	}
	return errFB
}

type fileSecretStore struct {
	mu   sync.Mutex
	path string
}

func (f *fileSecretStore) readAllLocked() map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(f.path)
	if err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func (f *fileSecretStore) writeAllLocked(m map[string]string) error {
	_ = os.MkdirAll(filepath.Dir(f.path), 0o700)
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(f.path, raw, 0o600)
}

func (f *fileSecretStore) Get(key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.readAllLocked()
	v, ok := m[key]
	if !ok {
		return nil, mygo.ErrSecretNotFound
	}
	return []byte(v), nil
}

func (f *fileSecretStore) Set(key string, val []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.readAllLocked()
	m[key] = string(val)
	return f.writeAllLocked(m)
}

func (f *fileSecretStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.readAllLocked()
	delete(m, key)
	return f.writeAllLocked(m)
}
