package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

const (
	webSearchTimeout       = 15 * time.Second
	webSearchCacheTTL      = 10 * time.Minute
	webSearchMaxBodyBytes  = 1 * 1024 * 1024
	webSearchDefaultMaxRes = 8
)

type webSearchCacheEntry struct {
	output    string
	count     int
	expiresAt time.Time
}

// WebSearchTool searches the public web (DuckDuckGo HTML or optional NEX_SEARCH_API_URL SearXNG endpoint)
// with domain allow/block filters and a 10-minute TTL cache.
type WebSearchTool struct {
	client    *http.Client
	customURL string // optional override for tests or SearXNG
	mu        sync.Mutex
	cache     map[string]webSearchCacheEntry
}

func NewWebSearchTool() *WebSearchTool {
	return &WebSearchTool{
		client:    &http.Client{Timeout: webSearchTimeout},
		customURL: strings.TrimSpace(os.Getenv("NEX_SEARCH_API_URL")),
		cache:     make(map[string]webSearchCacheEntry),
	}
}

func (t *WebSearchTool) Name() string                  { return "websearch" }
func (t *WebSearchTool) Category() string              { return "system" }
func (t *WebSearchTool) IsReadOnly(json.RawMessage) bool { return true }

func (t *WebSearchTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:     "websearch",
		Category: "system",
		Description: "Search the web for up-to-date documentation, API references, error messages, or current events. " +
			"Returns ranked titles, URLs, and snippets. Follow up with `webfetch` on promising URLs when full page content is needed.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Search query string."
				},
				"max_results": {
					"type": "integer",
					"description": "Maximum number of results to return (default 8, max 15)."
				},
				"allowed_domains": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Optional list of domains to restrict results to (e.g. ['pkg.go.dev', 'developer.mozilla.org'])."
				},
				"blocked_domains": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Optional list of domains to exclude from results."
				}
			},
			"required": ["query"]
		}`),
	}
}

type searchHit struct {
	Title   string
	URL     string
	Snippet string
}

func (t *WebSearchTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args struct {
		Query          string   `json:"query"`
		MaxResults     int      `json:"max_results"`
		AllowedDomains []string `json:"allowed_domains"`
		BlockedDomains []string `json:"blocked_domains"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: "invalid websearch arguments: " + err.Error(), IsError: true}, nil
	}
	q := strings.TrimSpace(args.Query)
	if q == "" {
		return Result{Output: "query is required", IsError: true}, nil
	}
	maxRes := args.MaxResults
	if maxRes <= 0 {
		maxRes = webSearchDefaultMaxRes
	}
	if maxRes > 15 {
		maxRes = 15
	}

	cacheKey := fmt.Sprintf("%s|%d|%s|%s", q, maxRes, strings.Join(args.AllowedDomains, ","), strings.Join(args.BlockedDomains, ","))
	t.mu.Lock()
	if ent, ok := t.cache[cacheKey]; ok && time.Now().Before(ent.expiresAt) {
		t.mu.Unlock()
		return Result{
			Output:   ent.output,
			Metadata: map[string]any{"query": q, "results": ent.count, "cached": true},
		}, nil
	}
	t.mu.Unlock()

	var hits []searchHit
	var err error
	if t.customURL != "" {
		hits, err = t.searchCustomEndpoint(ctx, q)
	} else {
		hits, err = t.searchDuckDuckGo(ctx, q)
	}
	if err != nil {
		return Result{Output: fmt.Sprintf("websearch failed: %v", err), IsError: true}, nil
	}

	filtered := filterSearchHits(hits, args.AllowedDomains, args.BlockedDomains)
	if len(filtered) > maxRes {
		filtered = filtered[:maxRes]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Search results for: %s (%d results)\n\n", q, len(filtered))
	if len(filtered) == 0 {
		sb.WriteString("(No matching results found. Try broadening the query or removing domain filters.)\n")
	} else {
		for i, h := range filtered {
			fmt.Fprintf(&sb, "%d. **%s**\n   URL: %s\n", i+1, h.Title, h.URL)
			if h.Snippet != "" {
				fmt.Fprintf(&sb, "   %s\n", h.Snippet)
			}
			sb.WriteByte('\n')
		}
	}
	out := strings.TrimSpace(sb.String())

	t.mu.Lock()
	t.cache[cacheKey] = webSearchCacheEntry{
		output:    out,
		count:     len(filtered),
		expiresAt: time.Now().Add(webSearchCacheTTL),
	}
	t.mu.Unlock()

	return Result{
		Output:   out,
		Metadata: map[string]any{"query": q, "results": len(filtered), "cached": false},
	}, nil
}

