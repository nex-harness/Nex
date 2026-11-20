package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

const (
	webFetchMaxBodyBytes   = 512 * 1024
	webFetchMaxOutputBytes = 40 * 1024
	webFetchTimeout        = 20 * time.Second
	webFetchCacheTTL       = 2 * time.Minute
)

type webCacheEntry struct {
	content   string
	expiresAt time.Time
}

// WebFetchTool fetches a public HTTP/HTTPS URL, strips HTML noise into clean text,
// enforces SSRF protection against private/loopback networks, and caches responses briefly.
type WebFetchTool struct {
	client             *http.Client
	allowPrivateHosts  bool // only set true in unit tests using httptest.Server
	mu                 sync.Mutex
	cache              map[string]webCacheEntry
}

func NewWebFetchTool() *WebFetchTool {
	return &WebFetchTool{
		client: &http.Client{
			Timeout: webFetchTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
		cache: make(map[string]webCacheEntry),
	}
}

func (t *WebFetchTool) Name() string                      { return "webfetch" }
func (t *WebFetchTool) Category() string                  { return "system" }
func (t *WebFetchTool) IsReadOnly(_ json.RawMessage) bool { return true }

func (t *WebFetchTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "webfetch",
		Category:    "system",
		Description: "Fetch content from a public HTTP/HTTPS URL and return clean readable text (HTML tags, scripts, and styles stripped). Blocks private/loopback addresses for safety and caches recent fetches.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {"type": "string", "description": "HTTP or HTTPS URL to fetch"},
				"max_chars": {"type": "integer", "description": "Optional max characters to return (default 40000)"}
			},
			"required": ["url"]
		}`),
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars,omitempty"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid webfetch arguments: " + err.Error(), IsError: true}, nil
	}
	rawURL := strings.TrimSpace(args.URL)
	if rawURL == "" {
		return Result{Output: "url is required", IsError: true}, nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Result{Output: "url must be a valid http:// or https:// URL", IsError: true}, nil
	}

	if !t.allowPrivateHosts {
		if err := validatePublicHost(ctx, parsed.Hostname()); err != nil {
			return Result{Output: "webfetch blocked: " + err.Error(), IsError: true}, nil
		}
	}

	maxChars := args.MaxChars
	if maxChars <= 0 || maxChars > webFetchMaxOutputBytes {
		maxChars = webFetchMaxOutputBytes
	}

	// Check TTL cache
	t.mu.Lock()
	if ent, ok := t.cache[rawURL]; ok && time.Now().Before(ent.expiresAt) {
		t.mu.Unlock()
		return Result{
			Output:   truncateWebText(ent.content, maxChars),
			Metadata: map[string]any{"url": rawURL, "cached": true},
		}, nil
	}
	t.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{Output: "failed to construct request: " + err.Error(), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "Nex-Agent/1.0 (compatible; +https://github.com/nex-harness/nex)")
	req.Header.Set("Accept", "text/html,text/plain,application/json,text/markdown;q=0.9,*/*;q=0.5")

	client := t.client
	if client == nil {
		client = &http.Client{Timeout: webFetchTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Output: fmt.Sprintf("webfetch request failed: %v", err), IsError: true}, nil
	}
	defer resp.Body.Close()

	// Re-check final host after redirects against SSRF
	if !t.allowPrivateHosts && resp.Request != nil && resp.Request.URL != nil {
		if err := validatePublicHost(ctx, resp.Request.URL.Hostname()); err != nil {
			return Result{Output: "webfetch blocked after redirect: " + err.Error(), IsError: true}, nil
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBodyBytes+1))
	if err != nil {
		return Result{Output: fmt.Sprintf("webfetch read failed: %v", err), IsError: true}, nil
	}

	if resp.StatusCode >= 400 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 400 {
			snippet = snippet[:400] + "..."
		}
		return Result{
			Output:   fmt.Sprintf("HTTP %d from %s: %s", resp.StatusCode, rawURL, snippet),
			IsError:  true,
			Metadata: map[string]any{"url": rawURL, "status_code": resp.StatusCode},
		}, nil
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	text := string(body)
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") || looksLikeHTML(text) {
		text = extractReadableHTML(text)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = "(empty response body)"
	}

	formatted := fmt.Sprintf("URL: %s\nStatus: %d\n\n%s", rawURL, resp.StatusCode, text)

	t.mu.Lock()
	t.cache[rawURL] = webCacheEntry{
		content:   formatted,
		expiresAt: time.Now().Add(webFetchCacheTTL),
	}
	t.mu.Unlock()

	return Result{
		Output:   truncateWebText(formatted, maxChars),
		Metadata: map[string]any{"url": rawURL, "status_code": resp.StatusCode, "cached": false},
	}, nil
}

func validatePublicHost(ctx context.Context, host string) error {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return fmt.Errorf("access to local/internal host %q is not allowed", host)
	}
	if ip := net.ParseIP(h); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("access to private/loopback IP %s is not allowed", ip.String())
		}
		return nil
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(resolveCtx, h)
	if err != nil {
		return fmt.Errorf("DNS lookup failed for %q: %v", host, err)
	}
	for _, ipa := range ips {
		if isBlockedIP(ipa.IP) {
			return fmt.Errorf("host %q resolved to blocked private/loopback IP %s", host, ipa.IP.String())
		}
	}
	return nil
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style|noscript|svg)[^>]*>.*?</(script|style|noscript|svg)>`)
	reBlockTags   = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|header|footer|pre|blockquote)[^>]*>`)
	reAllTags     = regexp.MustCompile(`<[^>]+>`)
	reMultiBlank  = regexp.MustCompile(`\n{3,}`)
	reMultiSpace  = regexp.MustCompile(`[ \t]{2,}`)
)

func looksLikeHTML(s string) bool {
	head := strings.ToLower(strings.TrimSpace(s))
	if len(head) > 256 {
		head = head[:256]
	}
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html") || strings.Contains(head, "<body")
}

func extractReadableHTML(raw string) string {
	s := reScriptStyle.ReplaceAllString(raw, " ")
	s = reBlockTags.ReplaceAllString(s, "\n")
	s = reAllTags.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	var cleaned []string
	for _, l := range lines {
		l = strings.TrimSpace(reMultiSpace.ReplaceAllString(l, " "))
		if l != "" {
			cleaned = append(cleaned, l)
		}
	}
	out := strings.Join(cleaned, "\n")
	return reMultiBlank.ReplaceAllString(out, "\n\n")
}

func truncateWebText(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + fmt.Sprintf("\n\n... [truncated %d more bytes]", len(s)-maxChars)
}
