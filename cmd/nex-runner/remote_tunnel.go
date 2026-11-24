package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nex-harness/nex/pkg/remote"
)

const (
	runnerIdentityFile = "remote_identity.json"
	pairedServerFile   = "paired_server.json"
)

type pairedServerState struct {
	Profile     remote.PairedHostProfile `json:"profile"`
	DeviceToken string                   `json:"device_token"`
}

func savePairedServerState(dataDir string, st pairedServerState) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, pairedServerFile), b, 0o600)
}

func loadPairedServerState(dataDir string) (*pairedServerState, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, pairedServerFile))
	if err != nil {
		return nil, err
	}
	var st pairedServerState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if strings.TrimSpace(st.Profile.NodeID) == "" || strings.TrimSpace(st.DeviceToken) == "" {
		return nil, errors.New("invalid paired_server.json")
	}
	return &st, nil
}

// pairRunnerWithURI completes pairing with an intranet Nex Server via LAN -> Direct -> nex-relay
// and persists the paired host profile + device token under dataDir.
func pairRunnerWithURI(ctx context.Context, dataDir, pairURI, runnerName string) (*pairedServerState, string, error) {
	invite, err := remote.ParsePairingURI(pairURI)
	if err != nil {
		return nil, "", fmt.Errorf("parse pairing URI: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, "", err
	}
	idPath := filepath.Join(dataDir, runnerIdentityFile)
	host, _ := os.Hostname()
	if strings.TrimSpace(runnerName) == "" {
		runnerName = host
	}
	if strings.TrimSpace(runnerName) == "" {
		runnerName = "nex-runner"
	}
	clientID, err := remote.LoadOrCreateIdentity(idPath, "nex-runner-"+runnerName)
	if err != nil {
		return nil, "", fmt.Errorf("load/create runner identity: %w", err)
	}
	profile, deviceToken, transport, err := remote.PairWithHost(ctx, clientID, invite, "Runner: "+runnerName, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		return nil, "", fmt.Errorf("pair with server: %w", err)
	}
	st := pairedServerState{
		Profile:     profile,
		DeviceToken: deviceToken,
	}
	if err := savePairedServerState(dataDir, st); err != nil {
		return nil, "", fmt.Errorf("save paired server state: %w", err)
	}
	return &st, transport, nil
}

// startPairedServerTunnel starts a local multi-path E2EE tunnel proxy (LAN -> Direct -> nex-relay)
// and returns the local WebSocket URL (ws://127.0.0.1:<port>/ws/runner), device token, and cleanup func.
func startPairedServerTunnel(ctx context.Context, dataDir string, st *pairedServerState) (wsURL string, authToken string, cleanup func(), err error) {
	if st == nil {
		return "", "", func() {}, errors.New("paired server state is nil")
	}
	idPath := filepath.Join(dataDir, runnerIdentityFile)
	clientID, err := remote.LoadOrCreateIdentity(idPath, "nex-runner")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("load runner identity: %w", err)
	}
	lp, err := remote.NewLocalProxy(clientID, st.Profile, st.DeviceToken, remote.LocalProxyOptions{
		OnStatusChanged: func(ts remote.TransportStatus) {
			log.Printf("nex-runner: tunnel transport=%s connected=%v target=%s", ts.Transport, ts.Connected, ts.Target)
		},
	})
	if err != nil {
		return "", "", func() {}, fmt.Errorf("start local tunnel proxy: %w", err)
	}
	status, err := lp.Connect(ctx)
	if err != nil {
		log.Printf("nex-runner: initial tunnel connect failed (%v); proxy will retry automatically", err)
	} else {
		log.Printf("nex-runner: paired tunnel ready via %s (%s -> %s)", status.Transport, status.LocalProxyURL, status.Target)
	}
	localWS := "ws" + strings.TrimPrefix(lp.LocalURL(), "http") + "/ws/runner"
	return localWS, st.DeviceToken, func() { _ = lp.Close() }, nil
}
