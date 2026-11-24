package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/base64"
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
	"sort"
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
	Source    string `json:"source,omitempty"`
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
	case strings.Contains(osName, "windows"), strings.Contains(osName, "mingw"), strings.Contains(osName, "msys"), strings.Contains(osName, "cygwin"), strings.Contains(osName, "win32"):
		osName = "windows"
	}
	archName := strings.ToLower(strings.TrimSpace(rawArch))
	switch archName {
	case "x86_64", "amd64", "x64":
		archName = "amd64"
	case "aarch64", "arm64":
		archName = "arm64"
	}
	return osName, archName
}

func runnerBinaryFileName(targetOS, targetArch string) string {
	if targetOS == "windows" {
		return fmt.Sprintf("nex-runner-%s-%s.exe", targetOS, targetArch)
	}
	return fmt.Sprintf("nex-runner-%s-%s", targetOS, targetArch)
}

func detectBinaryOSArch(data []byte) (string, string, error) {
	r := bytes.NewReader(data)
	if ef, err := elf.NewFile(r); err == nil {
		_ = ef.Close()
		switch ef.Machine {
		case elf.EM_X86_64:
			return "linux", "amd64", nil
		case elf.EM_AARCH64:
			return "linux", "arm64", nil
		default:
			return "", "", fmt.Errorf("unsupported ELF arch: %v", ef.Machine)
		}
	}
	if mf, err := macho.NewFile(r); err == nil {
		_ = mf.Close()
		switch mf.Cpu {
		case macho.CpuAmd64:
			return "darwin", "amd64", nil
		case macho.CpuArm64:
			return "darwin", "arm64", nil
		default:
			return "", "", fmt.Errorf("unsupported Mach-O cpu: %v", mf.Cpu)
		}
	}
	if ff, err := macho.NewFatFile(r); err == nil {
		defer ff.Close()
		hasAMD64 := false
		for _, arch := range ff.Arches {
			if arch.Cpu == macho.CpuArm64 {
				return "darwin", "arm64", nil
			}
			if arch.Cpu == macho.CpuAmd64 {
				hasAMD64 = true
			}
		}
		if hasAMD64 {
			return "darwin", "amd64", nil
		}
		return "", "", errors.New("unsupported Mach-O universal binary")
	}
	if pf, err := pe.NewFile(r); err == nil {
		_ = pf.Close()
		switch pf.Machine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			return "windows", "amd64", nil
		case pe.IMAGE_FILE_MACHINE_ARM64:
			return "windows", "arm64", nil
		default:
			return "", "", fmt.Errorf("unsupported PE machine: %v", pf.Machine)
		}
	}
	return "", "", errors.New("unrecognized binary format (expected ELF, Mach-O, or PE)")
}

type runnerBinaryDirCandidate struct {
	dir    string
	source string
}

func (s *Server) candidateRunnersDirs() []runnerBinaryDirCandidate {
	var dirs []runnerBinaryDirCandidate
	seen := map[string]bool{}
	add := func(d, src string) {
		d = strings.TrimSpace(d)
		if d == "" {
			return
		}
		clean := filepath.Clean(d)
		if seen[clean] {
			return
		}
		seen[clean] = true
		dirs = append(dirs, runnerBinaryDirCandidate{dir: clean, source: src})
	}

	add(s.sshBinariesDir(), "staged")
	add(s.cfg.RunnersDir, "bundled")
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		add(filepath.Join(exeDir, "runners"), "bundled")
		add(filepath.Join(filepath.Dir(exeDir), "runners"), "bundled")
	}
	add("/opt/nex-v3/runners", "bundled")
	add("./bin/runners", "bundled")
	add("./dist/runners", "bundled")
	return dirs
}

func parseRunnerFileOSArch(name string) (string, string, bool) {
	if !strings.HasPrefix(name, "nex-runner-") {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, "nex-runner-")
	rest = strings.TrimSuffix(rest, ".exe")
	parts := strings.Split(rest, "-")
	if len(parts) != 2 {
		return "", "", false
	}
	osName, archName := normalizeTargetOSArch(parts[0], parts[1])
	if (osName != "linux" && osName != "darwin" && osName != "windows") || (archName != "amd64" && archName != "arm64") {
		return "", "", false
	}
	return osName, archName, true
}

