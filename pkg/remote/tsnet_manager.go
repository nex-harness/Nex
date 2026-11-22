//go:build !ios && !android

package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

func managedTSNetBinaryPath(dataDir string) string {
	name := "nex-tsnet"
	if runtime.GOOS == "windows" {
		name = "nex-tsnet.exe"
	}
	return filepath.Join(dataDir, "bin", name)
}

func findTSNetBinary(dataDir string) string {
	if env := strings.TrimSpace(os.Getenv("NEX_TSNET_BIN")); env != "" {
		if st, err := os.Stat(env); err == nil && !st.IsDir() {
			return env
		}
	}
	managed := managedTSNetBinaryPath(dataDir)
	if st, err := os.Stat(managed); err == nil && !st.IsDir() {
		return managed
	}
	if p, err := exec.LookPath("nex-tsnet"); err == nil && p != "" {
		return p
	}
	return ""
}

// ProbeTSNetSidecar checks whether the optional nex-tsnet binary is installed.
func ProbeTSNetSidecar(dataDir string) TSNetSidecarStatus {
	binPath := findTSNetBinary(dataDir)
	if binPath == "" {
		return TSNetSidecarStatus{
			PlatformSupported: true,
			Installed:         false,
			State:             "not_installed",
		}
	}
	return TSNetSidecarStatus{
		PlatformSupported: true,
		Installed:         true,
		BinaryPath:        binPath,
		State:             "stopped",
	}
}

// InstallTSNetSidecar downloads nex-tsnet from downloadURLOrMirror into <dataDir>/bin/nex-tsnet.
func InstallTSNetSidecar(ctx context.Context, dataDir, downloadURLOrMirror string, hc *http.Client) (TSNetSidecarStatus, error) {
	rawBase := strings.TrimSpace(downloadURLOrMirror)
	if rawBase == "" {
		rawBase = strings.TrimSpace(os.Getenv("NEX_TSNET_MIRROR"))
	}
	if rawBase == "" {
		return ProbeTSNetSidecar(dataDir), errors.New("download_url or mirror base URL is required to install nex-tsnet")
	}

	artifact, err := resolveTSNetPlatformArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return TSNetSidecarStatus{PlatformSupported: false, State: "error", Error: err.Error()}, err
	}

	targetURL := rawBase
	if !strings.Contains(rawBase, "nex-tsnet-") && !strings.HasSuffix(rawBase, "nex-tsnet") && !strings.HasSuffix(rawBase, ".exe") {
		targetURL = strings.TrimRight(rawBase, "/") + "/" + artifact
	}

	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return ProbeTSNetSidecar(dataDir), err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return ProbeTSNetSidecar(dataDir), fmt.Errorf("download nex-tsnet: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ProbeTSNetSidecar(dataDir), fmt.Errorf("download nex-tsnet HTTP %d from %s", resp.StatusCode, targetURL)
	}

	dest := managedTSNetBinaryPath(dataDir)
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return ProbeTSNetSidecar(dataDir), err
	}
	n, cpErr := io.Copy(f, io.LimitReader(resp.Body, 200<<20))
	closeErr := f.Close()
	if cpErr != nil {
		_ = os.Remove(tmp)
		return ProbeTSNetSidecar(dataDir), cpErr
	}
	if closeErr != nil || n == 0 {
		_ = os.Remove(tmp)
		return ProbeTSNetSidecar(dataDir), errors.New("downloaded empty nex-tsnet binary")
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return ProbeTSNetSidecar(dataDir), err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return ProbeTSNetSidecar(dataDir), err
	}
	return ProbeTSNetSidecar(dataDir), nil
}

