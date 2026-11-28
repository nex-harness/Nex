package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ManagedProcess owns an OS-level Chromium subprocess and its cleanup.
type ManagedProcess struct {
	cmd      *exec.Cmd
	wsURL    string
	closeMu  sync.Mutex
	closed   bool
	doneCh   chan struct{}
}

func (p *ManagedProcess) Done() <-chan struct{} {
	return p.doneCh
}

func (p *ManagedProcess) Close() {
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return
	}
	p.closed = true
	p.closeMu.Unlock()

	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(os.Interrupt)
		select {
		case <-p.doneCh:
			return
		case <-time.After(1500 * time.Millisecond):
			killProcessGroup(p.cmd)
		}
	}
}

// LaunchManaged starts Chromium with an explicit `--user-data-dir` (required by Chrome 136+ for CDP)
// and `--remote-debugging-port=0`, returning the WebSocket debugger URL read from stderr.
func LaunchManaged(ctx context.Context, execPath, profileDir string, headless bool) (*ManagedProcess, string, error) {
	if strings.TrimSpace(execPath) == "" {
		return nil, "", newError(ErrBackendUnavailable, "none", "no Chrome/Chromium executable path available")
	}
	if profileDir != "" {
		if err := os.MkdirAll(profileDir, 0o700); err != nil {
			return nil, "", newError(ErrExecutionError, "none", fmt.Sprintf("create browser profile dir: %v", err))
		}
	}

	args := []string{
		"--remote-debugging-port=0",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-dev-shm-usage",
		"--disable-features=Translate,OptimizationHints",
		fmt.Sprintf("--window-size=%d,%d", DefaultViewportWidth, DefaultViewportHeight),
	}
	if profileDir != "" {
		args = append(args, "--user-data-dir="+profileDir)
	}
	if extDir := strings.TrimSpace(os.Getenv("NEX_BROWSER_EXT_DIR")); extDir != "" {
		if st, err := os.Stat(filepath.Join(extDir, "manifest.json")); err == nil && !st.IsDir() {
			args = append(args,
				"--disable-extensions-except="+extDir,
				"--load-extension="+extDir,
			)
		}
	}
	if headless {
		if strings.Contains(filepath.Base(execPath), "chrome-headless-shell") {
			args = append(args, "--headless")
		} else {
			args = append(args, "--headless=new")
		}
	}
	args = append(args, "about:blank")

	cmd := exec.Command(execPath, args...)
	setProcessGroup(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", newError(ErrBackendUnavailable, "none", err.Error())
	}
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, "", newError(ErrBackendUnavailable, "none", fmt.Sprintf("launch browser %s: %v", execPath, err))
	}

	doneCh := make(chan struct{})
	wsCh := make(chan string, 1)
	errLines := make([]string, 0, 8)
	var errMu sync.Mutex

	go func() {
		defer close(doneCh)
		_ = cmd.Wait()
	}()

	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if idx := strings.Index(line, "DevTools listening on "); idx >= 0 {
				ws := strings.TrimSpace(line[idx+len("DevTools listening on "):])
				select {
				case wsCh <- ws:
				default:
				}
			}
			errMu.Lock()
			if len(errLines) < 20 {
				errLines = append(errLines, line)
			}
			errMu.Unlock()
		}
	}()

	proc := &ManagedProcess{
		cmd:    cmd,
		doneCh: doneCh,
	}

	startTimer := time.NewTimer(15 * time.Second)
	defer startTimer.Stop()

	select {
	case wsURL := <-wsCh:
		proc.wsURL = wsURL
		return proc, wsURL, nil
	case <-doneCh:
		errMu.Lock()
		detail := strings.Join(errLines, " | ")
		errMu.Unlock()
		return nil, "", newError(ErrBackendUnavailable, "none", fmt.Sprintf("browser exited before DevTools ready: %s", detail))
	case <-startTimer.C:
		proc.Close()
		return nil, "", newError(ErrTimeout, "none", "timed out waiting for Chromium DevTools websocket URL")
	case <-ctx.Done():
		proc.Close()
		return nil, "", ctx.Err()
	}
}

// DiscoverAttachWS resolves a Chrome WebSocket debugger URL from either a ws:// URL directly
// or an HTTP debugging endpoint (e.g. http://127.0.0.1:9222 -> /json/version).
func DiscoverAttachWS(ctx context.Context, rawURL string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if trimmed == "" {
		return "", newError(ErrBackendUnavailable, "none", "attach_url is empty")
	}
	if strings.HasPrefix(trimmed, "ws://") || strings.HasPrefix(trimmed, "wss://") {
		return trimmed, nil
	}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		trimmed = "http://" + trimmed
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, trimmed+"/json/version", nil)
	if err != nil {
		return "", newError(ErrBackendUnavailable, "none", err.Error())
	}
	hc := &http.Client{Timeout: 4 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return "", newError(ErrBackendUnavailable, "none", fmt.Sprintf("probe %s/json/version: %v", trimmed, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", newError(ErrBackendUnavailable, "none", fmt.Sprintf("%s/json/version returned HTTP %d", trimmed, resp.StatusCode))
	}
	var meta struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil || meta.WebSocketDebuggerURL == "" {
		return "", newError(ErrBackendUnavailable, "none", "missing webSocketDebuggerUrl in /json/version response")
	}
	return meta.WebSocketDebuggerURL, nil
}
