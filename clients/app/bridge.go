package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// NativeBridge exposes typed IPC methods bindable via egoist/mygo (`mygo.Bind(&NativeBridge{...})`)
// for Desktop (macOS/Windows/Linux) and Mobile (iOS/Android) Web Mode containers.
type NativeBridge struct {
	mu          sync.Mutex
	configDir   string
	pushToken   string
	onNotify    func(title, body string)
	onHaptic    func(style string)
}

type storedCredentials struct {
	AuthToken string `json:"auth_token"`
}

// NewNativeBridge initializes the Go-side HostBridge implementation.
func NewNativeBridge(configDir string, onNotify func(title, body string), onHaptic func(style string)) *NativeBridge {
	_ = os.MkdirAll(configDir, 0o700)
	return &NativeBridge{
		configDir: configDir,
		onNotify:  onNotify,
		onHaptic:  onHaptic,
	}
}

func (b *NativeBridge) credFile() string {
	return filepath.Join(b.configDir, "credentials.json")
}

// GetAuthToken retrieves the stored Server auth token (bound to mygo Typed IPC).
func (b *NativeBridge) GetAuthToken() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := os.ReadFile(b.credFile())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var c storedCredentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", err
	}
	return c.AuthToken, nil
}

// SetAuthToken persists the Server auth token with 0600 file permissions.
func (b *NativeBridge) SetAuthToken(token string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := json.Marshal(storedCredentials{AuthToken: token})
	if err != nil {
		return err
	}
	return os.WriteFile(b.credFile(), raw, 0o600)
}

// Notify triggers a native desktop/mobile notification when an approval request arrives.
func (b *NativeBridge) Notify(title, body string) {
	if b.onNotify != nil {
		b.onNotify(title, body)
	}
}

// Haptic triggers device tactile feedback on mobile hosts.
func (b *NativeBridge) Haptic(style string) {
	if b.onHaptic != nil {
		b.onHaptic(style)
	}
}

// SetMobilePushToken records the APNs/FCM token from the mygo mobile container.
func (b *NativeBridge) SetMobilePushToken(token string) {
	b.mu.Lock()
	b.pushToken = token
	b.mu.Unlock()
}

// GetMobilePushToken returns the registered APNs/FCM token.
func (b *NativeBridge) GetMobilePushToken() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pushToken
}
