package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/remote"
	"github.com/nex-harness/nex/pkg/store"
)

func (s *Server) handleRemoteStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	st := s.remoteMgr.Status()
	devs, err := s.store.ListPairedDevices(r.Context(), false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              st,
		"paired_device_count": len(devs),
	})
}

func (s *Server) handleRemoteConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.remoteMgr.Status().Config)
	case http.MethodPut, http.MethodPost:
		cfg := s.remoteMgr.Status().Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := s.store.SaveRemoteHostConfigJSON(r.Context(), string(raw)); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.remoteMgr.ApplyConfig(cfg)
		writeJSON(w, http.StatusOK, s.remoteMgr.Status())
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRemotePairStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ttl := 5 * time.Minute
	if body.TTLSeconds > 0 && body.TTLSeconds <= 3600 {
		ttl = time.Duration(body.TTLSeconds) * time.Second
	}
	ps, err := s.remoteMgr.StartPairing(ttl)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handleRemotePairComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		PairToken       string `json:"pair_token"`
		DeviceName      string `json:"device_name"`
		Platform        string `json:"platform"`
		ClientPubKeyPEM string `json:"client_pubkey_pem"`
		Transport       string `json:"transport"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.PairToken = strings.TrimSpace(req.PairToken)
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	req.Platform = strings.TrimSpace(req.Platform)
	req.ClientPubKeyPEM = strings.TrimSpace(req.ClientPubKeyPEM)
	if req.PairToken == "" || req.DeviceName == "" || req.ClientPubKeyPEM == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pair_token, device_name, and client_pubkey_pem are required"})
		return
	}

	pubkeyFP, err := remote.FingerprintFromPubKeyPEM(req.ClientPubKeyPEM)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if peer, ok := remote.PeerFromContext(r.Context()); ok {
		if peer.PubKeyFP != pubkeyFP {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "TLS client certificate does not match submitted public key"})
			return
		}
		if req.Transport == "" {
			req.Transport = peer.Transport
		}
	}

	if !s.remoteMgr.ConsumePairingToken(req.PairToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired pair_token"})
		return
	}

	tokBytes := make([]byte, 32)
	if _, err := rand.Read(tokBytes); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	rawDeviceToken := hex.EncodeToString(tokBytes)

	saved, err := s.store.UpsertPairedDevice(r.Context(), store.PairedDevice{
		Name:          req.DeviceName,
		Platform:      req.Platform,
		PubKeyFP:      pubkeyFP,
		PubKeyPEM:     req.ClientPubKeyPEM,
		TokenHash:     sha256Hex(rawDeviceToken),
		LastTransport: req.Transport,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	id := s.remoteMgr.Identity()
	st := s.remoteMgr.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":    saved.ID,
		"device_token": rawDeviceToken,
		"node_id":      id.NodeID,
		"fingerprint":  id.Fingerprint,
		"host_name":    st.HostName,
		"relay_url":    st.RelayURL,
		"direct_urls":  st.DirectURLs,
		"lan_urls":     st.LANURLs,
	})
}

func (s *Server) handleRemoteDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	includeRevoked := r.URL.Query().Get("include_revoked") == "1"
	list, err := s.store.ListPairedDevices(r.Context(), includeRevoked)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": list,
	})
}

func (s *Server) handleRemoteDeviceSubroutes(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/remote/devices/")
	id = strings.Trim(id, "/")
	if id == "" || strings.Contains(id, "/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fp, err := s.store.RevokePairedDevice(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	s.remoteMgr.DisconnectDevice(fp)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"device_id": id,
	})
}

func (s *Server) handleRemoteTSNetProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	sysTS, tsStatus := s.remoteMgr.ProbeTSNet(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"system_tailscale": sysTS,
		"tsnet":            tsStatus,
	})
}

func (s *Server) handleRemoteTSNetInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		DownloadURL string `json:"download_url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	st, err := s.remoteMgr.InstallTSNet(r.Context(), body.DownloadURL, s.client)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"tsnet": st,
	})
}

func (s *Server) handleRemoteTSNetUninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := s.remoteMgr.UninstallTSNet(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sysTS, tsStatus := s.remoteMgr.ProbeTSNet(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"system_tailscale": sysTS,
		"tsnet":            tsStatus,
	})
}
