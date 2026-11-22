package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func TestParseTailscaleStatusJSON(t *testing.T) {
	runningJSON := []byte(`{
		"BackendState": "Running",
		"Self": {
			"DNSName": "studio-mac.tailnet-abc.ts.net.",
			"TailscaleIPs": ["100.64.0.12", "fd7a:115c:a1e0::12"]
		}
	}`)
	st, err := ParseTailscaleStatusJSON(runningJSON, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Available || !st.Running || st.DNSName != "studio-mac.tailnet-abc.ts.net" {
		t.Fatalf("unexpected running Tailscale status: %+v", st)
	}
	if len(st.SuggestedURLs) != 3 || st.SuggestedURLs[0] != "http://studio-mac.tailnet-abc.ts.net:8080" || st.SuggestedURLs[1] != "https://studio-mac.tailnet-abc.ts.net" || st.SuggestedURLs[2] != "http://100.64.0.12:8080" {
		t.Fatalf("unexpected SuggestedURLs: %#v", st.SuggestedURLs)
	}

	needsLoginJSON := []byte(`{"BackendState":"NeedsLogin","Self":{"DNSName":""}}`)
	st2, err := ParseTailscaleStatusJSON(needsLoginJSON, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.Available || st2.Running || st2.BackendState != "NeedsLogin" || len(st2.SuggestedURLs) != 0 {
		t.Fatalf("unexpected NeedsLogin status: %+v", st2)
	}

	if _, err := ParseTailscaleStatusJSON([]byte("not-json"), 8080); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestInstallProbeRunnerAndUninstallTSNetSidecar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock sidecar runs on Unix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	artifact, err := resolveTSNetPlatformArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}

	// Serve a tiny mock sidecar script matching the resolved platform artifact name
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+artifact {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("#!/bin/sh\necho '{\"state\":\"needs_login\",\"auth_url\":\"https://login.tailscale.com/a/test123\"}'\nsleep 0.05\necho '{\"state\":\"running\",\"dns_name\":\"nex-box.ts.net\",\"ips\":[\"100.64.0.99\"]}'\nwhile :; do sleep 0.05; done\n"))
	}))
	defer mockServer.Close()

	dataDir := t.TempDir()
	if st := ProbeTSNetSidecar(dataDir); st.Installed || st.State != "not_installed" {
		t.Fatalf("expected not_installed before download, got %+v", st)
	}

	installed, err := InstallTSNetSidecar(ctx, dataDir, mockServer.URL, mockServer.Client())
	if err != nil {
		t.Fatalf("InstallTSNetSidecar failed: %v", err)
	}
	if !installed.Installed || !installed.PlatformSupported || installed.BinaryPath == "" {
		t.Fatalf("unexpected installed sidecar status: %+v", installed)
	}

	runner := NewTSNetHostRunner(dataDir)
	if err := runner.Start(TSNetHostOptions{
		DataDir:     dataDir,
		Hostname:    "nex-box",
		UpstreamURL: "http://127.0.0.1:18080",
	}); err != nil {
		t.Fatalf("TSNetHostRunner.Start failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var sawAuthURL string
	var live TSNetSidecarStatus
	for time.Now().Before(deadline) {
		live = runner.Status()
		if live.AuthURL != "" {
			sawAuthURL = live.AuthURL
		}
		if live.State == "running" && live.DNSName == "nex-box.ts.net" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if live.State != "running" || sawAuthURL != "https://login.tailscale.com/a/test123" || len(live.TailscaleIPs) != 1 {
		runner.Stop()
		t.Fatalf("unexpected live sidecar status (sawAuthURL=%q): %+v", sawAuthURL, live)
	}

	runner.Stop()
	if err := UninstallTSNetSidecar(dataDir); err != nil {
		t.Fatalf("UninstallTSNetSidecar failed: %v", err)
	}
	if st := ProbeTSNetSidecar(dataDir); st.Installed {
		t.Fatalf("expected not installed after uninstall, got %+v", st)
	}
}
