// The install pipeline (docs/next/runner-install.md decision 9/10):
// connect → detect → layout → env → upload → pty assets → npm install →
// supervisor → start → wait for hello.
package installer

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"
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
	ShellBin string // sh/bash on posix, powershell on windows
}

// detect probes the target: OS and arch. Node.js is NOT required — the
// bundle ships a private Node distribution (decision 4).
func detect(c *sshClient) (targetPlatform, error) {
	out, err := c.run(`uname -s -m 2>/dev/null || ver`, nil)
	if err != nil {
		return targetPlatform{}, fmt.Errorf("detecting platform: %w", err)
	}
	return parsePlatform(out), nil
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

// supervisorSystemd / supervisorLaunchd / supervisorLoop are the values written
// into NEX_RUNNER_SUPERVISOR (docs/next/runner-update.md decision 5). The runner
// reads it to decide whether an exit(75) is restarted by an external supervisor
// or must be handled by spawning a detached copy of itself.
const (
	supervisorSystemd = "systemd"
	supervisorLaunchd = "launchd"
	supervisorLoop    = "loop"
)

// supervisorName maps a detected target to the restart owner written into the
// env file. Linux prefers the systemd user unit and only falls back to the
// wrapper loop when the target has no systemd (containers).
func supervisorName(platform targetPlatform, systemdAvailable bool) string {
	switch platform.GOOS {
	case "win32":
		// schtasks runs start-runner.cmd, which owns the loop.
		return supervisorLoop
	case "darwin":
		return supervisorLaunchd
	default:
		if systemdAvailable {
			return supervisorSystemd
		}
		return supervisorLoop
	}
}

// detectSystemd probes the target for a usable systemd user manager. A
// non-systemd host (container, WSL without systemd) gets the wrapper loop
// instead, and must report that in NEX_RUNNER_SUPERVISOR so the runner does not
// exit 75 expecting an external restart that never comes.
func detectSystemd(c *sshClient) bool {
	out, err := c.run(`[ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1 && echo yes || echo no`, nil)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "yes"
}

// buildEnvFile renders the runner env file. Values are controlled (server
// URL/token/id/name); the name is single-line sanitized upstream.
func buildEnvFile(req installRequest, name, workspacePath, supervisor string) string {
	return fmt.Sprintf("NEX_SERVER_URL=%s\nNEX_RUNNER_TOKEN=%s\nNEX_RUNNER_ID=%s\nNEX_RUNNER_NAME=%s\nNEX_RUNNER_WORKSPACES=%s\nNEX_RUNNER_SUPERVISOR=%s\n",
		req.ServerURL, req.RunnerTok, req.RunnerID, name, workspacePath, supervisor)
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

	// Layout + env (decision 5): idempotent mkdir; env chmod 600 (posix).
	s.Log(LogInfo, "preparing install layout")
	// SFTP does not expand `~`: resolve the absolute home path first. POSIX
	// via $HOME; Windows cmd via %USERPROFILE% (the SFTP root needs forward
	// slashes, so the home is normalized to C:/Users/x form).
	var home string
	if platform.GOOS == "win32" {
		homeOut, err := client.run(`echo %USERPROFILE%`, nil)
		if err != nil {
			return fmt.Errorf("resolving home: %w", err)
		}
		home = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(homeOut, "\r", ""), "\n", ""))
		home = strings.ReplaceAll(home, `\`, "/")
	} else {
		homeOut, err := client.run(`sh -c 'printf %s "$HOME"'`, nil)
		if err != nil {
			return fmt.Errorf("resolving home: %w", err)
		}
		home = strings.TrimSpace(homeOut)
	}
	if home == "" {
		return fmt.Errorf("remote home directory is empty")
	}
	remoteBase := home + "/" + remoteDirName
	remoteEnvFile := remoteBase + "/env"
	if platform.GOOS == "win32" {
		// cmd mkdir is idempotent-failing; per-dir mkdir keeps going.
		for _, dir := range []string{"", "/lib", "/node", "/bundles"} {
			if _, err := client.run(fmt.Sprintf(`mkdir "%s%s" 2>nul & echo OK`, remoteBase, dir), nil); err != nil {
				return fmt.Errorf("mkdir: %w", err)
			}
		}
	} else {
		if _, err := client.run(fmt.Sprintf("mkdir -p %s/bundles %s/lib %s/node", remoteBase, remoteBase, remoteBase), nil); err != nil {
			return fmt.Errorf("mkdir: %w", err)
		}
	}
	name := req.Name
	if name == "" {
		name = target.Host
	}
	// The runner requires at least one served workspace. At install time the
	// user has not chosen a directory yet, so the home directory itself is the
	// initial workspace (the machine picker can browse it; narrower roots can
	// be added by editing the env file — dynamic workspace discovery is
	// follow-up work). The runner resolves paths itself; keep the OS-native
	// separator in the env value.
	workspacePath := home
	if platform.GOOS == "win32" {
		workspacePath = strings.ReplaceAll(home, "/", `\`)
	}
	// 重启归属（docs/next/runner-update.md 决策 5）：systemd/launchd 由外部
	// supervisor 拉起 exit 75；Windows 与 setsid 回退没有 supervisor，改由安装
	// 脚本里的循环负责。runner 读到该值后才知道要不要自己 spawn 分离副本。
	systemdAvailable := false
	if platform.GOOS == "linux" {
		systemdAvailable = detectSystemd(client)
		if !systemdAvailable {
			s.Log(LogWarn, "systemd user manager not detected; using the wrapper loop")
		}
	}
	supervisor := supervisorName(platform, systemdAvailable)
	envScript := buildEnvFile(req, name, workspacePath, supervisor)
	if _, err := client.run(writeRemoteFileCommand(remoteEnvFile, envScript, platform), nil); err != nil {
		return fmt.Errorf("writing env: %w", err)
	}
	if platform.GOOS != "win32" {
		if _, err := client.run(fmt.Sprintf("chmod 600 %s", remoteEnvFile), nil); err != nil {
			return fmt.Errorf("chmod env: %w", err)
		}
	}
	s.Log(LogInfo, "env written (runner id %s)", req.RunnerID)

	// Stop a previous installation before uploading: Windows locks running
	// executables (SFTP over node.exe fails with SSH_FX_FAILURE), and posix
	// restarts are cleaner from a stopped state.
	s.Log(LogInfo, "stopping any previous runner")
	if platform.GOOS == "win32" {
		// End the scheduled task, then kill by image name: the runner is the
		// only node.exe a fresh install box runs (WQL path filtering is
		// quoting-fragile through the cmd+ssh layers).
		if _, err := client.run(`schtasks /end /tn NexRunner 2>nul & taskkill /f /im node.exe 2>nul & echo OK`, nil); err != nil {
			s.Log(LogWarn, "stop previous runner warning: %v", err)
		}
	} else {
		if _, err := client.run(fmt.Sprintf("systemctl --user stop nex-runner.service 2>/dev/null; pkill -f '%s/runner.mjs' 2>/dev/null; true", remoteBase), nil); err != nil {
			s.Log(LogWarn, "stop previous runner warning: %v", err)
		}
	}

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

	// Private Node (decision 4): the bundle ships the official Node
	// distribution, so the target needs no Node.js installed. All later
	// commands (npm install, the runner, the pty host) run through it.
	s.Log(LogInfo, "uploading bundled Node.js (~100MB, first install only)")
	if err := client.uploadDir(bundleDir+"/node", remoteBase+"/node"); err != nil {
		s.Log(LogError, "node upload failed: %v", err)
		return err
	}
	if platform.GOOS != "win32" {
		if _, err := client.run(fmt.Sprintf("chmod +x %s/node/node", remoteBase), nil); err != nil {
			return fmt.Errorf("chmod node: %w", err)
		}
	}
	s.Log(LogInfo, "bundled Node installed")
	nodeBinary := remoteBase + "/node/node"
	npmCli := remoteBase + "/node/node_modules/npm/bin/npm-cli.js"
	if platform.GOOS == "win32" {
		nodeBinary = remoteBase + `/node/node.exe`
		npmCli = remoteBase + `/node/node_modules/npm/bin/npm-cli.js`
	}

	// node_modules: runner.mjs keeps @earendil-works/* external with a pinned
	// dist/package.json; `npm install --omit=dev` fetches them on the target
	// with the bundled Node/npm.
	// --ignore-scripts: node-pty's install script would node-gyp rebuild (the
	// bundle ships the prebuild instead; targets may lack make/g++).
	s.Log(LogInfo, "installing dependencies (npm install via bundled Node)")
	var npmCommand string
	if platform.GOOS == "win32" {
		npmCommand = fmt.Sprintf(`cd /d "%s" && "%s" "%s" install --omit=dev --no-audit --no-fund --ignore-scripts`, strings.ReplaceAll(remoteBase, "/", `\`), strings.ReplaceAll(nodeBinary, "/", `\`), strings.ReplaceAll(npmCli, "/", `\`))
	} else {
		npmCommand = fmt.Sprintf("cd %s && %s %s install --omit=dev --no-audit --no-fund --ignore-scripts", remoteBase, nodeBinary, npmCli)
	}
	if _, err := client.runWithOutput(npmCommand, s); err != nil {
		s.Log(LogError, "npm install failed: %v", err)
		return err
	}
	s.Log(LogInfo, "dependencies installed")

	// Unpack the pty prebuild into node_modules/node-pty/prebuilds (<platform>/)
	// (the runtime loader lib/ptyHostAssets.mjs does this on first start; doing
	// it here too makes the very first start deterministic).
	s.Log(LogInfo, "placing node-pty prebuild assets")
	var extract string
	if platform.GOOS == "win32" {
		winBase := strings.ReplaceAll(remoteBase, "/", `\`)
		extract = fmt.Sprintf(`cd /d "%s" && tar -xzf node-pty-assets.tar.gz -C node_modules\node-pty\prebuilds\%s`, winBase, platform.bundlePlatform())
	} else {
		extract = fmt.Sprintf("cd %s && tar -xzf node-pty-assets.tar.gz -C node_modules/node-pty/prebuilds/%s 2>/dev/null || (mkdir -p node_modules/node-pty/prebuilds/%s && tar -xzf node-pty-assets.tar.gz -C node_modules/node-pty/prebuilds/%s)", remoteBase, platform.bundlePlatform(), platform.bundlePlatform(), platform.bundlePlatform())
		if platform.GOOS == "darwin" {
			extract += " && chmod +x node_modules/node-pty/prebuilds/darwin-*/spawn-helper 2>/dev/null || true"
		}
	}
	if _, err := client.run(extract, nil); err != nil {
		s.Log(LogWarn, "pty asset placement warning: %v", err)
	}

	// Supervisor + start (decision 6): rewrite paths absolute (units must not
	// rely on `~` either).
	s.Log(LogInfo, "starting runner (supervisor: %s)", supervisor)
	startCommand := buildStartCommand(platform, req, home, supervisor)
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
		// Windows: base64 + PowerShell WriteAllBytes — cmd quoting of
		// multi-line content is unreliable, and `type env` (the wrapper's
		// source loop) needs real CRLF lines.
		b64 := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(content, "\n", "\r\n")))
		winPath := strings.ReplaceAll(remotePath, "/", `\`)
		return fmt.Sprintf(`powershell -Command "[IO.File]::WriteAllBytes('%s', [Convert]::FromBase64String('%s'))"`, winPath, b64)
	}
	// POSIX: printf %b interprets the \n escapes. %s would write one literal
	// line — the env file could not be sourced line-by-line and unit files
	// were rejected by systemd ("Assignment outside of section").
	escaped := strings.ReplaceAll(content, "\n", "\\n")
	return fmt.Sprintf("printf '%%b' '%s' > %s", escaped, remotePath)
}

// buildStartCommand assembles the platform supervisor command (decision 6).
// `home` is the target's absolute home path (SFTP/unit files cannot use `~`).
// All commands run through the bundled private Node (decision 4), not any
// system Node. supervisor selects the restart owner written into the env file:
// systemd and launchd restart the process themselves, so the wrapper loop only
// exists for win32 and the setsid fallback.
func buildStartCommand(platform targetPlatform, req installRequest, home, supervisor string) string {
	base := home + "/.nex-next-runner"
	nodeBinary := base + "/node/node"
	if platform.GOOS == "win32" {
		nodeBinary = base + "/node/node.exe"
		winBase := strings.ReplaceAll(base, "/", `\`)
		// Windows: a wrapper .cmd reads the env file into the process env
		// (cmd cannot source a file inline), then starts the bundled node.
		// schtasks runs it at logon; a /run fires it immediately.
		//
		// 循环（docs/next/runner-update.md 决策 5）：self-update 以 exit 75 请求
		// 重启，包装脚本只对 75 重新拉起，其他退出码（崩溃、人工停止）直接结束，
		// 否则崩溃会无限重启。
		wrapper := fmt.Sprintf("@echo off\r\ncd /d \"%s\"\r\nfor /f \"usebackq delims=\" %%%%i in (`type env`) do set %%%%i\r\n:start\r\n\"%s\" runner.mjs\r\nif %%errorlevel%%==75 goto start\r\nexit /b %%errorlevel%%\r\n", winBase, strings.ReplaceAll(nodeBinary, "/", `\`))
		// The content has CR LF and % escapes; write it as a base64-decoded blob
		// instead of fighting cmd quoting.
		wrapperB64 := base64.StdEncoding.EncodeToString([]byte(wrapper))
		writeWrapper := fmt.Sprintf(`powershell -Command "[IO.File]::WriteAllBytes('%s/start-runner.cmd', [Convert]::FromBase64String('%s'))"`, winBase, wrapperB64)
		return fmt.Sprintf(`%s && (schtasks /delete /f /tn NexRunner 2>nul & schtasks /create /f /tn NexRunner /tr "%s/start-runner.cmd" /sc onlogon && schtasks /run /tn NexRunner)`, writeWrapper, winBase)
	}
	switch platform.GOOS {
	case "darwin":
		// LaunchAgent: load-once, restarts on login. The plist is written
		// by the installer; the command only needs to (re)load it.
		return fmt.Sprintf("%s && (launchctl unload %s/Library/LaunchAgents/cn.nex.runner.plist 2>/dev/null || true) && launchctl load %s/Library/LaunchAgents/cn.nex.runner.plist",
			writeLaunchdPlist(platform, home, nodeBinary), home, home)
	default:
		// Linux：优先 systemd user unit（需要 XDG_RUNTIME_DIR，非交互 SSH
		// 没有它，这里显式导出）；linger 通过 sudo -n 尽力开启。任何失败都
		// 回落 setsid+nohup 后台启动（容器/无 systemd 主机）：stdin 显式
		// 挂 /dev/null、stdout/stderr 全部重定向到文件，否则 SSH 会话关闭
		// 时后台进程会随会话管道一起死掉。整条命令以 `true` 收尾：启动是
		// 尽力而为，真正的验收是后面的 wait-for-hello。
		//
		// 回退路径的循环（docs/next/runner-update.md 决策 5）：setsid 没有
		// supervisor，自更新以 exit 75 请求重启，只对 75 重新拉起，其他退出码
		// 结束循环（崩溃不无限重启）。循环体不能带 exec——exec 会替换掉 shell，
		// 之后就无法回到循环里；systemd 的 ExecStart 才用 exec。
		loopLine := fmt.Sprintf("while :; do cd %s && set -a && . ./env && set +a && %s runner.mjs; [ $? -eq 75 ] || break; done", base, nodeBinary)
		return fmt.Sprintf("%s && { export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}; (systemctl --user daemon-reload && systemctl --user enable nex-runner.service && systemctl --user restart nex-runner.service) || (sudo -n loginctl enable-linger %s 2>/dev/null && systemctl --user daemon-reload && systemctl --user enable nex-runner.service && systemctl --user restart nex-runner.service) || (setsid nohup sh -c '%s' <%s/null >%s/runner.log 2>&1 &); } ; true",
			writeSystemdUnit(base, nodeBinary), sanitizeShellArg(req.Target.Username), loopLine, base, base)
	}
}

// writeSystemdUnit emits the user unit file write command. printf %b
// interprets the \n escapes (%s would write one literal line and systemd
// rejects the unit with "Assignment outside of section"). The unit body
// contains single quotes (ExecStart's shell command); each one is escaped with
// a backslash so the printf argument's own single-quoting stays intact.
func writeSystemdUnit(base, nodeBinary string) string {
	unit := fmt.Sprintf("[Unit]\nDescription=Nex agent runner\nAfter=network-online.target\n\n[Service]\nType=simple\nExecStart=/bin/sh -c 'cd %s && set -a && . ./env && set +a && exec %s runner.mjs'\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n", base, nodeBinary)
	shellSafe := sanitizeShellArg(unit)
	return fmt.Sprintf("mkdir -p ${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user && printf '%%b' %s > ${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/nex-runner.service", shellSafe)
}

// writeLaunchdPlist emits the LaunchAgent write command (printf %b + the
// sanitizeShellArg quoting, same reasons as writeSystemdUnit).
func writeLaunchdPlist(platform targetPlatform, home, nodeBinary string) string {
	base := home + "/.nex-next-runner"
	plist := fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\">\n<dict>\n<key>Label</key><string>cn.nex.runner</string>\n<key>ProgramArguments</key>\n<array>\n<string>/bin/sh</string>\n<string>-c</string>\n<string>cd %s && set -a && . ./env && set +a && exec %s runner.mjs</string>\n</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><true/>\n</dict>\n</plist>\n", base, nodeBinary)
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
