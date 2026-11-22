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
)

// Typed events emitted from the native mygo host to the web frontend.
var (
	EventOpenSession  = mygo.NewEvent[string]("nex:open-session")
	EventAppResume    = mygo.NewEvent[int64]("nex:app-resume")
	EventOpenSettings = mygo.NewEvent[string]("nex:open-settings")
	EventNewTask      = mygo.NewEvent[bool]("nex:new-task")
	EventServerChanged = mygo.NewEvent[HostInfo]("nex:server-changed")
)

// HostInfo describes the active mygo runtime container and connection target.
type HostInfo struct {
	Platform          string   `json:"platform"`
	FormFactor        string   `json:"form_factor"`
	Version           string   `json:"version"`
	ConnectionMode    string   `json:"connection_mode"`
	ServerURL         string   `json:"server_url"`
	LocalURL          string   `json:"local_url,omitempty"`
	SupportsLocalMode bool     `json:"supports_local_mode"`
	RecentServers     []string `json:"recent_servers"`
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
	ConnectionMode string   `json:"connection_mode"`
	ServerURL      string   `json:"server_url"`
	RecentServers  []string `json:"recent_servers,omitempty"`
}

const maxRecentServers = 8

// HostService exposes typed IPC methods bound as "NexHost" via mygo.BindAs
// across Linux, macOS, Windows, iOS, and Android.
type HostService struct {
	mu             sync.Mutex
	configDir      string
	workspaceDir   string
	localAddr      string
	secrets        SecretStore
	cfg            persistedHostConfig
	localInst      localInstance
	localURL       string
	pushToken      string
	appReady       bool
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
			default:
				if parsed.ServerURL != "" {
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

// ensureLocalStarted boots the desktop All-in-One embedded server + runner if configured for local mode.
func (h *HostService) ensureLocalStarted(ctx context.Context) (HostInfo, error) {
	h.mu.Lock()
	if !supportsLocalMode {
		info := h.infoLocked()
		h.mu.Unlock()
		return info, errors.ErrUnsupported
	}
	if _, err := h.startLocalBackendLocked(ctx); err != nil {
		info := h.infoLocked()
		h.mu.Unlock()
		return info, err
	}
	h.cfg.ConnectionMode = "local"
	_ = h.saveConfigLocked()
	info := h.infoLocked()
	h.mu.Unlock()
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
	if h.cfg.ConnectionMode == "local" {
		effectiveURL = h.localURL
	}
	ver := mygo.App.Version()
	if ver == "" {
		ver = "0.3.0"
	}
	recent := make([]string, len(h.cfg.RecentServers))
	copy(recent, h.cfg.RecentServers)
	return HostInfo{
		Platform:          PlatformName(),
		FormFactor:        FormFactorName(),
		Version:           ver,
		ConnectionMode:    h.cfg.ConnectionMode,
		ServerURL:         effectiveURL,
		LocalURL:          h.localURL,
		SupportsLocalMode: supportsLocalMode,
		RecentServers:     recent,
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
	h.cfg.ConnectionMode = "remote"
	h.cfg.ServerURL = norm
	h.cfg.RecentServers = prependRecentServer(h.cfg.RecentServers, norm)
	saveErr := h.saveConfigLocked()
	info := h.infoLocked()
	h.mu.Unlock()
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
		if _, err := h.startLocalBackendLocked(ctx); err != nil {
			h.mu.Unlock()
			return HostInfo{}, err
		}
		h.cfg.ConnectionMode = "local"
		err := h.saveConfigLocked()
		info := h.infoLocked()
		h.mu.Unlock()
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
		if h.cfg.ServerURL != "" {
			h.cfg.ConnectionMode = "remote"
		} else {
			h.cfg.ConnectionMode = "unconfigured"
		}
		err := h.saveConfigLocked()
		info := h.infoLocked()
		h.mu.Unlock()
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

// Notify displays a native desktop or mobile notification for approval alerts or task events.
func (h *HostService) Notify(title, body, sessionID string) error {
	h.mu.Lock()
	ready := h.appReady
	h.mu.Unlock()
	if !ready || !mygo.NotificationsSupported() {
		return nil
	}
	notifID := strings.TrimSpace(sessionID)
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    notifID,
		Group: notifID,
		Title: title,
		Body:  body,
	})
	if notifID != "" {
		n.OnClick(func() {
			_ = EventOpenSession.Broadcast(notifID)
		})
	}
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

// closeHost shuts down any embedded local backend cleanly.
func (h *HostService) closeHost() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLocalBackendLocked()
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
