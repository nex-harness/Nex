// The install pipeline (docs/next/runner-install.md decision 9/10):
// connect → detect → layout → env → upload → pty assets → npm install →
// supervisor → start → wait for hello.
package installer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// Bundles points the installer at the per-platform runner bundles staged on
// the server (spec decision 3): bundleDir/<platform>/ with runner.mjs,
// pty-host.mjs, node-pty-assets.tar.gz, package.json.
type Bundles struct {
	Dir string
}

// remoteDirName is the install directory under the target user's home
// (spec decision 5); the absolute path is resolved per install (SFTP and
// supervisor units cannot use `~`).
const remoteDirName = ".nex-next-runner"

// targetPlatform is one detected target.
type targetPlatform struct {
	GOOS     string // linux | darwin | win32
	GOArch   string // x64 | arm64
	NodeOK   bool
	NodeCmd  string
	ShellBin string // sh/bash on posix, powershell on windows
}

var versionPattern = regexp.MustCompile(`v(\d+)\.(\d+)`)

// detect probes the target: OS, arch, and a usable Node ≥ 24 (decision 4:
// detect, not deploy).
func detect(c *sshClient) (targetPlatform, error) {
	out, err := c.run(`uname -s -m 2>/dev/null || ver`, nil)
	if err != nil {
		return targetPlatform{}, fmt.Errorf("detecting platform: %w", err)
	}
	platform := parsePlatform(out)
	// Node probe: `node --version`; nvm 管理的机器可能需要 login shell 才能找到
	// node。两种探测命中都记录 `node`（exec 上下文已由调用方的 supervisor 单元
	// 保证是 login shell 或 env PATH 已包含 node）。
	probes := []string{`node --version 2>/dev/null || echo missing`, `bash -lc 'node --version' 2>/dev/null || echo missing`}
	if platform.ShellBin == "powershell" {
		probes = []string{`node --version 2>$null; if (-not $?) { 'missing' }`}
	}
	for _, probe := range probes {
		out, err = c.run(probe, nil)
		if err != nil {
			continue
		}
		version := strings.TrimSpace(out)
		if version != "missing" && version != "" && versionPattern.MatchString(version) {
			platform.NodeOK = nodeMajorAtLeast24(version)
			platform.NodeCmd = "node"
			break
		}
	}
	return platform, nil
}

func parsePlatform(uname string) targetPlatform {
	platform := targetPlatform{ShellBin: "sh"}
	lower := strings.ToLower(uname)
	switch {
	case strings.Contains(lower, "mingw") || strings.Contains(lower, "windows"):
		platform.GOOS = "win32"
		platform.ShellBin = "powershell"
	case strings.Contains(lower, "darwin"):
		platform.GOOS = "darwin"
	case strings.Contains(lower, "linux"):
		platform.GOOS = "linux"
	default:
		platform.GOOS = "linux"
	}
	switch {
	case strings.Contains(lower, "arm64") || strings.Contains(lower, "aarch64"):
		platform.GOArch = "arm64"
	case strings.Contains(lower, "x86_64") || strings.Contains(lower, "amd64"):
		platform.GOArch = "x64"
	default:
		platform.GOArch = "x64"
	}
	return platform
}

func nodeMajorAtLeast24(version string) bool {
	match := versionPattern.FindStringSubmatch(version)
	if match == nil {
		return false
	}
	var major int
	if _, err := fmt.Sscanf(match[1], "%d", &major); err != nil {
		return false
	}
	return major >= 24
}

// bundlePlatform maps the detected target to the bundle directory name
// (matches the pty prebuild platform naming).
func (p targetPlatform) bundlePlatform() string {
	if p.GOOS == "win32" {
		return "win32-" + p.GOArch
	}
	if p.GOOS == "darwin" && p.GOArch == "arm64" {
		return "darwin-arm64"
	}
	if p.GOOS == "darwin" {
		return "darwin-x64"
	}
	if p.GOArch == "arm64" {
		return "linux-arm64"
	}
	return "linux-x64"
}

// installRequest is one validated install job.
type installRequest struct {
	Target    SSHTarget
	RunnerID  string
	RunnerTok string
	// ServerURL the runner dials back (wss://.../runner).
	ServerURL string
	// Name is the display name (defaults to host).
	Name string
}

