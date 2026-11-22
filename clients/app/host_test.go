package app

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeServerURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://nex.tylinux.cn/", "https://nex.tylinux.cn", false},
		{"  http://127.0.0.1:8080///  ", "http://127.0.0.1:8080", false},
		{"https://example.com/sub/", "https://example.com/sub", false},
		{"", "", true},
		{"ftp://example.com", "", true},
		{"javascript:alert(1)", "", true},
		{"https://", "", true},
	}

	for _, tc := range cases {
		got, err := NormalizeServerURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("NormalizeServerURL(%q): expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("NormalizeServerURL(%q) = (%q, %v), want (%q, nil)", tc.in, got, err, tc.want)
		}
	}
}

func TestParseDeepLink(t *testing.T) {
	if act, ok := ParseDeepLink("nex://session/sess_123"); !ok || act.Kind != "session" || act.Value != "sess_123" {
		t.Fatalf("unexpected session deeplink result: (%+v, %v)", act, ok)
	}
	if act, ok := ParseDeepLink("nex://connect?url=https%3A%2F%2Fnex.tylinux.cn%2F"); !ok || act.Kind != "connect" || act.Value != "https://nex.tylinux.cn" {
		t.Fatalf("unexpected connect deeplink result: (%+v, %v)", act, ok)
	}
	if act, ok := ParseDeepLink("nex://settings/browser"); !ok || act.Kind != "settings" || act.Section != "browser" {
		t.Fatalf("unexpected settings deeplink result: (%+v, %v)", act, ok)
	}
	if _, ok := ParseDeepLink("https://nex.tylinux.cn"); ok {
		t.Fatalf("expected non-nex scheme to be rejected")
	}
	if _, ok := ParseDeepLink("nex://session/"); ok {
		t.Fatalf("expected empty session ID to be rejected")
	}
}

func TestHostServicePersistenceSecretsAndModeSwitch(t *testing.T) {
	cfgDir := t.TempDir()
	secrets := &fileSecretStore{path: filepath.Join(cfgDir, "secrets.json")}

	svc := NewHostService(HostServiceOptions{
		ConfigDir:    cfgDir,
		WorkspaceDir: t.TempDir(),
		LocalAddr:    "127.0.0.1:0",
		Secrets:      secrets,
	})
	defer svc.closeHost()

	// Verify scheme validation on OpenExternal
	if err := svc.OpenExternal("file:///etc/passwd"); err == nil {
		t.Fatalf("expected file:// scheme to be blocked")
	}
	if err := svc.OpenExternal("javascript:alert(1)"); err == nil {
		t.Fatalf("expected javascript: scheme to be blocked")
	}

	// Verify per-server secret token isolation
	if err := svc.SetAuthToken("https://a.example.com/", "tok_a"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetAuthToken("https://b.example.com", "tok_b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.GetAuthToken("https://a.example.com"); got != "tok_a" {
		t.Fatalf("token mismatch for server A: got=%q", got)
	}
	if got, _ := svc.GetAuthToken("https://b.example.com"); got != "tok_b" {
		t.Fatalf("token mismatch for server B: got=%q", got)
	}
	if err := svc.ClearAuthToken("https://a.example.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.GetAuthToken("https://a.example.com"); got != "" {
		t.Fatalf("expected cleared token to be empty, got=%q", got)
	}

	// Verify SetServerURL persistence and deduped/capped recent servers
	info, err := svc.SetServerURL("https://s1.example.com/")
	if err != nil || info.ServerURL != "https://s1.example.com" || info.ConnectionMode != "remote" {
		t.Fatalf("SetServerURL failed: info=%+v err=%v", info, err)
	}
	for _, s := range []string{"https://s2.example.com", "https://s1.example.com", "https://s3.example.com"} {
		if _, err := svc.SetServerURL(s); err != nil {
			t.Fatal(err)
		}
	}

	reloaded := NewHostService(HostServiceOptions{ConfigDir: cfgDir, Secrets: secrets})
	reloadedInfo := reloaded.Info()
	if reloadedInfo.ServerURL != "https://s3.example.com" {
		t.Fatalf("expected persisted server URL, got=%q", reloadedInfo.ServerURL)
	}
	if len(reloadedInfo.RecentServers) != 3 || reloadedInfo.RecentServers[0] != "https://s3.example.com" || reloadedInfo.RecentServers[1] != "https://s1.example.com" {
		t.Fatalf("unexpected recent servers ordering: %#v", reloadedInfo.RecentServers)
	}

	// Verify desktop local All-in-One switch & clean shutdown when switching to remote
	if !supportsLocalMode {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	localInfo, err := svc.SwitchMode(ctx, "local", "")
	if err != nil {
		t.Fatalf("SwitchMode(local) failed: %v", err)
	}
	if localInfo.ConnectionMode != "local" || !strings.HasPrefix(localInfo.ServerURL, "http://127.0.0.1:") {
		t.Fatalf("unexpected local host info: %+v", localInfo)
	}

	resp, err := http.Get(localInfo.ServerURL + "/api/v1/health")
	if err != nil {
		t.Fatalf("embedded server health check failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from embedded server, got %d", resp.StatusCode)
	}

	remoteInfo, err := svc.SwitchMode(ctx, "remote", "https://nex.tylinux.cn")
	if err != nil {
		t.Fatalf("SwitchMode(remote) failed: %v", err)
	}
	if remoteInfo.ConnectionMode != "remote" || remoteInfo.LocalURL != "" || remoteInfo.ServerURL != "https://nex.tylinux.cn" {
		t.Fatalf("unexpected remote host info after switch: %+v", remoteInfo)
	}
}