func (s *Server) listAvailableRunnerBinaries() []stagedBinaryInfo {
	byKey := map[string]stagedBinaryInfo{}
	for _, cand := range s.candidateRunnersDirs() {
		entries, err := os.ReadDir(cand.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			osName, archName, ok := parseRunnerFileOSArch(e.Name())
			if !ok {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() <= 0 {
				continue
			}
			key := osName + "/" + archName
			if _, exists := byKey[key]; !exists {
				byKey[key] = stagedBinaryInfo{
					OS:        osName,
					Arch:      archName,
					SizeBytes: info.Size(),
					UpdatedAt: info.ModTime().UnixMilli(),
					Source:    cand.source,
				}
			}
		}
	}

	// Host binary fallback for current runtime.GOOS/runtime.GOARCH
	hostKey := runtime.GOOS + "/" + runtime.GOARCH
	if _, exists := byKey[hostKey]; !exists {
		exeName := "nex-runner"
		if runtime.GOOS == "windows" {
			exeName = "nex-runner.exe"
		}
		candidates := []string{filepath.Join("./bin", exeName), filepath.Join("/opt/nex-v3/bin", exeName)}
		if exe, err := os.Executable(); err == nil {
			candidates = append([]string{filepath.Join(filepath.Dir(exe), exeName)}, candidates...)
		}
		for _, c := range candidates {
			if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Size() > 0 {
				byKey[hostKey] = stagedBinaryInfo{
					OS:        runtime.GOOS,
					Arch:      runtime.GOARCH,
					SizeBytes: st.Size(),
					UpdatedAt: st.ModTime().UnixMilli(),
					Source:    "local",
				}
				break
			}
		}
	}

	out := make([]stagedBinaryInfo, 0, len(byKey))
	for _, v := range byKey {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OS != out[j].OS {
			return out[i].OS < out[j].OS
		}
		return out[i].Arch < out[j].Arch
	})
	return out
}

func (s *Server) handleSSHBinaries(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.listAvailableRunnerBinaries())

	case http.MethodPost:
		dir := s.sshBinariesDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		tmp := filepath.Join(dir, fmt.Sprintf("nex-runner-%d.uploading", time.Now().UnixNano()))
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

		rawOS := strings.TrimSpace(r.URL.Query().Get("os"))
		rawArch := strings.TrimSpace(r.URL.Query().Get("arch"))
		var targetOS, targetArch string
		if rawOS != "" && rawArch != "" {
			targetOS, targetArch = normalizeTargetOSArch(rawOS, rawArch)
		} else {
			headerBytes, _ := os.ReadFile(tmp)
			detOS, detArch, detErr := detectBinaryOSArch(headerBytes)
			if detErr != nil {
				_ = os.Remove(tmp)
				writeErr(w, http.StatusBadRequest, fmt.Errorf("failed to detect binary os/arch: %w", detErr))
				return
			}
			targetOS, targetArch = detOS, detArch
		}
		if (targetOS != "linux" && targetOS != "darwin" && targetOS != "windows") || (targetArch != "amd64" && targetArch != "arm64") {
			_ = os.Remove(tmp)
			writeErr(w, http.StatusBadRequest, errors.New("os must be linux|darwin|windows and arch must be amd64|arm64"))
			return
		}
		dst := filepath.Join(dir, runnerBinaryFileName(targetOS, targetArch))
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
			Source:    "staged",
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
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", runnerBinaryFileName(targetOS, targetArch)))
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

