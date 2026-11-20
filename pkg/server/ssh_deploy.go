package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/pkg/store"
)

// SSHConfigHost represents a concrete Host entry parsed from ~/.ssh/config.
type SSHConfigHost struct {
	Alias        string `json:"alias"`
	HostName     string `json:"hostname"`
	User         string `json:"user,omitempty"`
	Port         int    `json:"port"`
	IdentityFile string `json:"identity_file,omitempty"`
}

type stagedBinaryInfo struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt int64  `json:"updated_at"`
}

type oneTimeBinaryToken struct {
	OS        string
	Arch      string
	ExpiresAt time.Time
}

var (
	dlTokensMu sync.Mutex
	dlTokens   = map[string]oneTimeBinaryToken{}
)

func issueBinaryDownloadToken(targetOS, targetArch string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	dlTokensMu.Lock()
	now := time.Now()
	for k, v := range dlTokens {
		if now.After(v.ExpiresAt) {
			delete(dlTokens, k)
		}
	}
	dlTokens[tok] = oneTimeBinaryToken{OS: targetOS, Arch: targetArch, ExpiresAt: now.Add(5 * time.Minute)}
	dlTokensMu.Unlock()
	return tok
}

func validateBinaryDownloadToken(tok, targetOS, targetArch string) bool {
	if tok == "" {
		return false
	}
	dlTokensMu.Lock()
	defer dlTokensMu.Unlock()
	entry, ok := dlTokens[tok]
	if !ok || time.Now().After(entry.ExpiresAt) {
		delete(dlTokens, tok)
		return false
	}
	return entry.OS == targetOS && entry.Arch == targetArch
}

func expandHomePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// parseSSHConfigFile parses OpenSSH config files (including top-level Include directives).
func parseSSHConfigFile(path string, depth int) []SSHConfigHost {
	if depth > 2 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var hosts []SSHConfigHost
	var cur *SSHConfigHost
	seen := make(map[string]bool)

	flushCur := func() {
		if cur == nil || cur.Alias == "" {
			return
		}
		if cur.HostName == "" {
			cur.HostName = cur.Alias
		}
		if cur.Port <= 0 {
			cur.Port = 22
		}
		if !seen[cur.Alias] {
			seen[cur.Alias] = true
			hosts = append(hosts, *cur)
		}
		cur = nil
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip inline comments
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		// Split keyword and argument (whitespace or '=')
		var key, val string
		if eqIdx := strings.IndexByte(line, '='); eqIdx > 0 && !strings.ContainsAny(line[:eqIdx], " \t") {
			key = strings.ToLower(strings.TrimSpace(line[:eqIdx]))
			val = strings.TrimSpace(line[eqIdx+1:])
		} else {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			key = strings.ToLower(fields[0])
			val = strings.TrimSpace(strings.TrimPrefix(line[len(fields[0]):], " "))
		}
		val = strings.Trim(val, `"'`)

		switch key {
		case "include":
			flushCur()
			for _, pat := range strings.Fields(val) {
				full := expandHomePath(pat)
				if !filepath.IsAbs(full) {
					full = filepath.Join(filepath.Dir(path), full)
				}
				matches, _ := filepath.Glob(full)
				for _, m := range matches {
					for _, h := range parseSSHConfigFile(m, depth+1) {
						if !seen[h.Alias] {
							seen[h.Alias] = true
							hosts = append(hosts, h)
						}
					}
				}
			}
		case "host":
			flushCur()
			// Only pick concrete single aliases (skip wildcards like `*` or `*.internal`)
			aliases := strings.Fields(val)
			for _, a := range aliases {
				if strings.ContainsAny(a, "*?!") {
					continue
				}
				cur = &SSHConfigHost{Alias: a, Port: 22}
				break
			}
		case "match":
			flushCur()
		case "hostname":
			if cur != nil && cur.HostName == "" {
				cur.HostName = val
			}
		case "user":
			if cur != nil && cur.User == "" {
				cur.User = val
			}
		case "port":
			if cur != nil {
				if p, err := strconv.Atoi(val); err == nil && p > 0 && p <= 65535 {
					cur.Port = p
				}
			}
		case "identityfile":
			if cur != nil && cur.IdentityFile == "" {
				cur.IdentityFile = val
			}
		}
	}
	flushCur()
	return hosts
}

