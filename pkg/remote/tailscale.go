package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// SystemTailscaleStatus describes the host machine's existing Tailscale daemon state.
type SystemTailscaleStatus struct {
	Available     bool     `json:"available"`
	Running       bool     `json:"running"`
	BackendState  string   `json:"backend_state"`
	DNSName       string   `json:"dns_name,omitempty"`
	TailscaleIPs  []string `json:"tailscale_ips,omitempty"`
	SuggestedURLs []string `json:"suggested_urls,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// TSNetSidecarStatus describes the optional user-downloaded nex-tsnet sidecar state.
type TSNetSidecarStatus struct {
	PlatformSupported bool     `json:"platform_supported"`
	Installed         bool     `json:"installed"`
	BinaryPath        string   `json:"binary_path,omitempty"`
	Running           bool     `json:"running"`
	State             string   `json:"state"` // "not_installed" | "stopped" | "starting" | "needs_login" | "running" | "error" | "unsupported_mobile"
	AuthURL           string   `json:"auth_url,omitempty"`
	DNSName           string   `json:"dns_name,omitempty"`
	TailscaleIPs      []string `json:"tailscale_ips,omitempty"`
	ListenURL         string   `json:"listen_url,omitempty"`
	Error             string   `json:"error,omitempty"`
}

// ParseTailscaleStatusJSON parses output of `tailscale status --json` and computes SuggestedURLs.
func ParseTailscaleStatusJSON(raw []byte, listenPort int) (SystemTailscaleStatus, error) {
	var parsed struct {
		BackendState string `json:"BackendState"`
		Self         struct {
			DNSName      string   `json:"DNSName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return SystemTailscaleStatus{}, err
	}
	dnsName := strings.TrimRight(strings.TrimSpace(parsed.Self.DNSName), ".")
	running := strings.EqualFold(strings.TrimSpace(parsed.BackendState), "Running")
	st := SystemTailscaleStatus{
		Available:    true,
		Running:      running,
		BackendState: strings.TrimSpace(parsed.BackendState),
		DNSName:      dnsName,
		TailscaleIPs: parsed.Self.TailscaleIPs,
	}
	if running {
		st.SuggestedURLs = buildTailnetURLs(dnsName, parsed.Self.TailscaleIPs, listenPort)
	}
	return st, nil
}

func buildTailnetURLs(dnsName string, ips []string, listenPort int) []string {
	var out []string
	if dnsName != "" {
		if listenPort > 0 && listenPort != 443 && listenPort != 80 {
			out = append(out, fmt.Sprintf("http://%s:%d", dnsName, listenPort))
		}
		out = append(out, "https://"+dnsName)
	}
	if listenPort > 0 {
		for _, ipStr := range ips {
			ip := net.ParseIP(strings.TrimSpace(ipStr))
			if ip == nil {
				continue
			}
			if ip.To4() != nil {
				out = append(out, fmt.Sprintf("http://%s:%d", ip.String(), listenPort))
				break
			}
		}
	}
	return cleanURLList(out)
}

// ProbeSystemTailscale checks if an OS-installed tailscale CLI/daemon is running and logged in.
func ProbeSystemTailscale(ctx context.Context, listenPort int) SystemTailscaleStatus {
	if runtime.GOOS == "ios" || runtime.GOOS == "android" {
		return SystemTailscaleStatus{
			Available:    false,
			Running:      false,
			BackendState: "mobile_os_managed",
		}
	}
	binPath := findSystemTailscaleBinary()
	if binPath == "" {
		return SystemTailscaleStatus{
			Available:    false,
			Running:      false,
			BackendState: "not_installed",
		}
	}

	pCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(pCtx, binPath, "status", "--json").Output()
	if err != nil {
		return SystemTailscaleStatus{
			Available:    true,
			Running:      false,
			BackendState: "stopped",
			Error:        err.Error(),
		}
	}
	st, pErr := ParseTailscaleStatusJSON(out, listenPort)
	if pErr != nil {
		return SystemTailscaleStatus{
			Available:    true,
			Running:      false,
			BackendState: "error",
			Error:        pErr.Error(),
		}
	}
	return st
}

func findSystemTailscaleBinary() string {
	if p, err := exec.LookPath("tailscale"); err == nil && p != "" {
		return p
	}
	switch runtime.GOOS {
	case "darwin":
		const macAppCLI = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
		if st, err := os.Stat(macAppCLI); err == nil && !st.IsDir() {
			return macAppCLI
		}
	case "windows":
		const winCLI = `C:\Program Files\Tailscale\tailscale.exe`
		if st, err := os.Stat(winCLI); err == nil && !st.IsDir() {
			return winCLI
		}
	}
	return ""
}

func resolveTSNetPlatformArtifact(goos, goarch string) (string, error) {
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	switch goos + "/" + goarch {
	case "darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64":
		return fmt.Sprintf("nex-tsnet-%s-%s%s", goos, goarch, ext), nil
	default:
		return "", errors.New("unsupported platform for prebuilt nex-tsnet sidecar")
	}
}
