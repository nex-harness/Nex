package remote

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// PairingInvite captures everything a client needs to verify, reach, and pair with an intranet host.
type PairingInvite struct {
	Version     int      `json:"version"`
	NodeID      string   `json:"node_id"`
	Fingerprint string   `json:"fingerprint"`
	HostName    string   `json:"host_name,omitempty"`
	Token       string   `json:"token"`
	RelayURL    string   `json:"relay_url,omitempty"`
	RelayToken  string   `json:"relay_token,omitempty"`
	DirectURLs  []string `json:"direct_urls,omitempty"`
	LANURLs     []string `json:"lan_urls,omitempty"`
}

// EncodePairingURI formats a PairingInvite into a compact nex://pair?... URI suitable for QR codes or deep links.
func EncodePairingURI(inv PairingInvite) string {
	q := url.Values{}
	q.Set("v", "1")
	q.Set("node", strings.TrimSpace(inv.NodeID))
	q.Set("fp", strings.ToLower(strings.TrimSpace(inv.Fingerprint)))
	if strings.TrimSpace(inv.HostName) != "" {
		q.Set("name", strings.TrimSpace(inv.HostName))
	}
	q.Set("token", strings.TrimSpace(inv.Token))
	if strings.TrimSpace(inv.RelayURL) != "" {
		q.Set("relay", strings.TrimSpace(inv.RelayURL))
	}
	if strings.TrimSpace(inv.RelayToken) != "" {
		q.Set("rtok", strings.TrimSpace(inv.RelayToken))
	}
	if len(inv.DirectURLs) > 0 {
		q.Set("direct", strings.Join(cleanURLList(inv.DirectURLs), ","))
	}
	if len(inv.LANURLs) > 0 {
		q.Set("lan", strings.Join(cleanURLList(inv.LANURLs), ","))
	}
	return "nex://pair?" + q.Encode()
}

// ParsePairingURI parses and validates a nex://pair?... URI string.
func ParsePairingURI(raw string) (PairingInvite, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return PairingInvite{}, fmt.Errorf("invalid pairing URI: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "nex") || !strings.EqualFold(u.Host, "pair") {
		return PairingInvite{}, errors.New("pairing URI must use nex://pair")
	}
	q := u.Query()
	v := 1
	if vs := strings.TrimSpace(q.Get("v")); vs != "" {
		parsedV, err := strconv.Atoi(vs)
		if err != nil || parsedV != 1 {
			return PairingInvite{}, fmt.Errorf("unsupported pairing URI version %q", vs)
		}
		v = parsedV
	}
	nodeID := strings.TrimSpace(q.Get("node"))
	fp := strings.ToLower(strings.TrimSpace(q.Get("fp")))
	token := strings.TrimSpace(q.Get("token"))
	if nodeID == "" || fp == "" || token == "" {
		return PairingInvite{}, errors.New("pairing URI missing required node, fp, or token")
	}
	return PairingInvite{
		Version:     v,
		NodeID:      nodeID,
		Fingerprint: fp,
		HostName:    strings.TrimSpace(q.Get("name")),
		Token:       token,
		RelayURL:    strings.TrimSpace(q.Get("relay")),
		RelayToken:  strings.TrimSpace(q.Get("rtok")),
		DirectURLs:  splitCleanCSV(q.Get("direct")),
		LANURLs:     splitCleanCSV(q.Get("lan")),
	}, nil
}

// DiscoverLANURLs returns up to 4 sorted private IPv4 HTTP URLs for the host's local network interfaces.
func DiscoverLANURLs(port int) []string {
	if port <= 0 {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			if !ip4.IsPrivate() {
				continue
			}
			u := fmt.Sprintf("http://%s:%d", ip4.String(), port)
			if _, exists := seen[u]; !exists {
				seen[u] = struct{}{}
				out = append(out, u)
			}
		}
	}
	sort.Strings(out)
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func cleanURLList(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimRight(strings.TrimSpace(item), "/")
		if trimmed == "" {
			continue
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func splitCleanCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return cleanURLList(strings.Split(raw, ","))
}