func parseProbeLines(out string) remoteProbeInfo {
	var info remoteProbeInfo
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToUpper(strings.TrimSpace(k)) {
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
	return info
}

func probeRemoteHost(ctx context.Context, t store.SSHTargetRecord) (remoteProbeInfo, string, error) {
	posixScript := `printf "OS=%s\nARCH=%s\nHOST=%s\nHOME=%s\nRUNNING=%s\n" "$(uname -s)" "$(uname -m)" "$(hostname)" "${HOME:-$(eval echo ~)}" "$(pgrep -f nex-runner >/dev/null 2>&1 && echo 1 || echo 0)"`
	out, err := runSSHCommand(ctx, t, posixScript)
	if err == nil {
		info := parseProbeLines(out)
		if info.OS != "" && info.Arch != "" {
			return info, out, nil
		}
	}

	// Fallback for Windows OpenSSH targets (cmd.exe / PowerShell)
	cmdScript := `cmd /c "echo OS=windows& echo ARCH=%PROCESSOR_ARCHITECTURE%& echo HOST=%COMPUTERNAME%& echo HOME=%USERPROFILE%"`
	winOut, winErr := runSSHCommand(ctx, t, cmdScript)
	if winErr == nil {
		info := parseProbeLines(winOut)
		if info.OS != "" && info.HomeDir != "" {
			if info.Arch == "" {
				info.Arch = "amd64"
			}
			return info, winOut, nil
		}
	}

	psScript := `powershell -NoProfile -NonInteractive -Command "Write-Output 'OS=windows'; Write-Output ('ARCH=' + $env:PROCESSOR_ARCHITECTURE); Write-Output ('HOST=' + $env:COMPUTERNAME); Write-Output ('HOME=' + $env:USERPROFILE)"`
	psOut, psErr := runSSHCommand(ctx, t, psScript)
	if psErr == nil {
		info := parseProbeLines(psOut)
		if info.OS != "" {
			if info.Arch == "" {
				info.Arch = "amd64"
			}
			return info, psOut, nil
		}
	}

	if err != nil {
		return remoteProbeInfo{}, out, err
	}
	return remoteProbeInfo{}, winOut, winErr
}

func joinRemotePath(base, rel, targetOS string) string {
	rel = strings.TrimLeft(rel, "/\\")
	if targetOS == "windows" {
		base = strings.TrimRight(strings.ReplaceAll(base, "/", "\\"), "\\")
		if rel == "" {
			return base
		}
		return base + "\\" + strings.ReplaceAll(rel, "/", "\\")
	}
	base = strings.TrimRight(base, "/")
	if rel == "" {
		return base
	}
	return base + "/" + strings.ReplaceAll(rel, "\\", "/")
}

func inferRemoteHomeDir(targetOS, username string) string {
	u := strings.TrimSpace(username)
	switch targetOS {
	case "darwin":
		if u == "root" {
			return "/var/root"
		}
		if u != "" {
			return "/Users/" + u
		}
		return "/Users"
	case "windows":
		if u != "" {
			return `C:\Users\` + u
		}
		return `C:\Users\Public`
	default: // linux
		if u == "root" || u == "" {
			return "/root"
		}
		return "/home/" + u
	}
}

func expandRemoteEnvPath(raw, remoteHome, targetOS string) string {
	p := strings.TrimSpace(raw)
	switch {
	case p == "", p == "~", p == "$HOME", p == "${HOME}", strings.EqualFold(p, "%USERPROFILE%"):
		return remoteHome
	case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\"):
		return joinRemotePath(remoteHome, p[2:], targetOS)
	case strings.HasPrefix(p, "$HOME/") || strings.HasPrefix(p, "$HOME\\"):
		return joinRemotePath(remoteHome, p[len("$HOME/"):], targetOS)
	case strings.HasPrefix(p, "${HOME}/") || strings.HasPrefix(p, "${HOME}\\"):
		return joinRemotePath(remoteHome, p[len("${HOME}/"):], targetOS)
	case len(p) >= len("%USERPROFILE%/") && strings.EqualFold(p[:len("%USERPROFILE%")], "%USERPROFILE%") && (p[len("%USERPROFILE%")] == '/' || p[len("%USERPROFILE%")] == '\\'):
		return joinRemotePath(remoteHome, p[len("%USERPROFILE%")+1:], targetOS)
	default:
		return p
	}
}

// resolveRemotePaths computes OS-aware installDir, workspace, and binary name for a probed remote host.
// Empty or "$HOME" workspace paths resolve to the remote host's detected $HOME (or OS-specific default).
func resolveRemotePaths(t store.SSHTargetRecord, info remoteProbeInfo) (installDir, workspace, binName string) {
	targetOS := info.OS
	if targetOS == "" {
		targetOS = "linux"
	}
	remoteHome := strings.TrimSpace(info.HomeDir)
	if remoteHome == "" {
		remoteHome = inferRemoteHomeDir(targetOS, t.Username)
	}

	rawInstall := strings.TrimSpace(t.InstallDir)
	if rawInstall == "" {
		rawInstall = "~/.nex-runner"
	}
	installDir = expandRemoteEnvPath(rawInstall, remoteHome, targetOS)
	workspace = expandRemoteEnvPath(t.WorkspacePath, remoteHome, targetOS)

	binName = "nex-runner"
	if targetOS == "windows" {
		binName = "nex-runner.exe"
	}
	return installDir, workspace, binName
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

	serverOnline := s.isRunnerOnline(sshTargetRunnerID(target.Name))
	logMsg := fmt.Sprintf("Connected to %s (%s/%s, home=%s, runner_running=%v, server_online=%v)", info.Hostname, info.OS, info.Arch, info.HomeDir, info.RunnerRunning, serverOnline)
	status := "idle"
	if serverOnline {
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
		"server_online":  serverOnline,
		"log":            logMsg,
	})
}

func sshTargetRunnerID(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	s := strings.Trim(sb.String(), "_")
	if s == "" {
		s = "remote"
	}
	return "run_" + s
}

const darwinRunnerBundleID = "com.nex.runner"

func darwinLaunchAgentLabel(runnerID string) string {
	return "com.nex.runner." + runnerID
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

func buildDarwinAppInfoPlist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key>
  <string>` + darwinRunnerBundleID + `</string>
  <key>CFBundleName</key>
  <string>NexRunner</string>
  <key>CFBundleDisplayName</key>
  <string>NexRunner</string>
  <key>CFBundleExecutable</key>
  <string>nex-runner</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleVersion</key>
  <string>1</string>
  <key>CFBundleShortVersionString</key>
  <string>1.0</string>
  <key>LSUIElement</key>
  <true/>
  <key>NSLocalNetworkUsageDescription</key>
  <string>NexRunner connects to your Nex server on the local network.</string>
</dict>
</plist>
`
}

func buildDarwinLaunchAgentPlist(label, binPath, runnerID, name, wsURL, workspace, dataDir, pairingToken, homeDir, logFile string) string {
	args := []string{
		binPath,
		"-id", runnerID,
		"-name", name,
		"-server", wsURL,
		"-workspace", workspace,
		"-data-dir", dataDir,
	}
	if pairingToken != "" {
		args = append(args, "-token", pairingToken)
	}
	var sb strings.Builder
	sb.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	sb.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	sb.WriteString("<plist version=\"1.0\">\n<dict>\n")
	sb.WriteString("  <key>Label</key>\n  <string>" + xmlEscape(label) + "</string>\n")
	sb.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range args {
		sb.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	sb.WriteString("  </array>\n")
	sb.WriteString("  <key>AssociatedBundleIdentifiers</key>\n  <array>\n")
	sb.WriteString("    <string>" + darwinRunnerBundleID + "</string>\n")
	sb.WriteString("  </array>\n")
	sb.WriteString("  <key>WorkingDirectory</key>\n  <string>" + xmlEscape(workspace) + "</string>\n")
	sb.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	sb.WriteString("    <key>HOME</key>\n    <string>" + xmlEscape(homeDir) + "</string>\n")
	sb.WriteString("    <key>PATH</key>\n    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>\n")
	sb.WriteString("  </dict>\n")
	sb.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	sb.WriteString("  <key>KeepAlive</key>\n  <true/>\n")
	sb.WriteString("  <key>StandardOutPath</key>\n  <string>" + xmlEscape(logFile) + "</string>\n")
	sb.WriteString("  <key>StandardErrorPath</key>\n  <string>" + xmlEscape(logFile) + "</string>\n")
	sb.WriteString("</dict>\n</plist>\n")
	return sb.String()
}

func (s *Server) resolveOrBuildRunnerBinary(ctx context.Context, targetOS, targetArch string, logBuf *strings.Builder) (string, error) {
	logf := func(format string, args ...any) {
		if logBuf != nil {
			fmt.Fprintf(logBuf, format+"\n", args...)
		}
	}

	fileName := runnerBinaryFileName(targetOS, targetArch)

	// 1. Check staged & bundled runner directories (<dataDir>/runner-binaries, Config.RunnersDir, <exeDir>/runners, /opt/nex-v3/runners, ./bin/runners)
	for _, cand := range s.candidateRunnersDirs() {
		p := filepath.Join(cand.dir, fileName)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			logf("Using %s binary: %s (%d bytes)", cand.source, p, st.Size())
			return p, nil
		}
	}

	// 2. If target matches host OS/Arch, check local nex-runner binaries next to executable or ./bin
	if targetOS == runtime.GOOS && targetArch == runtime.GOARCH {
		exeName := "nex-runner"
		if targetOS == "windows" {
			exeName = "nex-runner.exe"
		}
		candidates := []string{filepath.Join("./bin", exeName), filepath.Join("/opt/nex-v3/bin", exeName)}
		if exe, err := os.Executable(); err == nil {
			candidates = append([]string{filepath.Join(filepath.Dir(exe), exeName)}, candidates...)
		}
		for _, c := range candidates {
			if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Size() > 0 {
				logf("Using local host binary: %s (%d bytes)", c, st.Size())
				return c, nil
			}
		}
	}

	// 3. If running inside a Go source tree with cmd/nex-runner, cross-compile cleanly (CGO_ENABLED=0)!
	staged := filepath.Join(s.sshBinariesDir(), fileName)
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

	return "", fmt.Errorf("no nex-runner binary found for %s/%s in bundled runners directory or local build environment", targetOS, targetArch)
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

	installDir, workspace, binName := resolveRemotePaths(*target, info)

	wsURL, httpBase := resolveServerEndpointsForRemote(r, s.cfg.PublicBaseURL, target.ServerWSURL)
	logf("Install dir: %s | Workspace: %s | Runner WS target: %s", installDir, workspace, wsURL)

	remoteBinDir := joinRemotePath(installDir, "bin", info.OS)
	remoteDataDir := joinRemotePath(installDir, "data", info.OS)
	remoteBin := joinRemotePath(remoteBinDir, binName, info.OS)
	remoteBinNew := remoteBin + ".new"

	// Step 2: Prepare remote directory
	var mkdirCmd string
	if info.OS == "windows" {
		mkdirCmd = fmt.Sprintf(
			`powershell -NoProfile -NonInteractive -Command "New-Item -ItemType Directory -Force -Path '%s','%s' | Out-Null"`,
			strings.ReplaceAll(remoteBinDir, "'", "''"),
			strings.ReplaceAll(remoteDataDir, "'", "''"),
		)
	} else {
		mkdirCmd = fmt.Sprintf("mkdir -p %s %s", shellQuote(remoteBinDir), shellQuote(remoteDataDir))
	}
	if out, err := runSSHCommand(ctx, *target, mkdirCmd); err != nil {
		errMsg := strings.TrimSpace(out)
		if errMsg == "" {
			errMsg = err.Error()
		}
		logf("Failed to create install directory: %s", errMsg)
		_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", errMsg, logBuf.String(), 0)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errMsg, "log": logBuf.String()})
		return
	}

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

		var dlCmd string
		if info.OS == "windows" {
			dlCmd = fmt.Sprintf(
				`powershell -NoProfile -NonInteractive -Command "Invoke-WebRequest -UseBasicParsing -Uri '%s' -OutFile '%s'; Move-Item -Force '%s' '%s'"`,
				strings.ReplaceAll(dlURL, "'", "''"),
				strings.ReplaceAll(remoteBinNew, "'", "''"),
				strings.ReplaceAll(remoteBinNew, "'", "''"),
				strings.ReplaceAll(remoteBin, "'", "''"),
			)
		} else {
			dlCmd = fmt.Sprintf(
				"if command -v curl >/dev/null 2>&1; then curl -fsSL %s -o %s; elif command -v wget >/dev/null 2>&1; then wget -qO %s %s; else echo 'neither curl nor wget installed' >&2; exit 1; fi && chmod +x %s && mv -f %s %s",
				shellQuote(dlURL), shellQuote(remoteBinNew),
				shellQuote(remoteBinNew), shellQuote(dlURL),
				shellQuote(remoteBinNew), shellQuote(remoteBinNew), shellQuote(remoteBin),
			)
		}
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
		scpDest := remoteBinNew
		if info.OS == "windows" {
			scpDest = strings.ReplaceAll(remoteBinNew, "\\", "/")
		}
		logf("Uploading %s via SCP to %s...", localBin, scpDest)
		if out, err := runSCPFile(ctx, *target, localBin, scpDest); err != nil {
			errMsg := strings.TrimSpace(out)
			if errMsg == "" {
				errMsg = err.Error()
			}
			logf("SCP upload failed: %s", errMsg)
			_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", errMsg, logBuf.String(), 0)
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errMsg, "log": logBuf.String()})
			return
		}
		var swapCmd string
		if info.OS == "windows" {
			swapCmd = fmt.Sprintf(
				`powershell -NoProfile -NonInteractive -Command "Move-Item -Force '%s' '%s'"`,
				strings.ReplaceAll(remoteBinNew, "'", "''"),
				strings.ReplaceAll(remoteBin, "'", "''"),
			)
		} else {
			swapCmd = fmt.Sprintf("chmod +x %s && mv -f %s %s", shellQuote(remoteBinNew), shellQuote(remoteBinNew), shellQuote(remoteBin))
		}
		if out, err := runSSHCommand(ctx, *target, swapCmd); err != nil {
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
	runnerID := sshTargetRunnerID(target.Name)
	pidFile := joinRemotePath(installDir, "runner.pid", info.OS)
	logFile := joinRemotePath(installDir, "runner.log", info.OS)

	var startCmd string
	switch info.OS {
	case "windows":
		psArgs := fmt.Sprintf(`'-id','%s','-name','%s','-server','%s','-workspace','%s','-data-dir','%s'`,
			strings.ReplaceAll(runnerID, "'", "''"),
			strings.ReplaceAll(target.Name, "'", "''"),
			strings.ReplaceAll(wsURL, "'", "''"),
			strings.ReplaceAll(workspace, "'", "''"),
			strings.ReplaceAll(remoteDataDir, "'", "''"),
		)
		effectiveSecret, _ := s.effectiveRunnerPairingSecret(ctx)
		if effectiveSecret != "" {
			psArgs += fmt.Sprintf(`','-token','%s`, strings.ReplaceAll(effectiveSecret, "'", "''"))
		}
		startCmd = fmt.Sprintf(
			`powershell -NoProfile -NonInteractive -Command "$pidFile='%s'; $logFile='%s'; if (Test-Path $pidFile) { $old=Get-Content $pidFile -ErrorAction SilentlyContinue; if ($old) { Stop-Process -Id ([int]$old) -Force -ErrorAction SilentlyContinue }; Start-Sleep -Milliseconds 500 }; $p = Start-Process -FilePath '%s' -ArgumentList @(%s) -RedirectStandardOutput $logFile -RedirectStandardError ($logFile + '.err') -WindowStyle Hidden -PassThru; Set-Content -Path $pidFile -Value $p.Id; Start-Sleep -Seconds 1; if (Get-Process -Id $p.Id -ErrorAction SilentlyContinue) { Write-Output ('STARTED_PID=' + $p.Id) } else { Write-Error 'Runner exited immediately'; exit 1 }"`,
			strings.ReplaceAll(pidFile, "'", "''"),
			strings.ReplaceAll(logFile, "'", "''"),
			strings.ReplaceAll(remoteBin, "'", "''"),
			psArgs,
		)

	case "darwin":
		remoteHome := strings.TrimSpace(info.HomeDir)
		if remoteHome == "" {
			remoteHome = inferRemoteHomeDir("darwin", target.Username)
		}
		appBundleDir := joinRemotePath(installDir, "NexRunner.app", "darwin")
		appContentsDir := joinRemotePath(appBundleDir, "Contents", "darwin")
		appMacOSDir := joinRemotePath(appContentsDir, "MacOS", "darwin")
		appInfoPlist := joinRemotePath(appContentsDir, "Info.plist", "darwin")
		appBin := joinRemotePath(appMacOSDir, "nex-runner", "darwin")
		infoPlistB64 := base64.StdEncoding.EncodeToString([]byte(buildDarwinAppInfoPlist()))

		label := darwinLaunchAgentLabel(runnerID)
		launchAgentsDir := joinRemotePath(remoteHome, "Library/LaunchAgents", "darwin")
		plistPath := joinRemotePath(launchAgentsDir, label+".plist", "darwin")
		effectiveSecret, _ := s.effectiveRunnerPairingSecret(ctx)
		plistContent := buildDarwinLaunchAgentPlist(
			label, appBin, runnerID, target.Name, wsURL, workspace, remoteDataDir, effectiveSecret, remoteHome, logFile,
		)
		plistB64 := base64.StdEncoding.EncodeToString([]byte(plistContent))
		tokenFlag := ""
		if effectiveSecret != "" {
			tokenFlag = " -token " + shellQuote(effectiveSecret)
		}
		logf("Installing macOS NexRunner.app (%s) & LaunchAgent %s (%s)...", appBundleDir, label, plistPath)
		startCmd = fmt.Sprintf(
			"if [ -f %s ]; then oldpid=$(cat %s 2>/dev/null || true); [ -n \"$oldpid\" ] && kill \"$oldpid\" 2>/dev/null || true; rm -f %s; fi; "+
				"pkill -f -- %s 2>/dev/null || true; "+
				"mkdir -p %s %s && printf '%%s' %s | base64 -d > %s && cp -f %s %s && chmod +x %s && "+
				"xattr -dr com.apple.quarantine %s 2>/dev/null || true; "+
				"if command -v codesign >/dev/null 2>&1; then codesign --force --deep --sign - %s 2>/dev/null || true; fi; "+
				"/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f %s 2>/dev/null || true; "+
				"printf '%%s' %s | base64 -d > %s && : > %s; "+
				"uid=$(id -u); domain=''; "+
				"launchctl bootout \"gui/$uid/%s\" 2>/dev/null || true; "+
				"launchctl bootout \"user/$uid/%s\" 2>/dev/null || true; "+
				"sleep 0.3; "+
				"if launchctl bootstrap \"gui/$uid\" %s 2>/dev/null; then "+
				"  domain=\"gui/$uid\"; launchctl kickstart -k \"$domain/%s\" 2>/dev/null || true; "+
				"elif launchctl bootstrap \"user/$uid\" %s 2>/dev/null; then "+
				"  domain=\"user/$uid\"; launchctl kickstart -k \"$domain/%s\" 2>/dev/null || true; "+
				"else "+
				"  echo 'WARN: launchctl bootstrap failed; falling back to nohup'; "+
				"  nohup %s -id %s -name %s -server %s -workspace %s -data-dir %s%s > %s 2>&1 < /dev/null & "+
				"  newpid=$!; echo \"$newpid\" > %s; "+
				"fi; "+
				"sleep 1; "+
				"newpid=''; "+
				"if [ -n \"$domain\" ]; then newpid=$(launchctl print \"$domain/%s\" 2>/dev/null | awk '/pid =/{print $3; exit}'); fi; "+
				"if [ -z \"$newpid\" ]; then newpid=$(pgrep -f -- %s 2>/dev/null | head -n 1 || true); fi; "+
				"if [ -n \"$newpid\" ]; then echo \"$newpid\" > %s; echo \"STARTED_PID=$newpid (launchd=${domain:-nohup})\"; tail -n 15 %s 2>/dev/null || true; "+
				"else echo 'Runner failed to start:' >&2; tail -n 20 %s 2>/dev/null >&2 || true; exit 1; fi",
			shellQuote(pidFile), shellQuote(pidFile), shellQuote(pidFile),
			shellQuote("-id "+runnerID),
			shellQuote(appMacOSDir), shellQuote(launchAgentsDir), shellQuote(infoPlistB64), shellQuote(appInfoPlist), shellQuote(remoteBin), shellQuote(appBin), shellQuote(appBin),
			shellQuote(appBundleDir),
			shellQuote(appBundleDir),
			shellQuote(appBundleDir),
			shellQuote(plistB64), shellQuote(plistPath), shellQuote(logFile),
			label, label,
			shellQuote(plistPath), label,
			shellQuote(plistPath), label,
			shellQuote(appBin), shellQuote(runnerID), shellQuote(target.Name), shellQuote(wsURL), shellQuote(workspace), shellQuote(remoteDataDir), tokenFlag, shellQuote(logFile),
			shellQuote(pidFile),
			label,
			shellQuote("-id "+runnerID),
			shellQuote(pidFile), shellQuote(logFile), shellQuote(logFile),
		)

	default:
		effectiveSecret, _ := s.effectiveRunnerPairingSecret(ctx)
		tokenFlag := ""
		if effectiveSecret != "" {
			tokenFlag = " -token " + shellQuote(effectiveSecret)
		}
		startCmd = fmt.Sprintf(
			"if [ -f %s ]; then oldpid=$(cat %s); kill \"$oldpid\" 2>/dev/null || true; sleep 0.5; fi; "+
				"nohup %s -id %s -name %s -server %s -workspace %s -data-dir %s%s > %s 2>&1 < /dev/null & "+
				"newpid=$!; echo \"$newpid\" > %s; sleep 1; "+
				"if kill -0 \"$newpid\" 2>/dev/null; then echo \"STARTED_PID=$newpid\"; tail -n 10 %s 2>/dev/null || true; else echo 'Runner exited immediately:' >&2; tail -n 20 %s >&2; exit 1; fi",
			shellQuote(pidFile), shellQuote(pidFile),
			shellQuote(remoteBin), shellQuote(runnerID), shellQuote(target.Name), shellQuote(wsURL), shellQuote(workspace), shellQuote(remoteDataDir), tokenFlag, shellQuote(logFile),
			shellQuote(pidFile), shellQuote(logFile), shellQuote(logFile),
		)
	}

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

	// Step 5: Verify the remote runner actually registers online with this Server
	logf("Waiting for runner %q to register online...", runnerID)
	deadline := time.Now().Add(6 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		if s.isRunnerOnline(runnerID) {
			connected = true
			break
		}
		select {
		case <-ctx.Done():
			break
		case <-time.After(250 * time.Millisecond):
		}
	}

	if !connected {
		var tailCmd string
		if info.OS == "windows" {
			tailCmd = fmt.Sprintf(`powershell -NoProfile -NonInteractive -Command "Get-Content -Tail 30 '%s' -ErrorAction SilentlyContinue"`, strings.ReplaceAll(logFile, "'", "''"))
		} else {
			tailCmd = fmt.Sprintf("tail -n 30 %s 2>/dev/null || true", shellQuote(logFile))
		}
		remoteTail, _ := runSSHCommand(ctx, *target, tailCmd)
		if strings.TrimSpace(remoteTail) != "" {
			logf("Remote runner.log:\n%s", strings.TrimSpace(remoteTail))
		}
		errMsg := fmt.Sprintf("Runner started remotely, but did not connect to %s within 6s.", wsURL)
		if info.OS == "darwin" && strings.Contains(remoteTail, "no route to host") {
			errMsg += ` macOS Local Network privacy blocked outgoing LAN access to this server. Click Allow on the macOS prompt for "NexRunner" or enable NexRunner in System Settings -> Privacy & Security -> Local Network (nex-runner will auto-reconnect every 5s once allowed).`
		}
		logf("Verification failed: %s", errMsg)
		_ = s.store.UpdateSSHTargetStatus(r.Context(), id, "error", errMsg, logBuf.String(), 0)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        false,
			"runner_id": runnerID,
			"error":     errMsg,
			"log":       logBuf.String(),
		})
		return
	}

	logf("Deployment succeeded! Runner %q (%s) is online at %s.", target.Name, runnerID, wsURL)
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

	info, _, _ := probeRemoteHost(ctx, *target)
	installDir, _, _ := resolveRemotePaths(*target, info)
	runnerID := sshTargetRunnerID(target.Name)

	var stopCmd string
	switch info.OS {
	case "windows":
		pidFile := joinRemotePath(installDir, "runner.pid", "windows")
		stopCmd = fmt.Sprintf(
			`powershell -NoProfile -NonInteractive -Command "$pidf='%s'; if (Test-Path $pidf) { $p=Get-Content $pidf -ErrorAction SilentlyContinue; if ($p) { Stop-Process -Id ([int]$p) -Force -ErrorAction SilentlyContinue }; Remove-Item -Force $pidf; Write-Output 'Stopped runner.' } else { Write-Output 'No pid file found.' }"`,
			strings.ReplaceAll(pidFile, "'", "''"),
		)
	case "darwin":
		remoteHome := strings.TrimSpace(info.HomeDir)
		if remoteHome == "" {
			remoteHome = inferRemoteHomeDir("darwin", target.Username)
		}
		label := darwinLaunchAgentLabel(runnerID)
		plistPath := joinRemotePath(joinRemotePath(remoteHome, "Library/LaunchAgents", "darwin"), label+".plist", "darwin")
		pidFile := joinRemotePath(installDir, "runner.pid", "darwin")
		stopCmd = fmt.Sprintf(
			"uid=$(id -u); launchctl bootout \"gui/$uid/%s\" 2>/dev/null || true; launchctl bootout \"user/$uid/%s\" 2>/dev/null || true; rm -f %s; "+
				"pidf=%s; if [ -f \"$pidf\" ]; then kill $(cat \"$pidf\") 2>/dev/null || true; rm -f \"$pidf\"; fi; "+
				"pkill -f -- %s 2>/dev/null || true; echo 'Stopped runner.'",
			label, label, shellQuote(plistPath), shellQuote(pidFile), shellQuote("-id "+runnerID),
		)
	default:
		pidFile := joinRemotePath(installDir, "runner.pid", info.OS)
		stopCmd = fmt.Sprintf("pidf=%s; if [ -f \"$pidf\" ]; then kill $(cat \"$pidf\") 2>/dev/null || true; rm -f \"$pidf\"; echo 'Stopped runner.'; else echo 'No pid file found.'; fi", shellQuote(pidFile))
	}
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