func (s *Server) handleSSHConfigHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		writeJSON(w, http.StatusOK, []SSHConfigHost{})
		return
	}
	hosts := parseSSHConfigFile(filepath.Join(home, ".ssh", "config"), 0)
	if hosts == nil {
		hosts = []SSHConfigHost{}
	}
	writeJSON(w, http.StatusOK, hosts)
}

func sanitizeSSHTarget(r store.SSHTargetRecord) store.SSHTargetRecord {
	r.HasPassword = r.HasPassword || r.Password != ""
	r.Password = ""
	return r
}

func (s *Server) handleSSHTargets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.store.ListSSHTargets(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]store.SSHTargetRecord, 0, len(list))
		for _, item := range list {
			out = append(out, sanitizeSSHTarget(item))
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost, http.MethodPut:
		var rec store.SSHTargetRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		rec.Host = strings.TrimSpace(rec.Host)
		rec.Username = strings.TrimSpace(rec.Username)
		if rec.Host == "" {
			writeErr(w, http.StatusBadRequest, errors.New("host is required"))
			return
		}
		saved, err := s.store.UpsertSSHTarget(r.Context(), rec)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, sanitizeSSHTarget(saved))

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSSHTargetSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/ssh/targets/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch {
	case action == "" && r.Method == http.MethodDelete:
		if err := s.store.DeleteSSHTarget(r.Context(), id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})

	case action == "test" && r.Method == http.MethodPost:
		s.handleSSHTargetTest(w, r, id)

	case action == "deploy" && r.Method == http.MethodPost:
		s.handleSSHTargetDeploy(w, r, id)

	case action == "stop" && r.Method == http.MethodPost:
		s.handleSSHTargetStop(w, r, id)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) sshBinariesDir() string {
	return filepath.Join(s.store.DataDir(), "runner-binaries")
}

func normalizeTargetOSArch(rawOS, rawArch string) (string, string) {
	osName := strings.ToLower(strings.TrimSpace(rawOS))
	switch {
	case strings.Contains(osName, "darwin"):
		osName = "darwin"
	case strings.Contains(osName, "linux"):
		osName = "linux"
	}
	archName := strings.ToLower(strings.TrimSpace(rawArch))
	switch archName {
	case "x86_64", "amd64":
		archName = "amd64"
	case "aarch64", "arm64":
		archName = "arm64"
	}
	return osName, archName
}

func (s *Server) handleSSHBinaries(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		dir := s.sshBinariesDir()
		entries, _ := os.ReadDir(dir)
		out := make([]stagedBinaryInfo, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "nex-runner-") {
				continue
			}
			parts := strings.Split(strings.TrimPrefix(e.Name(), "nex-runner-"), "-")
			if len(parts) != 2 {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, stagedBinaryInfo{
				OS:        parts[0],
				Arch:      parts[1],
				SizeBytes: info.Size(),
				UpdatedAt: info.ModTime().UnixMilli(),
			})
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		targetOS, targetArch := normalizeTargetOSArch(r.URL.Query().Get("os"), r.URL.Query().Get("arch"))
		if (targetOS != "linux" && targetOS != "darwin") || (targetArch != "amd64" && targetArch != "arm64") {
			writeErr(w, http.StatusBadRequest, errors.New("os must be linux|darwin and arch must be amd64|arm64"))
			return
		}
		dir := s.sshBinariesDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		dst := filepath.Join(dir, fmt.Sprintf("nex-runner-%s-%s", targetOS, targetArch))
		tmp := dst + ".uploading"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		n, copyErr := io.Copy(f, io.LimitReader(r.Body, 128<<20))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || n == 0 {
			_ = os.Remove(tmp)
			if copyErr != nil {
				writeErr(w, http.StatusBadRequest, copyErr)
			} else {
				writeErr(w, http.StatusBadRequest, errors.New("empty binary upload"))
			}
			return
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, stagedBinaryInfo{
			OS:        targetOS,
			Arch:      targetArch,
			SizeBytes: n,
			UpdatedAt: time.Now().UnixMilli(),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSSHBinaryDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	targetOS, targetArch := normalizeTargetOSArch(r.URL.Query().Get("os"), r.URL.Query().Get("arch"))
	if targetOS == "" || targetArch == "" {
		writeErr(w, http.StatusBadRequest, errors.New("os and arch query params required"))
		return
	}
	dlTok := strings.TrimSpace(r.URL.Query().Get("dl_token"))
	enabled, authed := s.isRequestAuthenticated(r.Context(), r)
	if enabled && !authed && !validateBinaryDownloadToken(dlTok, targetOS, targetArch) {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized binary download"))
		return
	}
	binPath, err := s.resolveOrBuildRunnerBinary(r.Context(), targetOS, targetArch, nil)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"nex-runner-%s-%s\"", targetOS, targetArch))
	http.ServeFile(w, r, binPath)
}

