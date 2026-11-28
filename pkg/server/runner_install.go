package server

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// resolvePublicOrigin derives the HTTP(S) origin and corresponding WS(S) runner URL
// from the incoming request (respecting X-Forwarded-Proto / X-Forwarded-Host).
func resolvePublicOrigin(r *http.Request) (httpBase string, wsURL string) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fp := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); fp != "" {
		scheme = strings.ToLower(strings.Split(fp, ",")[0])
	}
	host := r.Host
	if fh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); fh != "" {
		host = strings.TrimSpace(strings.Split(fh, ",")[0])
	}
	httpBase = fmt.Sprintf("%s://%s", scheme, host)
	wsScheme := "ws"
	if scheme == "https" {
		wsScheme = "wss"
	}
	wsURL = fmt.Sprintf("%s://%s/ws/runner", wsScheme, host)
	return httpBase, wsURL
}

func (s *Server) authorizeInstallScript(r *http.Request) (token string, ok bool) {
	qTok := strings.TrimSpace(r.URL.Query().Get("token"))
	pairingSecret, requireAuth := s.effectiveRunnerPairingSecret(r.Context())
	if !requireAuth || pairingSecret == "" {
		return qTok, true
	}
	if qTok != "" && subtle.ConstantTimeCompare([]byte(qTok), []byte(pairingSecret)) == 1 {
		return qTok, true
	}
	// Also allow authenticated web sessions
	if enabled, authOK := s.isRequestAuthenticated(r.Context(), r); enabled && authOK {
		return pairingSecret, true
	}
	return "", false
}

// handleRunnerInstallPS1 serves a zero-SSH PowerShell one-liner installer for Windows runners:
//
//	irm "https://<server>/api/v1/runners/install.ps1?token=<TOKEN>" | iex
func (s *Server) handleRunnerInstallPS1(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	tok, ok := s.authorizeInstallScript(r)
	if !ok {
		http.Error(w, "# unauthorized: valid ?token=<pairing_secret> is required\n", http.StatusUnauthorized)
		return
	}
	httpBase, wsURL := resolvePublicOrigin(r)
	dlURL := httpBase + "/api/v1/runners/binary/windows/amd64"
	if tok != "" {
		dlURL += "?token=" + url.QueryEscape(tok)
	}
	escPS := func(v string) string {
		return strings.ReplaceAll(v, "'", "''")
	}

	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$InstallDir = Join-Path $env:LOCALAPPDATA 'Nex'
$BinPath = Join-Path $InstallDir 'nex-runner.exe'
$DataDir = Join-Path $env:USERPROFILE '.nex-runner'
$ServerWS = '%s'
$DownloadURL = '%s'
$RunnerToken = '%s'

Write-Host "==> Installing Nex Runner into $InstallDir ..." -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

# Stop any existing nex-runner process before replacing binary
Get-Process -Name 'nex-runner' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 300

Write-Host "==> Downloading nex-runner.exe ..." -ForegroundColor Cyan
Invoke-WebRequest -Uri $DownloadURL -OutFile $BinPath -UseBasicParsing

$RunnerArgs = '-server "' + $ServerWS + '" -data-dir "' + $DataDir + '" -no-uac'
if ($RunnerToken) {
    $RunnerArgs += ' -token "' + $RunnerToken + '"'
}

$IsAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if ($IsAdmin) {
    Write-Host "==> Registering Windows Service (Admin) ..." -ForegroundColor Cyan
    & $BinPath service stop 2>$null | Out-Null
    & $BinPath service uninstall 2>$null | Out-Null
    $SvcInstallArgs = @('service', 'install', '-server', $ServerWS, '-data-dir', $DataDir, '-no-uac')
    if ($RunnerToken) {
        $SvcInstallArgs += @('-token', $RunnerToken)
    }
    & $BinPath @SvcInstallArgs
    & $BinPath service start
} else {
    Write-Host "==> Registering Scheduled Task (Current User Logon, no UAC) ..." -ForegroundColor Cyan
    $TaskName = 'NexRunner'
    $Action = New-ScheduledTaskAction -Execute $BinPath -Argument $RunnerArgs
    $Trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $Settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit (New-TimeSpan -Days 3650) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
    Register-ScheduledTask -TaskName $TaskName -Action $Action -Trigger $Trigger -Settings $Settings -Force | Out-Null
    Start-Process -FilePath $BinPath -ArgumentList $RunnerArgs -WindowStyle Hidden
}

Write-Host "==> Nex Runner installed and started! Check Settings -> Runners in Nex." -ForegroundColor Green
`, escPS(wsURL), escPS(dlURL), escPS(tok))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(script))
}

// handleRunnerInstallSH serves a zero-SSH POSIX shell one-liner installer for Linux/macOS runners:
//
//	curl -fsSL "https://<server>/api/v1/runners/install.sh?token=<TOKEN>" | sh
func (s *Server) handleRunnerInstallSH(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	tok, ok := s.authorizeInstallScript(r)
	if !ok {
		http.Error(w, "# unauthorized: valid ?token=<pairing_secret> is required\n", http.StatusUnauthorized)
		return
	}
	httpBase, wsURL := resolvePublicOrigin(r)
	tokQuery := ""
	tokFlag := ""
	if tok != "" {
		tokQuery = "?token=" + url.QueryEscape(tok)
		tokFlag = fmt.Sprintf(" -token '%s'", strings.ReplaceAll(tok, "'", "'\"'\"'"))
	}

	script := fmt.Sprintf(`#!/usr/bin/env sh
set -eu

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

INSTALL_DIR="${HOME}/.local/bin"
DATA_DIR="${HOME}/.nex-runner"
BIN_PATH="${INSTALL_DIR}/nex-runner"
SERVER_WS='%s'
DOWNLOAD_URL='%s/api/v1/runners/binary/'"${OS}"'/'"${ARCH}"'%s'

mkdir -p "$INSTALL_DIR" "$DATA_DIR"
echo "==> Downloading nex-runner (${OS}/${ARCH}) ..."
curl -fsSL "$DOWNLOAD_URL" -o "${BIN_PATH}.tmp"
chmod +x "${BIN_PATH}.tmp"
mv "${BIN_PATH}.tmp" "$BIN_PATH"

pkill -f "nex-runner.*-server" 2>/dev/null || true
nohup "$BIN_PATH" -server "$SERVER_WS" -data-dir "$DATA_DIR"%s > "${DATA_DIR}/runner.log" 2>&1 &
echo "==> Nex Runner started in background (log: ${DATA_DIR}/runner.log)"
`, strings.ReplaceAll(wsURL, "'", "'\"'\"'"), strings.ReplaceAll(httpBase, "'", "'\"'\"'"), tokQuery, tokFlag)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(script))
}