func (t *WebSearchTool) searchCustomEndpoint(ctx context.Context, query string) ([]searchHit, error) {
	u, err := url.Parse(t.customURL)
	if err != nil {
		return nil, err
	}
	qv := u.Query()
	qv.Set("q", query)
	if qv.Get("format") == "" {
		qv.Set("format", "json")
	}
	u.RawQuery = qv.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ai.DefaultUserAgent)
	req.Header.Set("Accept", "application/json, text/html")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webSearchMaxBodyBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Try SearXNG / generic JSON `{results: [{title, url, content/snippet}]}`
	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &parsed) == nil && len(parsed.Results) > 0 {
		hits := make([]searchHit, 0, len(parsed.Results))
		for _, r := range parsed.Results {
			snip := strings.TrimSpace(r.Content)
			if snip == "" {
				snip = strings.TrimSpace(r.Snippet)
			}
			if r.URL != "" {
				hits = append(hits, searchHit{Title: strings.TrimSpace(r.Title), URL: strings.TrimSpace(r.URL), Snippet: snip})
			}
		}
		return hits, nil
	}
	// Fallback to DDG HTML parser if custom server returned HTML
	return parseDuckDuckGoHTML(string(body)), nil
}

func (t *WebSearchTool) searchDuckDuckGo(ctx context.Context, query string) ([]searchHit, error) {
	endpoint := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ai.DefaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d from search provider", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webSearchMaxBodyBytes))
	if err != nil {
		return nil, err
	}
	return parseDuckDuckGoHTML(string(body)), nil
}

var (
	reDDGResultBlock = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	reDDGSnippet     = regexp.MustCompile(`(?is)<(?:a|div|span)[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</(?:a|div|span)>`)
)

func parseDuckDuckGoHTML(raw string) []searchHit {
	linkMatches := reDDGResultBlock.FindAllStringSubmatchIndex(raw, 25)
	if len(linkMatches) == 0 {
		return nil
	}
	hits := make([]searchHit, 0, len(linkMatches))
	for i, idx := range linkMatches {
		href := html.UnescapeString(raw[idx[2]:idx[3]])
		title := cleanHTMLInline(raw[idx[4]:idx[5]])
		resolvedURL := unwrapDDGRedirect(href)
		if resolvedURL == "" || title == "" {
			continue
		}

		// Look for snippet between this result link and the next one
		segEnd := len(raw)
		if i+1 < len(linkMatches) {
			segEnd = linkMatches[i+1][0]
		}
		seg := raw[idx[1]:segEnd]
		snippet := ""
		if sm := reDDGSnippet.FindStringSubmatch(seg); len(sm) >= 2 {
			snippet = cleanHTMLInline(sm[1])
		}
		hits = append(hits, searchHit{Title: title, URL: resolvedURL, Snippet: snippet})
	}
	return hits
}

func unwrapDDGRedirect(href string) string {
	h := strings.TrimSpace(href)
	if strings.HasPrefix(h, "//") {
		h = "https:" + h
	}
	if u, err := url.Parse(h); err == nil {
		if uddg := u.Query().Get("uddg"); uddg != "" {
			return uddg
		}
		if u.Scheme == "http" || u.Scheme == "https" {
			return u.String()
		}
	}
	return ""
}

func cleanHTMLInline(s string) string {
	stripped := reAllTags.ReplaceAllString(s, " ")
	unescaped := html.UnescapeString(stripped)
	return strings.TrimSpace(reMultiSpace.ReplaceAllString(strings.ReplaceAll(unescaped, "\n", " "), " "))
}

func filterSearchHits(hits []searchHit, allowed, blocked []string) []searchHit {
	if len(allowed) == 0 && len(blocked) == 0 {
		return hits
	}
	out := make([]searchHit, 0, len(hits))
	for _, h := range hits {
		u, err := url.Parse(h.URL)
		if err != nil {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if len(allowed) > 0 && !domainMatchesAny(host, allowed) {
			continue
		}
		if len(blocked) > 0 && domainMatchesAny(host, blocked) {
			continue
		}
		out = append(out, h)
	}
	return out
}

func domainMatchesAny(host string, domains []string) bool {
	for _, d := range domains {
		clean := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, ".")))
		if clean == "" {
			continue
		}
		if host == clean || strings.HasSuffix(host, "."+clean) {
			return true
		}
	}
	return false
}