func buildSSHBaseArgs(t store.SSHTargetRecord, forSCP bool) ([]string, string, func(), error) {
	args := []string{
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if t.Port > 0 && t.Port != 22 {
		if forSCP {
			args = append(args, "-P", strconv.Itoa(t.Port))
		} else {
			args = append(args, "-p", strconv.Itoa(t.Port))
		}
	}

	cleanup := func() {}
	askpassPath := ""

	switch t.AuthMethod {
	case "key":
		args = append(args, "-o", "BatchMode=yes")
		if strings.TrimSpace(t.KeyPath) != "" {
			args = append(args, "-i", expandHomePath(t.KeyPath), "-o", "IdentitiesOnly=yes")
		}
	case "password":
		if strings.TrimSpace(t.Password) == "" {
			return nil, "", cleanup, errors.New("SSH password is empty; please enter a password or switch to SSH key/agent")
		}
		tmpFile, err := os.CreateTemp("", "nex-askpass-*.sh")
		if err != nil {
			return nil, "", cleanup, err
		}
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %s\n", shellQuote(t.Password))
		if _, err := tmpFile.WriteString(script); err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpFile.Name())
			return nil, "", cleanup, err
		}
		_ = tmpFile.Chmod(0o700)
		_ = tmpFile.Close()
		askpassPath = tmpFile.Name()
		cleanup = func() { _ = os.Remove(askpassPath) }
		args = append(args, "-o", "PreferredAuthentications=password,keyboard-interactive", "-o", "PubkeyAuthentication=no")
	default: // agent
		args = append(args, "-o", "BatchMode=yes")
	}

	return args, askpassPath, cleanup, nil
}

func sshTargetSpec(t store.SSHTargetRecord) string {
	if strings.TrimSpace(t.Username) != "" {
		return fmt.Sprintf("%s@%s", strings.TrimSpace(t.Username), strings.TrimSpace(t.Host))
	}
	return strings.TrimSpace(t.Host)
}

func runSSHCommand(ctx context.Context, t store.SSHTargetRecord, remoteCmd string) (string, error) {
	args, askpass, cleanup, err := buildSSHBaseArgs(t, false)
	if err != nil {
		return "", err
	}
	defer cleanup()

	args = append(args, sshTargetSpec(t), remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = os.Environ()
	if askpass != "" {
		cmd.Env = append(cmd.Env,
			"SSH_ASKPASS="+askpass,
			"SSH_ASKPASS_REQUIRE=force",
			"DISPLAY=nex:0",
		)
		cmd.Stdin = nil
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

func runSCPFile(ctx context.Context, t store.SSHTargetRecord, localPath, remotePath string) (string, error) {
	args, askpass, cleanup, err := buildSSHBaseArgs(t, true)
	if err != nil {
		return "", err
	}
	defer cleanup()

	args = append(args, localPath, fmt.Sprintf("%s:%s", sshTargetSpec(t), remotePath))
	cmd := exec.CommandContext(ctx, "scp", args...)
	cmd.Env = os.Environ()
	if askpass != "" {
		cmd.Env = append(cmd.Env,
			"SSH_ASKPASS="+askpass,
			"SSH_ASKPASS_REQUIRE=force",
			"DISPLAY=nex:0",
		)
		cmd.Stdin = nil
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

type remoteProbeInfo struct {
	OS            string
	Arch          string
	Hostname      string
	HomeDir       string
	RunnerRunning bool
}

func probeRemoteHost(ctx context.Context, t store.SSHTargetRecord) (remoteProbeInfo, string, error) {
	script := `printf "OS=%s\nARCH=%s\nHOST=%s\nHOME=%s\nRUNNING=%s\n" "$(uname -s)" "$(uname -m)" "$(hostname)" "$HOME" "$(pgrep -f nex-runner >/dev/null 2>&1 && echo 1 || echo 0)"`
	out, err := runSSHCommand(ctx, t, script)
	if err != nil {
		return remoteProbeInfo{}, out, err
	}
	var info remoteProbeInfo
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "OS":
			info.OS = v
		case "ARCH":
			info.Arch = v
		case "HOST":
			info.Hostname = v
		case "HOME":
			info.HomeDir = v
		case "RUNNING":
			info.RunnerRunning = v == "1"
		}
	}
	info.OS, info.Arch = normalizeTargetOSArch(info.OS, info.Arch)
	return info, out, nil
}

func (s *Server) handleSSHTargetTest(w http.ResponseWriter, r *http.Request, id string) {
	target, err := s.store.GetSSHTarget(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	info, rawOut, probeErr := probeRemoteHost(ctx, *target)
	if probeErr != nil {
		msg := strings.TrimSpace(rawOut)
		if msg == "" {
			msg = probeErr.Error()
		}
		_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", msg, msg, 0)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": msg,
			"log":   msg,
		})
		return
	}

	logMsg := fmt.Sprintf("Connected to %s (%s/%s, home=%s, runner_running=%v)", info.Hostname, info.OS, info.Arch, info.HomeDir, info.RunnerRunning)
	status := "idle"
	if info.RunnerRunning {
		status = "connected"
	}
	_ = s.store.UpdateSSHTargetStatus(r.Context(), id, status, "", logMsg, 0)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"os":             info.OS,
		"arch":           info.Arch,
		"hostname":       info.Hostname,
		"home_dir":       info.HomeDir,
		"runner_running": info.RunnerRunning,
		"log":            logMsg,
	})
}