// generateRunnerID makes the pre-generated id the installer waits for on the
// registry (spec decision 9: the wait is exact because the id is fixed
// before start).
func generateRunnerID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return "runner_" + hex.EncodeToString(raw)
}

// install runs the whole pipeline against one session. Every step logs.
func (s *Session) install(req installRequest, bundles Bundles, waitForRunner func(runnerID string) bool) error {
	target := req.Target
	s.Log(LogInfo, "connecting to %s", target)
	client, err := dialSSH(target)
	if err != nil {
		s.Log(LogError, "ssh connect failed: %v", err)
		return err
	}
	defer client.close()
	s.Log(LogInfo, "connected; detecting environment")
	platform, err := detect(client)
	if err != nil {
		return err
	}
	s.Log(LogInfo, "target: %s %s (bundle %s)", platform.GOOS, platform.GOArch, platform.bundlePlatform())
	if !platform.NodeOK {
		s.Log(LogError, "Node.js >= 24 is required on the target (found: %s); install Node and retry", platform.NodeCmd)
		return fmt.Errorf("node >= 24 not found on %s", target)
	}

	// Layout + env (decision 5): idempotent mkdir; env chmod 600.
	s.Log(LogInfo, "preparing ~/.nex-next-runner layout")
	// SFTP does not expand `~`: resolve the absolute home path first (the
	// remote shell does).
	homeOut, err := client.run(`sh -c 'printf %s "$HOME"'`, nil)
	if err != nil {
		return fmt.Errorf("resolving home: %w", err)
	}
	home := strings.TrimSpace(homeOut)
	if home == "" {
		return fmt.Errorf("remote home directory is empty")
	}
	remoteBase := home + "/" + remoteDirName
	remoteEnvFile := remoteBase + "/env"
	if _, err := client.run(fmt.Sprintf("mkdir -p %s/bundles %s/lib", remoteBase, remoteBase), nil); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	name := req.Name
	if name == "" {
		name = target.Host
	}
	// The runner requires at least one served workspace. At install time the
	// user has not chosen a directory yet, so the home directory itself is the
	// initial workspace (the machine picker can browse it; narrower roots can
	// be added by editing the env file — dynamic workspace discovery is
	// follow-up work).
	envScript := fmt.Sprintf("NEX_SERVER_URL=%s\nNEX_RUNNER_TOKEN=%s\nNEX_RUNNER_ID=%s\nNEX_RUNNER_NAME=%s\nNEX_RUNNER_WORKSPACES=%s\n",
		req.ServerURL, req.RunnerTok, req.RunnerID, name, home)
	if _, err := client.run(writeRemoteFileCommand(remoteEnvFile, envScript, platform), nil); err != nil {
		return fmt.Errorf("writing env: %w", err)
	}
	if platform.GOOS != "win32" {
		if _, err := client.run(fmt.Sprintf("chmod 600 %s", remoteEnvFile), nil); err != nil {
			return fmt.Errorf("chmod env: %w", err)
		}
	}
	s.Log(LogInfo, "env written (runner id %s)", req.RunnerID)

	// Upload the bundle (decision 3). `lib/ptyHostAssets.mjs` is the runtime
	// extractor loaded by pty-host via --import; it unpacks the tarball next
	// to node_modules at first start (no manual extraction step needed).
	bundleDir := path.Join(bundles.Dir, platform.bundlePlatform())
	s.Log(LogInfo, "uploading runner bundle from %s", bundleDir)
	uploadFiles := []struct{ local, remote string }{
		{bundleDir + "/runner.mjs", remoteBase + "/runner.mjs"},
		{bundleDir + "/pty-host.mjs", remoteBase + "/pty-host.mjs"},
		{bundleDir + "/package.json", remoteBase + "/package.json"},
		{bundleDir + "/node-pty-assets.tar.gz", remoteBase + "/node-pty-assets.tar.gz"},
		{bundleDir + "/lib/ptyHostAssets.mjs", remoteBase + "/lib/ptyHostAssets.mjs"},
	}
	for _, file := range uploadFiles {
		if err := client.upload(file.local, file.remote); err != nil {
			s.Log(LogError, "upload %s failed: %v", file.local, err)
			return err
		}
	}
	s.Log(LogInfo, "bundle uploaded")

	// node_modules: runner.mjs keeps @earendil-works/* external with a pinned
	// dist/package.json; `npm install --omit=dev` fetches them on the target.
	// --ignore-scripts: node-pty's install script would node-gyp rebuild (the
	// bundle ships the prebuild instead; targets may lack make/g++).
	s.Log(LogInfo, "installing dependencies (npm install)")
	npmCommand := fmt.Sprintf("cd %s && npm install --omit=dev --no-audit --no-fund --ignore-scripts", remoteBase)
	if _, err := client.runWithOutput(npmCommand, s); err != nil {
		s.Log(LogError, "npm install failed: %v", err)
		return err
	}
	s.Log(LogInfo, "dependencies installed")

	// Unpack the pty prebuild into node_modules/node-pty/prebuilds (<platform>/)
	// (the runtime loader lib/ptyHostAssets.mjs does this on first start; doing
	// it here too makes the very first start deterministic).
	s.Log(LogInfo, "placing node-pty prebuild assets")
	extract := fmt.Sprintf("cd %s && tar -xzf node-pty-assets.tar.gz -C node_modules/node-pty/prebuilds/%s 2>/dev/null || (mkdir -p node_modules/node-pty/prebuilds/%s && tar -xzf node-pty-assets.tar.gz -C node_modules/node-pty/prebuilds/%s)", remoteBase, platform.bundlePlatform(), platform.bundlePlatform(), platform.bundlePlatform())
	if platform.GOOS == "darwin" {
		extract += " && chmod +x node_modules/node-pty/prebuilds/darwin-*/spawn-helper 2>/dev/null || true"
	}
	if _, err := client.run(extract, nil); err != nil {
		s.Log(LogWarn, "pty asset placement warning: %v", err)
	}

	// Supervisor + start (decision 6): rewrite paths absolute (units must not
	// rely on `~` either).
	s.Log(LogInfo, "starting runner")
	startCommand := buildStartCommand(platform, req, home)
	if _, err := client.run(startCommand, nil); err != nil {
		s.Log(LogError, "start failed: %v", err)
		return err
	}

	// Wait for hello (decision 9): exact runnerId wait, ≤ 60 s.
	s.Log(LogInfo, "waiting for the runner to register (id %s)", req.RunnerID)
	if !waitForRunner(req.RunnerID) {
		s.Log(LogError, "runner did not register within 60s; check the target's logs")
		return fmt.Errorf("runner %s did not connect back", req.RunnerID)
	}
	s.Log(LogInfo, "runner registered; install complete")
	return nil
}

