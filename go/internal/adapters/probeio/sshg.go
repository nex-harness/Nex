package probeio

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// SSHGTimeout is SSH_G_TIMEOUT_MS.
const SSHGTimeout = 1500 * time.Millisecond

// SSHGMaxOutput is the stdout cap (bytes).
const SSHGMaxOutput = 128_000

// RunSSHConfigQuery runs `ssh -G -F config -o BatchMode=yes alias` with the
// askpass-disabled environment, a 1.5s timeout, and the 128KB stdout cap —
// the Go shape of runSshConfigQuery. Any failure returns ("", false).
func RunSSHConfigQuery(ctx context.Context, sshExecutablePath, configPath, alias string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, SSHGTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sshExecutablePath, "-G", "-F", configPath, "-o", "BatchMode=yes", alias)
	cmd.Env = append(os.Environ(), "SSH_ASKPASS_REQUIRE=never", "SSH_ASKPASS=", "DISPLAY=")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	if len(out) > SSHGMaxOutput {
		out = out[:SSHGMaxOutput]
	}
	return string(out), true
}