func (s *Server) resolveOrBuildRunnerBinary(ctx context.Context, targetOS, targetArch string, logBuf *strings.Builder) (string, error) {
	logf := func(format string, args ...any) {
		if logBuf != nil {
			fmt.Fprintf(logBuf, format+"\n", args...)
		}
	}

	// 1. Check explicitly uploaded/cached binary under <dataDir>/runner-binaries/
	staged := filepath.Join(s.sshBinariesDir(), fmt.Sprintf("nex-runner-%s-%s", targetOS, targetArch))
	if st, err := os.Stat(staged); err == nil && st.Size() > 0 {
		logf("Using staged binary: %s (%d bytes)", staged, st.Size())
		return staged, nil
	}

	// 2. If target matches host OS/Arch, check local nex-runner binaries next to executable or ./bin
	if targetOS == runtime.GOOS && targetArch == runtime.GOARCH {
		candidates := []string{"./bin/nex-runner", "/opt/nex-v3/bin/nex-runner"}
		if exe, err := os.Executable(); err == nil {
			candidates = append([]string{filepath.Join(filepath.Dir(exe), "nex-runner")}, candidates...)
		}
		for _, c := range candidates {
			if st, err := os.Stat(c); err == nil && st.Size() > 0 {
				logf("Using local host binary: %s (%d bytes)", c, st.Size())
				return c, nil
			}
		}
	}

	// 3. If running inside a Go source tree with cmd/nex-runner, cross-compile cleanly (CGO_ENABLED=0)!
	if _, err := os.Stat("cmd/nex-runner/main.go"); err == nil {
		if _, goErr := exec.LookPath("go"); goErr == nil {
			if err := os.MkdirAll(s.sshBinariesDir(), 0o700); err == nil {
				logf("Cross-compiling nex-runner for %s/%s (CGO_ENABLED=0)...", targetOS, targetArch)
				cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", staged, "./cmd/nex-runner")
				cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+targetOS, "GOARCH="+targetArch)
				if out, bErr := cmd.CombinedOutput(); bErr == nil {
					logf("Built %s successfully.", staged)
					return staged, nil
				} else {
					logf("Cross-compile failed: %s", strings.TrimSpace(string(out)))
				}
			}
		}
	}

	return "", fmt.Errorf("no nex-runner binary found for %s/%s; upload one in Settings -> Remote Devices or switch to Remote Install URL", targetOS, targetArch)
}

func resolveServerEndpointsForRemote(r *http.Request, publicBase, overrideWS string) (wsURL, httpBase string) {
	httpBase = strings.TrimRight(strings.TrimSpace(publicBase), "/")
	if httpBase == "" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		host := r.Host
		if host == "" {
			host = "127.0.0.1:8080"
		}
		httpBase = scheme + "://" + host
	}
	if strings.TrimSpace(overrideWS) != "" {
		return strings.TrimSpace(overrideWS), httpBase
	}
	if strings.HasPrefix(httpBase, "https://") {
		return "wss://" + strings.TrimPrefix(httpBase, "https://") + "/ws/runner", httpBase
	}
	return "ws://" + strings.TrimPrefix(httpBase, "http://") + "/ws/runner", httpBase
}