// writeRemoteFileCommand builds a heredoc-free file write. Values are
// controlled (server URL/token/id/name); the name comes from user input and
// is single-line sanitized (no quotes/newlines) by validation upstream.
func writeRemoteFileCommand(remotePath, content string, platform targetPlatform) string {
	if platform.GOOS == "win32" {
		// PowerShell here-string; content is validated single-line.
		return fmt.Sprintf(`powershell -Command "Set-Content -Path '%s' -Value '%s'"`, remotePath, content)
	}
	// POSIX: printf %b interprets the \n escapes. %s would write one literal
	// line — the env file could not be sourced line-by-line and unit files
	// were rejected by systemd ("Assignment outside of section").
	escaped := strings.ReplaceAll(content, "\n", "\\n")
	return fmt.Sprintf("printf '%%b' '%s' > %s", escaped, remotePath)
}

// buildStartCommand assembles the platform supervisor command (decision 6).
// `home` is the target's absolute home path (SFTP/unit files cannot use `~`).
func buildStartCommand(platform targetPlatform, req installRequest, home string) string {
	nodeCmd := platform.NodeCmd
	base := home + "/.nex-next-runner"
	runLine := fmt.Sprintf("cd %s && set -a && . ./env && set +a && exec %s runner.mjs", base, nodeCmd)
	switch platform.GOOS {
	case "darwin":
		// LaunchAgent: load-once, restarts on login. The plist is written
		// by the installer; the command only needs to (re)load it.
		return fmt.Sprintf("%s && (launchctl unload %s/Library/LaunchAgents/cn.nex.runner.plist 2>/dev/null || true) && launchctl load %s/Library/LaunchAgents/cn.nex.runner.plist",
			writeLaunchdPlist(platform, home), home, home)
	case "win32":
		return fmt.Sprintf(`schtasks /create /f /tn NexRunner /tr "%s %s runner.mjs" /sc onlogon`, nodeCmd, base)
	default:
		// Linux：优先 systemd user unit（需要 XDG_RUNTIME_DIR，非交互 SSH
		// 没有它，这里显式导出）；linger 通过 sudo -n 尽力开启。任何失败都
		// 回落 setsid+nohup 后台启动（容器/无 systemd 主机）：stdin 显式
		// 挂 /dev/null、stdout/stderr 全部重定向到文件，否则 SSH 会话关闭
		// 时后台进程会随会话管道一起死掉。整条命令以 `true` 收尾：启动是
		// 尽力而为，真正的验收是后面的 wait-for-hello。
		return fmt.Sprintf("%s && { export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}; (systemctl --user daemon-reload && systemctl --user enable nex-runner.service && systemctl --user restart nex-runner.service) || (sudo -n loginctl enable-linger %s 2>/dev/null && systemctl --user daemon-reload && systemctl --user enable nex-runner.service && systemctl --user restart nex-runner.service) || (setsid nohup sh -c '%s' <%s/null >%s/runner.log 2>&1 &); } ; true",
			writeSystemdUnit(base), sanitizeShellArg(req.Target.Username), runLine, base, base)
	}
}

