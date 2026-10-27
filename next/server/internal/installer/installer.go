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

// remoteLayout is the target-side install layout (spec decision 5).
const (
	remoteBase     = "~/.nex-next-runner"
	remoteEnvFile  = "~/.nex-next-runner/env"
	remoteBundles  = "~/.nex-next-runner/bundles"
	remoteNodeMods = "~/.nex-next-runner/node_modules"
)

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
	// Node probe: `node --version` (nvm-managed shells may need a login
	// shell to find node; try both plain and `bash -lc`).
	for _, probe := range []string{`node --version 2>/dev/null || echo missing`, `bash -lc 'node --version' 2>/dev/null || echo missing`} {
		if platform.ShellBin == "powershell" {
			probe = `node --version 2>$null; if (-not $?) { 'missing' }`
		}
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
		// A login shell may be needed (nvm); record a wrapper later.
		if strings.Contains(probe, "bash -lc") {
			platform.NodeOK = nodeMajorAtLeast24(version)
			platform.NodeCmd = "bash -lc node"
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
	if _, err := client.run(fmt.Sprintf("mkdir -p %s", remoteBundles), nil); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	name := req.Name
	if name == "" {
		name = target.Host
	}
	envScript := fmt.Sprintf("NEX_SERVER_URL=%s\nNEX_RUNNER_TOKEN=%s\nNEX_RUNNER_ID=%s\nNEX_RUNNER_NAME=%s\n",
		req.ServerURL, req.RunnerTok, req.RunnerID, name)
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
	// dist/package.json; `npm install --omit=dev` fetches them on the target
	// (requires network to the npm registry).
	s.Log(LogInfo, "installing dependencies (npm install)")
	npmCommand := fmt.Sprintf("cd %s && npm install --omit=dev --no-audit --no-fund", remoteBase)
	if _, err := client.runWithOutput(npmCommand, s); err != nil {
		s.Log(LogError, "npm install failed: %v", err)
		return err
	}
	s.Log(LogInfo, "dependencies installed")

	// Supervisor + start (decision 6).
	s.Log(LogInfo, "starting runner")
	startCommand := buildStartCommand(platform, req)
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
	// POSIX: printf with escaped newlines (no heredoc, no echo -e variance).
	escaped := strings.ReplaceAll(content, "\n", "\\n")
	return fmt.Sprintf("printf '%s' > %s", escaped, remotePath)
}

// buildStartCommand assembles the platform supervisor command (decision 6).
func buildStartCommand(platform targetPlatform, req installRequest) string {
	nodeCmd := platform.NodeCmd
	runLine := fmt.Sprintf("cd %s && set -a && . ./env && set +a && exec %s runner.mjs", remoteBase, nodeCmd)
	switch platform.GOOS {
	case "darwin":
		// LaunchAgent: load-once, restarts on login. The plist is written
		// by the installer; the command only needs to (re)load it.
		return fmt.Sprintf("%s && launchctl unload ~/Library/LaunchAgents/cn.nex.runner.plist 2>/dev/null; launchctl load ~/Library/LaunchAgents/cn.nex.runner.plist", writeLaunchdPlist(platform, req))
	case "win32":
		return fmt.Sprintf(`schtasks /create /f /tn NexRunner /tr "%s %s runner.mjs" /sc onlogon`, nodeCmd, remoteBase)
	default:
		// Linux: systemd user unit with linger; setsid fallback for
		// container hosts without systemd.
		return fmt.Sprintf("%s && (systemctl --user daemon-reload && systemctl --user enable --now nex-runner.service || (loginctl enable-linger %s 2>/dev/null; systemctl --user daemon-reload && systemctl --user enable --now nex-runner.service) || (setsid sh -c '%s' >/dev/null 2>&1 &))",
			writeSystemdUnit(), sanitizeShellArg(req.Target.Username), runLine)
	}
}

// writeSystemdUnit emits the user unit file write command.
func writeSystemdUnit() string {
	unit := "[Unit]\nDescription=Nex agent runner\nAfter=network-online.target\n\n[Service]\nType=simple\nExecStart=/bin/sh -lc 'cd ~/.nex-next-runner && set -a && . ./env && set +a && exec node runner.mjs'\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n"
	escaped := strings.ReplaceAll(unit, "\n", "\\n")
	return fmt.Sprintf("mkdir -p ~/.config/systemd/user && printf '%%s' '%s' > ~/.config/systemd/user/nex-runner.service", escaped)
}

// writeLaunchdPlist emits the LaunchAgent write + load command.
func writeLaunchdPlist(platform targetPlatform, req installRequest) string {
	plist := fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\">\n<dict>\n<key>Label</key><string>cn.nex.runner</string>\n<key>ProgramArguments</key>\n<array>\n<string>/bin/sh</string>\n<string>-lc</string>\n<string>cd ~/.nex-next-runner && set -a && . ./env && set +a && exec node runner.mjs</string>\n</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><true/>\n</dict>\n</plist>\n")
	escaped := strings.ReplaceAll(plist, "\n", "\\n")
	return fmt.Sprintf("mkdir -p ~/Library/LaunchAgents && printf '%%s' '%s' > ~/Library/LaunchAgents/cn.nex.runner.plist", escaped)
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