func (s *Server) handleSSHTargetDeploy(w http.ResponseWriter, r *http.Request, id string) {
	target, err := s.store.GetSSHTarget(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	var logBuf strings.Builder
	logf := func(format string, args ...any) {
		fmt.Fprintf(&logBuf, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}

	_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "deploying", "", "Deploying...", 0)

	// Step 1: Probe remote OS / Arch / Home
	logf("Probing %s...", sshTargetSpec(*target))
	info, rawProbe, err := probeRemoteHost(ctx, *target)
	if err != nil {
		errMsg := strings.TrimSpace(rawProbe)
		if errMsg == "" {
			errMsg = err.Error()
		}
		logf("SSH probe failed: %s", errMsg)
		_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", errMsg, logBuf.String(), 0)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errMsg, "log": logBuf.String()})
		return
	}
	logf("Remote host: %s (%s/%s, home=%s)", info.Hostname, info.OS, info.Arch, info.HomeDir)

	installDir := strings.TrimSpace(target.InstallDir)
	if installDir == "" {
		installDir = "~/.nex-runner"
	}
	if strings.HasPrefix(installDir, "~/") && info.HomeDir != "" {
		installDir = filepath.ToSlash(filepath.Join(info.HomeDir, installDir[2:]))
	} else if installDir == "~" && info.HomeDir != "" {
		installDir = info.HomeDir
	}
	workspace := strings.TrimSpace(target.WorkspacePath)
	if workspace == "" {
		workspace = info.HomeDir
	}

	wsURL, httpBase := resolveServerEndpointsForRemote(r, s.cfg.PublicBaseURL, target.ServerWSURL)
	logf("Install dir: %s | Runner WS target: %s", installDir, wsURL)

	// Step 2: Prepare remote directory
	if out, err := runSSHCommand(ctx, *target, fmt.Sprintf("mkdir -p %s/bin %s/data", shellQuote(installDir), shellQuote(installDir))); err != nil {
		errMsg := strings.TrimSpace(out)
		if errMsg == "" {
			errMsg = err.Error()
		}
		logf("Failed to create install directory: %s", errMsg)
		_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", errMsg, logBuf.String(), 0)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errMsg, "log": logBuf.String()})
		return
	}

	remoteBinNew := installDir + "/bin/nex-runner.new"
	remoteBin := installDir + "/bin/nex-runner"

	// Step 3: Deliver binary via SCP OR Remote Install
	if target.InstallMode == "remote_install" {
		dlURL := strings.TrimSpace(target.DownloadURL)
		if dlURL == "" {
			// Ensure binary is available on this Server and issue a 5-min one-time download token
			if _, bErr := s.resolveOrBuildRunnerBinary(ctx, info.OS, info.Arch, &logBuf); bErr != nil {
				logf("Error: %v", bErr)
				_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", bErr.Error(), logBuf.String(), 0)
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": bErr.Error(), "log": logBuf.String()})
				return
			}
			tok := issueBinaryDownloadToken(info.OS, info.Arch)
			dlURL = fmt.Sprintf("%s/api/v1/ssh/binary?os=%s&arch=%s&dl_token=%s", httpBase, info.OS, info.Arch, tok)
			logf("Remote installing from Nex server binary endpoint (%s/%s)...", info.OS, info.Arch)
		} else {
			dlURL = strings.ReplaceAll(dlURL, "{os}", info.OS)
			dlURL = strings.ReplaceAll(dlURL, "{arch}", info.Arch)
			logf("Remote installing from URL: %s", dlURL)
		}

		dlCmd := fmt.Sprintf(
			"if command -v curl >/dev/null 2>&1; then curl -fsSL %s -o %s; elif command -v wget >/dev/null 2>&1; then wget -qO %s %s; else echo 'neither curl nor wget installed' >&2; exit 1; fi && chmod +x %s && mv -f %s %s",
			shellQuote(dlURL), shellQuote(remoteBinNew),
			shellQuote(remoteBinNew), shellQuote(dlURL),
			shellQuote(remoteBinNew), shellQuote(remoteBinNew), shellQuote(remoteBin),
		)
		if out, err := runSSHCommand(ctx, *target, dlCmd); err != nil {
			errMsg := strings.TrimSpace(out)
			if errMsg == "" {
				errMsg = err.Error()
			}
			logf("Remote install download failed: %s", errMsg)
			_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", errMsg, logBuf.String(), 0)
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errMsg, "log": logBuf.String()})
			return
		}
		logf("Remote download complete.")
	} else {
		// SCP upload mode
		localBin, bErr := s.resolveOrBuildRunnerBinary(ctx, info.OS, info.Arch, &logBuf)
		if bErr != nil {
			logf("Error: %v", bErr)
			_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", bErr.Error(), logBuf.String(), 0)
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": bErr.Error(), "log": logBuf.String()})
			return
		}
		logf("Uploading %s via SCP to %s...", localBin, remoteBinNew)
		if out, err := runSCPFile(ctx, *target, localBin, remoteBinNew); err != nil {
			errMsg := strings.TrimSpace(out)
			if errMsg == "" {
				errMsg = err.Error()
			}
			logf("SCP upload failed: %s", errMsg)
			_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", errMsg, logBuf.String(), 0)
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errMsg, "log": logBuf.String()})
			return
		}
		if out, err := runSSHCommand(ctx, *target, fmt.Sprintf("chmod +x %s && mv -f %s %s", shellQuote(remoteBinNew), shellQuote(remoteBinNew), shellQuote(remoteBin))); err != nil {
			errMsg := strings.TrimSpace(out)
			if errMsg == "" {
				errMsg = err.Error()
			}
			logf("Swap binary failed: %s", errMsg)
			_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", errMsg, logBuf.String(), 0)
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errMsg, "log": logBuf.String()})
			return
		}
		logf("SCP upload and swap complete.")
	}

	// Step 4: Restart remote nex-runner daemon and verify PID
	runnerID := "run_" + strings.ToLower(strings.ReplaceAll(strings.TrimSpace(target.Name), " ", "_"))
	pidFile := installDir + "/runner.pid"
	logFile := installDir + "/runner.log"
	dataDir := installDir + "/data"

	startCmd := fmt.Sprintf(
		"if [ -f %s ]; then oldpid=$(cat %s); kill \"$oldpid\" 2>/dev/null || true; sleep 0.5; fi; "+
			"nohup %s -id %s -name %s -server %s -workspace %s -data %s > %s 2>&1 < /dev/null & "+
			"newpid=$!; echo \"$newpid\" > %s; sleep 1; "+
			"if kill -0 \"$newpid\" 2>/dev/null; then echo \"STARTED_PID=$newpid\"; tail -n 10 %s 2>/dev/null || true; else echo 'Runner exited immediately:' >&2; tail -n 20 %s >&2; exit 1; fi",
		shellQuote(pidFile), shellQuote(pidFile),
		shellQuote(remoteBin), shellQuote(runnerID), shellQuote(target.Name), shellQuote(wsURL), shellQuote(workspace), shellQuote(dataDir), shellQuote(logFile),
		shellQuote(pidFile), shellQuote(logFile), shellQuote(logFile),
	)

	startOut, startErr := runSSHCommand(ctx, *target, startCmd)
	if strings.TrimSpace(startOut) != "" {
		logf("Remote output:\n%s", strings.TrimSpace(startOut))
	}
	if startErr != nil {
		errMsg := strings.TrimSpace(startOut)
		if errMsg == "" {
			errMsg = startErr.Error()
		}
		logf("Failed to start remote runner: %s", errMsg)
		_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", errMsg, logBuf.String(), 0)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errMsg, "log": logBuf.String()})
		return
	}

	logf("Deployment succeeded! Runner %q is dialing %s.", target.Name, wsURL)
	_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "connected", "", logBuf.String(), time.Now().UnixMilli())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"runner_id": runnerID,
		"os":        info.OS,
		"arch":      info.Arch,
		"log":       logBuf.String(),
	})
}

func (s *Server) handleSSHTargetStop(w http.ResponseWriter, r *http.Request, id string) {
	target, err := s.store.GetSSHTarget(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	installDir := strings.TrimSpace(target.InstallDir)
	if installDir == "" {
		installDir = "~/.nex-runner"
	}
	stopCmd := fmt.Sprintf("pidf=%s/runner.pid; if [ -f \"$pidf\" ]; then kill $(cat \"$pidf\") 2>/dev/null || true; rm -f \"$pidf\"; echo 'Stopped runner.'; else echo 'No pid file found.'; fi", installDir)
	out, stopErr := runSSHCommand(ctx, *target, stopCmd)
	if stopErr != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = stopErr.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg, "log": msg})
		return
	}
	_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "idle", "", strings.TrimSpace(out), 0)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "log": strings.TrimSpace(out)})
}