// writeSystemdUnit emits the user unit file write command. printf %b
// interprets the \n escapes (%s would write one literal line and systemd
// rejects the unit with "Assignment outside of section"). The unit body
// contains single quotes (ExecStart's shell command); they are escaped as
// '\'' so the printf argument's own single-quoting stays intact.
func writeSystemdUnit(base string) string {
	unit := fmt.Sprintf("[Unit]\nDescription=Nex agent runner\nAfter=network-online.target\n\n[Service]\nType=simple\nExecStart=/bin/sh -c 'cd %s && set -a && . ./env && set +a && exec node runner.mjs'\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n", base)
	shellSafe := sanitizeShellArg(unit)
	return fmt.Sprintf("mkdir -p ${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user && printf '%%b' %s > ${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/nex-runner.service", shellSafe)
}

// writeLaunchdPlist emits the LaunchAgent write command (printf %b + the
// sanitizeShellArg quoting, same reasons as writeSystemdUnit).
func writeLaunchdPlist(platform targetPlatform, home string) string {
	base := home + "/.nex-next-runner"
	plist := fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\">\n<dict>\n<key>Label</key><string>cn.nex.runner</string>\n<key>ProgramArguments</key>\n<array>\n<string>/bin/sh</string>\n<string>-c</string>\n<string>cd %s && set -a && . ./env && set +a && exec node runner.mjs</string>\n</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><true/>\n</dict>\n</plist>\n", base)
	shellSafe := sanitizeShellArg(plist)
	return fmt.Sprintf("mkdir -p %s/Library/LaunchAgents && printf '%%b' %s > %s/Library/LaunchAgents/cn.nex.runner.plist", home, shellSafe, home)
}

// sanitizeShellArg single-quotes a value for shell interpolation.
func sanitizeShellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// runWithOutput streams command output into the session log (truncated).
func (c *sshClient) runWithOutput(command string, s *Session) (string, error) {
	return c.run(command, func(chunk string) {
		for _, line := range strings.Split(chunk, "\n") {
			line = strings.TrimRight(line, "\r")
			if line != "" {
				s.Log(LogInfo, "  %s", truncate(line, 200))
			}
		}
	})
}

func truncate(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}

// WaitForRunner builds the registry wait closure (exact id, 60 s).
func WaitForRunner(lookup func(runnerID string) bool) func(string) bool {
	return func(runnerID string) bool {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if lookup(runnerID) {
				return true
			}
			time.Sleep(500 * time.Millisecond)
		}
		return false
	}
}