// UninstallTSNetSidecar deletes the managed nex-tsnet binary from dataDir.
func UninstallTSNetSidecar(dataDir string) error {
	dest := managedTSNetBinaryPath(dataDir)
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// TSNetHostOptions configures a managed nex-tsnet host subprocess.
type TSNetHostOptions struct {
	DataDir      string
	Hostname     string
	ControlURL   string
	UpstreamURL  string
	ListenPort   int
}

// TSNetHostRunner supervises the on-demand nex-tsnet host subprocess when system Tailscale is unavailable.
type TSNetHostRunner struct {
	dataDir string

	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	doneCh chan struct{}
	status TSNetSidecarStatus
}

// NewTSNetHostRunner creates a TSNetHostRunner bound to dataDir.
func NewTSNetHostRunner(dataDir string) *TSNetHostRunner {
	return &TSNetHostRunner{
		dataDir: dataDir,
		status:  ProbeTSNetSidecar(dataDir),
	}
}

// Start launches `nex-tsnet host` and reads newline-delimited JSON status updates until stopped.
func (r *TSNetHostRunner) Start(opts TSNetHostOptions) error {
	r.Stop()

	binPath := findTSNetBinary(r.dataDir)
	if binPath == "" {
		st := ProbeTSNetSidecar(r.dataDir)
		r.mu.Lock()
		r.status = st
		r.mu.Unlock()
		return errors.New("nex-tsnet binary not installed")
	}

	stateDir := filepath.Join(r.dataDir, "tsnet-host")
	_ = os.MkdirAll(stateDir, 0o700)
	hostname := strings.TrimSpace(opts.Hostname)
	if hostname == "" {
		hostname = "nex-host"
	}
	args := []string{
		"host",
		"--state-dir", stateDir,
		"--hostname", hostname,
		"--upstream", opts.UpstreamURL,
	}
	if strings.TrimSpace(opts.ControlURL) != "" {
		args = append(args, "--control-url", strings.TrimSpace(opts.ControlURL))
	}

	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, binPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		cancel()
		r.mu.Lock()
		r.status = TSNetSidecarStatus{
			PlatformSupported: true,
			Installed:         true,
			BinaryPath:        binPath,
			State:             "error",
			Error:             err.Error(),
		}
		r.mu.Unlock()
		return err
	}

	doneCh := make(chan struct{})
	r.mu.Lock()
	r.cmd = cmd
	r.cancel = cancel
	r.doneCh = doneCh
	r.status = TSNetSidecarStatus{
		PlatformSupported: true,
		Installed:         true,
		BinaryPath:        binPath,
		Running:           true,
		State:             "starting",
	}
	r.mu.Unlock()

	go func() {
		defer close(doneCh)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var ev struct {
				State     string   `json:"state"`
				AuthURL   string   `json:"auth_url"`
				DNSName   string   `json:"dns_name"`
				IPs       []string `json:"ips"`
				ListenURL string   `json:"listen_url"`
				Error     string   `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &ev) != nil {
				continue
			}
			r.mu.Lock()
			r.status.State = ev.State
			r.status.AuthURL = ev.AuthURL
			r.status.DNSName = strings.TrimRight(ev.DNSName, ".")
			r.status.TailscaleIPs = ev.IPs
			r.status.ListenURL = ev.ListenURL
			r.status.Error = ev.Error
			r.status.Running = ev.State != "stopped" && ev.State != "error"
			r.mu.Unlock()
		}
		waitErr := cmd.Wait()
		r.mu.Lock()
		r.status.Running = false
		if waitErr != nil && runCtx.Err() == nil && r.status.Error == "" {
			r.status.State = "error"
			r.status.Error = waitErr.Error()
		} else if r.status.State != "error" {
			r.status.State = "stopped"
		}
		r.mu.Unlock()
	}()

	return nil
}

// Status returns a snapshot of the sidecar installation & live process state.
func (r *TSNetHostRunner) Status() TSNetSidecarStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.status
	if !st.Running {
		probe := ProbeTSNetSidecar(r.dataDir)
		probe.Error = st.Error
		if st.Error != "" && probe.Installed {
			probe.State = "error"
		}
		return probe
	}
	return st
}

// Stop cleanly shuts down any active nex-tsnet child process.
func (r *TSNetHostRunner) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	cmd := r.cmd
	doneCh := r.doneCh
	r.cancel = nil
	r.cmd = nil
	r.doneCh = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if doneCh != nil {
		<-doneCh
	}
}
