package runner

import (
	"testing"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/store"
)

func TestResolveProviderBaseURL(t *testing.T) {
	proxyProv := store.ProviderRecord{
		ID:        "corp_llm",
		Name:      "Corp Intranet LLM",
		Driver:    ai.DriverOpenAIChat,
		BaseURL:   "https://nex.tylinux.cn/llm-proxy/corp_llm",
		ProxyMode: "server_proxy",
		Enabled:   true,
	}

	// Relay-paired runner uses its own local E2EE tunnel proxy, ignoring PublicBaseURL.
	if got := resolveProviderBaseURL(proxyProv, "ws://127.0.0.1:38385/ws/runner"); got != "http://127.0.0.1:38385/llm-proxy/corp_llm" {
		t.Fatalf("relay runner expected local tunnel /llm-proxy URL, got %q", got)
	}

	// Direct LAN runner uses LAN wss origin directly (no relay detour).
	if got := resolveProviderBaseURL(proxyProv, "wss://nex.tylinux.cn/ws/runner"); got != "https://nex.tylinux.cn/llm-proxy/corp_llm" {
		t.Fatalf("LAN runner expected direct https /llm-proxy URL, got %q", got)
	}

	// Fallback to configured BaseURL when ServerWSURL is empty.
	if got := resolveProviderBaseURL(proxyProv, ""); got != proxyProv.BaseURL {
		t.Fatalf("expected fallback to prov.BaseURL %q, got %q", proxyProv.BaseURL, got)
	}

	// Direct provider mode preserves upstream BaseURL regardless of ServerWSURL.
	directProv := proxyProv
	directProv.ProxyMode = "direct"
	directProv.BaseURL = "http://10.0.0.5:8000/v1"
	if got := resolveProviderBaseURL(directProv, "ws://127.0.0.1:38385/ws/runner"); got != "http://10.0.0.5:8000/v1" {
		t.Fatalf("direct mode should keep original BaseURL, got %q", got)
	}
}
