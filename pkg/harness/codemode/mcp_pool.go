package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nex-harness/nex/pkg/store"
)

// MCPProbeResult reports the live handshake & tool discovery result for an MCP server.
type MCPProbeResult struct {
	Name       string      `json:"name"`
	Status     string      `json:"status"` // "connected" | "error" | "disabled"
	ToolCount  int         `json:"tool_count"`
	Tools      []ToolEntry `json:"tools,omitempty"`
	Error      string      `json:"error,omitempty"`
	StderrTail string      `json:"stderr_tail,omitempty"`
	LatencyMs  int64       `json:"latency_ms"`
	CheckedAt  int64       `json:"checked_at"`
}

// expandEnvPlaceholdersWithExtra expands ${VAR} references using extraEnv first, then os.LookupEnv.
func expandEnvPlaceholdersWithExtra(s string, extraEnv map[string]string) string {
	return os.Expand(s, func(key string) string {
		if v, ok := extraEnv[key]; ok {
			return v
		}
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		return "${" + key + "}"
	})
}

// expandEnvPlaceholders expands ${VAR} references using os.Getenv (Pi mcp.md convention).
func expandEnvPlaceholders(s string) string {
	return expandEnvPlaceholdersWithExtra(s, nil)
}

// mergeEnvVars overlays one or more env layers on top of base without duplicate keys.
func mergeEnvVars(base []string, layers ...map[string]string) []string {
	merged := make(map[string]string, len(base))
	for _, entry := range base {
		if k, v, ok := strings.Cut(entry, "="); ok && k != "" {
			merged[k] = v
		}
	}
	for _, layer := range layers {
		for k, v := range layer {
			if strings.TrimSpace(k) != "" {
				merged[k] = v
			}
		}
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+merged[k])
	}
	return out
}

type headerRoundTripper struct {
	base     http.RoundTripper
	headers  map[string]string
	extraEnv map[string]string
}

func (rt *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range rt.headers {
		if strings.TrimSpace(k) != "" {
			clone.Header.Set(k, expandEnvPlaceholdersWithExtra(v, rt.extraEnv))
		}
	}
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func connectMCPServer(ctx context.Context, rec store.MCPServerRecord) (*mcp.ClientSession, *bytes.Buffer, error) {
	return connectMCPServerWithEnv(ctx, rec, nil)
}

func connectMCPServerWithEnv(ctx context.Context, rec store.MCPServerRecord, extraEnv map[string]string) (*mcp.ClientSession, *bytes.Buffer, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "nex-runner", Version: "0.4.1"}, nil)
	transportKind := strings.ToLower(strings.TrimSpace(rec.Transport))
	if transportKind == "http" || transportKind == "streamable-http" || (transportKind == "" && strings.TrimSpace(rec.URL) != "") {
		endpoint := strings.TrimSpace(expandEnvPlaceholdersWithExtra(rec.URL, extraEnv))
		if endpoint == "" {
			return nil, nil, fmt.Errorf("HTTP MCP server %q has empty url", rec.Name)
		}
		hc := &http.Client{
			Timeout:   30 * time.Second,
			Transport: &headerRoundTripper{base: http.DefaultTransport, headers: rec.Headers, extraEnv: extraEnv},
		}
		tr := &mcp.StreamableClientTransport{
			Endpoint:             endpoint,
			HTTPClient:           hc,
			DisableStandaloneSSE: true,
		}
		sess, err := client.Connect(ctx, tr, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("connect HTTP MCP %q failed: %w", rec.Name, err)
		}
		return sess, nil, nil
	}

	if strings.TrimSpace(rec.Command) == "" {
		return nil, nil, fmt.Errorf("local MCP server %q has empty command", rec.Name)
	}
	args := make([]string, len(rec.Args))
	for i, a := range rec.Args {
		args[i] = expandEnvPlaceholdersWithExtra(a, extraEnv)
	}
	cmd := exec.Command(expandEnvPlaceholdersWithExtra(rec.Command, extraEnv), args...)
	if ws := strings.TrimSpace(rec.WorkspacePath); ws != "" {
		if info, err := os.Stat(ws); err == nil && info.IsDir() {
			cmd.Dir = ws
		}
	}
	expandedRecEnv := make(map[string]string, len(rec.Env))
	for k, v := range rec.Env {
		expandedRecEnv[k] = expandEnvPlaceholdersWithExtra(v, extraEnv)
	}
	cmd.Env = mergeEnvVars(os.Environ(), extraEnv, expandedRecEnv)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	tr := &mcp.CommandTransport{Command: cmd}
	sess, err := client.Connect(ctx, tr, nil)
	if err != nil {
		errMsg := err.Error()
		if tail := trimTail(stderrBuf.String(), 600); tail != "" {
			errMsg += " | stderr: " + tail
		}
		return nil, &stderrBuf, fmt.Errorf("spawn local MCP %q failed: %s", rec.Name, errMsg)
	}
	return sess, &stderrBuf, nil
}

func trimTail(s string, maxChars int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxChars {
		return s
	}
	return "…" + s[len(s)-maxChars:]
}

// ProbeServer tests connectivity to an MCP server (stdio or streamable HTTP), lists its tools,
// and cleanly closes the ephemeral probe session.
func ProbeServer(ctx context.Context, rec store.MCPServerRecord) MCPProbeResult {
	started := time.Now()
	res := MCPProbeResult{
		Name:      rec.Name,
		CheckedAt: started.UnixMilli(),
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	sess, stderrBuf, err := connectMCPServer(probeCtx, rec)
	if err != nil {
		res.Status = "error"
		res.Error = err.Error()
		if stderrBuf != nil {
			res.StderrTail = trimTail(stderrBuf.String(), 1200)
		}
		res.LatencyMs = time.Since(started).Milliseconds()
		return res
	}
	defer func() { _ = sess.Close() }()

	toolsRes, lErr := sess.ListTools(probeCtx, nil)
	if lErr != nil {
		res.Status = "error"
		res.Error = fmt.Sprintf("tools/list failed: %v", lErr)
		if stderrBuf != nil {
			res.StderrTail = trimTail(stderrBuf.String(), 1200)
		}
		res.LatencyMs = time.Since(started).Milliseconds()
		return res
	}
	if toolsRes != nil {
		entries := make([]ToolEntry, 0, len(toolsRes.Tools))
		for _, mt := range toolsRes.Tools {
			schemaBytes, _ := json.Marshal(mt.InputSchema)
			ro := mt.Annotations != nil && mt.Annotations.ReadOnlyHint
			entries = append(entries, ToolEntry{
				Namespace:   rec.Name,
				Name:        mt.Name,
				FullName:    rec.Name + "." + mt.Name,
				Description: mt.Description,
				InputSchema: schemaBytes,
				ReadOnly:    ro,
				HostMode:    rec.HostMode,
			})
		}
		res.Tools = entries
		res.ToolCount = len(entries)
	}
	res.Status = "connected"
	res.LatencyMs = time.Since(started).Milliseconds()
	return res
}

// ToolEntry represents a cached MCP tool descriptor stored in the lazy catalog index.
type ToolEntry struct {
	Namespace   string          `json:"namespace"`
	Name        string          `json:"name"`
	FullName    string          `json:"full_name"` // "<namespace>.<name>"
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	ReadOnly    bool            `json:"read_only"`
	HostMode    string          `json:"host_mode"` // "runner_local" | "server_hosted"
}

// RemoteCaller forwards server_hosted MCP calls over the Runner<->Server WebSocket when online.
type RemoteCaller interface {
	IsOnline() bool
	CallRemoteMCP(ctx context.Context, namespace, toolName string, args json.RawMessage) (json.RawMessage, error)
}

type liveLocalConn struct {
	session    *mcp.ClientSession
	lastUsedAt time.Time
	idleTTL    time.Duration
}

// Pool implements Two-Tier Lazy Loading for MCP servers:
// 1. Catalog index cached in memory/SQLite without keeping stdio processes alive.
// 2. Spawn-on-First-Call + automatic Idle TTL process reaping for local stdio MCP servers.
type Pool struct {
	mu           sync.Mutex
	servers      map[string]store.MCPServerRecord
	discovered   map[string]DiscoveredMCPServer
	catalog      []ToolEntry
	hydratedAt   map[string]time.Time
	hydrating    map[string]chan struct{}
	liveConns    map[string]*liveLocalConn
	remoteCaller RemoteCaller
	customLocal  map[string]func(ctx context.Context, toolName string, args json.RawMessage) (any, error)
	extraEnv     map[string]string
	connectFn    func(ctx context.Context, rec store.MCPServerRecord) (*mcp.ClientSession, *bytes.Buffer, error)
	stopReaper   chan struct{}
	closeOnce    sync.Once
}

// NewPool constructs a lazy-loading MCP connection pool and starts the background idle reaper.
func NewPool(remote RemoteCaller) *Pool {
	p := &Pool{
		servers:      make(map[string]store.MCPServerRecord),
		discovered:   make(map[string]DiscoveredMCPServer),
		hydratedAt:   make(map[string]time.Time),
		hydrating:    make(map[string]chan struct{}),
		liveConns:    make(map[string]*liveLocalConn),
		remoteCaller: remote,
		customLocal:  make(map[string]func(ctx context.Context, toolName string, args json.RawMessage) (any, error)),
		stopReaper:   make(chan struct{}),
	}
	go p.reapLoop()
	return p
}

func envMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// SetExtraEnv updates the Credentials Vault environment injected into runner-local MCP processes.
// When the effective env changes, open local MCP sessions are closed so subsequent calls spawn with fresh credentials.
func (p *Pool) SetExtraEnv(env map[string]string) {
	p.mu.Lock()
	if envMapsEqual(p.extraEnv, env) {
		p.mu.Unlock()
		return
	}
	if len(env) == 0 {
		p.extraEnv = nil
	} else {
		copied := make(map[string]string, len(env))
		for k, v := range env {
			copied[k] = v
		}
		p.extraEnv = copied
	}
	staleConns := make([]*mcp.ClientSession, 0, len(p.liveConns))
	for k, lc := range p.liveConns {
		staleConns = append(staleConns, lc.session)
		delete(p.liveConns, k)
	}
	for ns := range p.hydratedAt {
		if !p.namespaceHasCatalogLocked(ns) {
			delete(p.hydratedAt, ns)
		}
	}
	p.mu.Unlock()

	for _, s := range staleConns {
		_ = s.Close()
	}
}

func mcpServerChanged(a, b store.MCPServerRecord) bool {
	if a.Command != b.Command || a.URL != b.URL || a.Transport != b.Transport || a.HostMode != b.HostMode {
		return true
	}
	return strings.Join(a.Args, "\x00") != strings.Join(b.Args, "\x00")
}

func (p *Pool) clearNamespaceCatalogLocked(namespace string) {
	kept := make([]ToolEntry, 0, len(p.catalog))
	for _, e := range p.catalog {
		if e.Namespace != namespace {
			kept = append(kept, e)
		}
	}
	p.catalog = kept
}

// RegisterInProcessNamespace registers an in-process MCP namespace (useful for built-in bridges & deterministic testing).
func (p *Pool) RegisterInProcessNamespace(
	namespace string,
	tools []ToolEntry,
	handler func(ctx context.Context, toolName string, args json.RawMessage) (any, error),
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.customLocal[namespace] = handler
	filtered := make([]ToolEntry, 0, len(p.catalog)+len(tools))
	for _, e := range p.catalog {
		if e.Namespace != namespace {
			filtered = append(filtered, e)
		}
	}
	for _, t := range tools {
		t.Namespace = namespace
		t.FullName = namespace + "." + t.Name
		if t.HostMode == "" {
			t.HostMode = "runner_local"
		}
		if len(t.InputSchema) == 0 {
			t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		filtered = append(filtered, t)
	}
	p.catalog = filtered
}

// SyncServers updates the configured MCP server list and loads their cached tool catalogs without spawning idle processes.
func (p *Pool) SyncServers(records []store.MCPServerRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()

	nextMap := make(map[string]store.MCPServerRecord, len(records)+len(p.discovered))
	var rebuilt []ToolEntry
	// Preserve in-process namespaces
	for _, e := range p.catalog {
		if _, ok := p.customLocal[e.Namespace]; ok {
			rebuilt = append(rebuilt, e)
		}
	}

	for _, rec := range records {
		// Keep all configured servers in the pool map so per-workspace overrides can enable/disable them dynamically.
		if !rec.Enabled && len(rec.WorkspaceOverrides) == 0 {
			continue
		}
		nextMap[rec.Name] = rec
	}

	// Preserve file-discovered local MCP servers (.mcp.json / user mcp.json) across control-plane config.sync updates.
	for _, d := range p.discovered {
		if !d.Enabled {
			continue
		}
		if _, exists := nextMap[d.Name]; !exists || d.Source == "workspace" {
			nextMap[d.Name] = d.MCPServerRecord
		}
	}

	existingByNS := make(map[string][]ToolEntry)
	for _, e := range p.catalog {
		if _, inProc := p.customLocal[e.Namespace]; !inProc {
			existingByNS[e.Namespace] = append(existingByNS[e.Namespace], e)
		}
	}

	for name, rec := range nextMap {
		var cached []ToolEntry
		if len(rec.CatalogCache) > 0 {
			_ = json.Unmarshal(rec.CatalogCache, &cached)
		}
		if len(cached) > 0 {
			for _, t := range cached {
				t.Namespace = rec.Name
				t.FullName = rec.Name + "." + t.Name
				t.HostMode = rec.HostMode
				if len(t.InputSchema) == 0 {
					t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
				}
				rebuilt = append(rebuilt, t)
			}
			continue
		}
		// Keep previously hydrated catalog entries if this server configuration is unchanged.
		if prev, existed := p.servers[name]; existed && !mcpServerChanged(prev, rec) {
			rebuilt = append(rebuilt, existingByNS[name]...)
		}
	}

	// Close any live connections removed or modified in config & prune stale hydration cache
	for name, lc := range p.liveConns {
		nextRec, stillPresent := nextMap[name]
		prevRec, hadPrev := p.servers[name]
		if !stillPresent || (hadPrev && mcpServerChanged(prevRec, nextRec)) {
			_ = lc.session.Close()
			delete(p.liveConns, name)
		}
	}
	for name := range p.hydratedAt {
		nextRec, stillPresent := nextMap[name]
		prevRec, hadPrev := p.servers[name]
		if !stillPresent || (hadPrev && mcpServerChanged(prevRec, nextRec)) {
			delete(p.hydratedAt, name)
		}
	}

	p.servers = nextMap
	p.catalog = rebuilt
}

func (p *Pool) isNamespaceVisibleLocked(namespace, workspacePath string) bool {
	if _, inProc := p.customLocal[namespace]; inProc {
		return true
	}
	srv, ok := p.servers[namespace]
	if !ok {
		return false
	}
	if store.NormalizeMCPExposure(srv.Exposure) == "hidden" {
		return false
	}
	vis, _ := store.IsMCPVisibleInWorkspace(srv, workspacePath)
	return vis
}

// IsNamespaceVisibleInWorkspace returns true when a namespace is enabled for workspacePath and not hidden.
func (p *Pool) IsNamespaceVisibleInWorkspace(namespace, workspacePath string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isNamespaceVisibleLocked(namespace, workspacePath)
}

// NamespaceSummaryPrompt builds the ultra-compact MCP namespace summary (~few dozen tokens) for the System Prompt.
func (p *Pool) NamespaceSummaryPrompt() string {
	return p.NamespaceSummaryPromptForWorkspace("")
}

// NamespaceSummaryPromptForWorkspace builds the MCP namespace summary filtered by per-workspace visibility & exposure.
func (p *Pool) NamespaceSummaryPromptForWorkspace(workspacePath string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.catalog) == 0 && len(p.servers) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, t := range p.catalog {
		counts[t.Namespace]++
	}

	type summaryRow struct {
		name     string
		mode     string
		exposure string
		desc     string
		count    int
	}
	var rows []summaryRow
	seen := map[string]bool{}

	for name, srv := range p.servers {
		exp := store.NormalizeMCPExposure(srv.Exposure)
		if exp == "hidden" {
			continue
		}
		if vis, _ := store.IsMCPVisibleInWorkspace(srv, workspacePath); !vis {
			continue
		}
		seen[name] = true
		rows = append(rows, summaryRow{
			name:     name,
			mode:     srv.HostMode,
			exposure: exp,
			desc:     strings.TrimSpace(srv.Description),
			count:    counts[name],
		})
	}
	for ns := range p.customLocal {
		if seen[ns] {
			continue
		}
		rows = append(rows, summaryRow{
			name:     ns,
			mode:     "runner_local",
			exposure: "codemode",
			count:    counts[ns],
		})
	}
	if len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	var sb strings.Builder
	sb.WriteString("Enabled MCP Namespaces (accessible lazily via `codemode` tool -> searchTools / describeTool / tools.<ns>.<tool>):\n")
	for _, row := range rows {
		if row.desc != "" {
			fmt.Fprintf(&sb, "- %s (%s, %s, %d tools): %s\n", row.name, row.mode, row.exposure, row.count, row.desc)
		} else {
			fmt.Fprintf(&sb, "- %s (%s, %s, %d tools)\n", row.name, row.mode, row.exposure, row.count)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

const (
	hydrateRetryCooldown = 30 * time.Second
	hydrateTimeout       = 10 * time.Second
)

func (p *Pool) namespaceHasCatalogLocked(namespace string) bool {
	for _, t := range p.catalog {
		if t.Namespace == namespace {
			return true
		}
	}
	return false
}

// EnsureNamespaceHydrated lazily connects to an uncached runner-local MCP server (such as one discovered
// from .mcp.json without a pre-populated catalog cache) and populates p.catalog via tools/list.
func (p *Pool) EnsureNamespaceHydrated(ctx context.Context, namespace string) error {
	for {
		p.mu.Lock()
		if _, inProc := p.customLocal[namespace]; inProc || p.namespaceHasCatalogLocked(namespace) {
			p.mu.Unlock()
			return nil
		}
		rec, ok := p.servers[namespace]
		if !ok || (rec.HostMode != "" && rec.HostMode != "runner_local") {
			p.mu.Unlock()
			return nil
		}
		if probedAt, ok := p.hydratedAt[namespace]; ok && time.Since(probedAt) < hydrateRetryCooldown {
			p.mu.Unlock()
			return nil
		}
		if waitCh, busy := p.hydrating[namespace]; busy {
			p.mu.Unlock()
			select {
			case <-waitCh:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		waitCh := make(chan struct{})
		p.hydrating[namespace] = waitCh
		p.mu.Unlock()

		hydrateCtx, cancel := context.WithTimeout(ctx, hydrateTimeout)
		_, err := p.getOrSpawnLocalSession(hydrateCtx, rec)
		cancel()

		p.mu.Lock()
		if err != nil {
			p.hydratedAt[namespace] = time.Now()
		}
		delete(p.hydrating, namespace)
		close(waitCh)
		p.mu.Unlock()
		return err
	}
}

func (p *Pool) uncachedLocalNamespaces(workspacePath string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	cachedNS := make(map[string]bool)
	for _, t := range p.catalog {
		cachedNS[t.Namespace] = true
	}
	var out []string
	for name, rec := range p.servers {
		if rec.HostMode != "" && rec.HostMode != "runner_local" {
			continue
		}
		if _, inProc := p.customLocal[name]; inProc || cachedNS[name] {
			continue
		}
		if probedAt, ok := p.hydratedAt[name]; ok && time.Since(probedAt) < hydrateRetryCooldown {
			continue
		}
		if workspacePath != "" && !p.isNamespaceVisibleLocked(name, workspacePath) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (p *Pool) hydrateNamespacesConcurrently(ctx context.Context, namespaces []string) {
	if len(namespaces) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, ns := range namespaces {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_ = p.EnsureNamespaceHydrated(ctx, name)
		}(ns)
	}
	wg.Wait()
}

// SearchTools searches the cached MCP catalog without starting any subprocesses.
func (p *Pool) SearchTools(query, namespace string, limit int) []map[string]any {
	return p.searchCatalog(query, namespace, "", limit)
}

// SearchToolsCtx searches the MCP catalog, filtering to namespaces visible in workspacePath and lazily
// hydrating uncached runner-local MCP servers (e.g. workspace .mcp.json servers) on demand.
func (p *Pool) SearchToolsCtx(ctx context.Context, query, namespace, workspacePath string, limit int) []map[string]any {
	if namespace != "" {
		_ = p.EnsureNamespaceHydrated(ctx, namespace)
		return p.searchCatalog(query, namespace, workspacePath, limit)
	}

	uncached := p.uncachedLocalNamespaces(workspacePath)
	if len(uncached) > 0 {
		terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
		var priority []string
		for _, ns := range uncached {
			nsLower := strings.ToLower(ns)
			if len(terms) == 0 {
				priority = append(priority, ns)
				continue
			}
			for _, term := range terms {
				if strings.Contains(nsLower, term) || strings.Contains(term, nsLower) {
					priority = append(priority, ns)
					break
				}
			}
		}
		if len(priority) > 0 {
			p.hydrateNamespacesConcurrently(ctx, priority)
		}
	}

	hits := p.searchCatalog(query, namespace, workspacePath, limit)
	if len(hits) == 0 {
		if remaining := p.uncachedLocalNamespaces(workspacePath); len(remaining) > 0 {
			p.hydrateNamespacesConcurrently(ctx, remaining)
			hits = p.searchCatalog(query, namespace, workspacePath, limit)
		}
	}
	return hits
}

func (p *Pool) searchCatalog(query, namespace, workspacePath string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 20
	}
	q := strings.ToLower(strings.TrimSpace(query))
	terms := strings.Fields(q)
	online := p.remoteCaller != nil && p.remoteCaller.IsOnline()

	p.mu.Lock()
	snapshot := make([]ToolEntry, len(p.catalog))
	copy(snapshot, p.catalog)
	visibleNS := make(map[string]bool)
	if workspacePath != "" {
		for _, t := range snapshot {
			if _, seen := visibleNS[t.Namespace]; !seen {
				visibleNS[t.Namespace] = p.isNamespaceVisibleLocked(t.Namespace, workspacePath)
			}
		}
	}
	p.mu.Unlock()

	out := make([]map[string]any, 0)
	for _, t := range snapshot {
		if namespace != "" && t.Namespace != namespace {
			continue
		}
		if workspacePath != "" && !visibleNS[t.Namespace] {
			continue
		}
		hay := strings.ToLower(t.FullName + " " + t.Description)
		matched := true
		for _, term := range terms {
			if !strings.Contains(hay, term) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		status := "available"
		if t.HostMode == "server_hosted" && !online {
			status = "offline_unavailable"
		}
		out = append(out, map[string]any{
			"name":        t.FullName,
			"namespace":   t.Namespace,
			"tool":        t.Name,
			"description": t.Description,
			"host_mode":   t.HostMode,
			"read_only":   t.ReadOnly,
			"status":      status,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// DescribeTool returns the full JSON Input Schema for fullName ("namespace.tool") on demand.
func (p *Pool) DescribeTool(fullName string) (map[string]any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.catalog {
		if t.FullName == fullName || t.Name == fullName {
			var schemaObj any
			_ = json.Unmarshal(t.InputSchema, &schemaObj)
			return map[string]any{
				"name":         t.FullName,
				"description":  t.Description,
				"host_mode":    t.HostMode,
				"read_only":    t.ReadOnly,
				"input_schema": schemaObj,
			}, nil
		}
	}
	return nil, fmt.Errorf("MCP tool %q not found in catalog", fullName)
}

// DescribeToolCtx returns the tool schema, lazily hydrating uncached local MCP servers when needed.
func (p *Pool) DescribeToolCtx(ctx context.Context, fullName string) (map[string]any, error) {
	if desc, err := p.DescribeTool(fullName); err == nil {
		return desc, nil
	}
	if ns, _, ok := strings.Cut(fullName, "."); ok && ns != "" {
		if err := p.EnsureNamespaceHydrated(ctx, ns); err != nil {
			return nil, err
		}
		return p.DescribeTool(fullName)
	}
	// If called with an unprefixed tool name, hydrate any uncached local servers before failing
	if uncached := p.uncachedLocalNamespaces(""); len(uncached) > 0 {
		p.hydrateNamespacesConcurrently(ctx, uncached)
	}
	return p.DescribeTool(fullName)
}

// LookupEntry finds a ToolEntry by namespace and toolName.
func (p *Pool) LookupEntry(namespace, toolName string) (ToolEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	full := namespace + "." + toolName
	for _, t := range p.catalog {
		if t.FullName == full {
			return t, true
		}
	}
	return ToolEntry{}, false
}

// Call invokes either a runner-local MCP tool (lazily spawning stdio if needed) or a server-hosted remote MCP tool.
func (p *Pool) Call(ctx context.Context, namespace, toolName string, args json.RawMessage) (any, error) {
	p.mu.Lock()
	inProc := p.customLocal[namespace]
	srvRec, hasSrv := p.servers[namespace]
	p.mu.Unlock()

	if inProc != nil {
		return inProc(ctx, toolName, args)
	}
	if !hasSrv {
		return nil, fmt.Errorf("unknown MCP namespace %q", namespace)
	}

	if srvRec.HostMode == "server_hosted" {
		if p.remoteCaller == nil || !p.remoteCaller.IsOnline() {
			return nil, fmt.Errorf("MCP tool %s.%s is server-hosted and offline_unavailable while Runner is disconnected", namespace, toolName)
		}
		rawRes, err := p.remoteCaller.CallRemoteMCP(ctx, namespace, toolName, args)
		if err != nil {
			return nil, err
		}
		var parsed any
		if json.Unmarshal(rawRes, &parsed) == nil {
			return parsed, nil
		}
		return string(rawRes), nil
	}

	// Runner-local stdio MCP: Spawn-on-First-Call!
	sess, err := p.getOrSpawnLocalSession(ctx, srvRec)
	if err != nil {
		return nil, err
	}

	var argMap map[string]any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &argMap)
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: argMap,
	})
	if err != nil {
		return nil, err
	}
	if res.IsError {
		return nil, errors.New(extractMCPText(res))
	}
	txt := extractMCPText(res)
	var structured any
	if json.Unmarshal([]byte(txt), &structured) == nil {
		return structured, nil
	}
	return txt, nil
}

func extractMCPText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (p *Pool) getOrSpawnLocalSession(ctx context.Context, rec store.MCPServerRecord) (*mcp.ClientSession, error) {
	p.mu.Lock()
	if lc, ok := p.liveConns[rec.Name]; ok {
		lc.lastUsedAt = time.Now()
		sess := lc.session
		p.mu.Unlock()
		return sess, nil
	}
	connect := p.connectFn
	var extraCopy map[string]string
	if len(p.extraEnv) > 0 {
		extraCopy = make(map[string]string, len(p.extraEnv))
		for k, v := range p.extraEnv {
			extraCopy[k] = v
		}
	}
	p.mu.Unlock()

	var (
		sess *mcp.ClientSession
		err  error
	)
	if connect != nil {
		sess, _, err = connect(ctx, rec)
	} else {
		sess, _, err = connectMCPServerWithEnv(ctx, rec, extraCopy)
	}
	if err != nil {
		return nil, err
	}

	// Opportunistically refresh catalog cache on first spawn
	if toolsRes, lErr := sess.ListTools(ctx, nil); lErr == nil && toolsRes != nil {
		var entries []ToolEntry
		for _, mt := range toolsRes.Tools {
			schemaBytes, _ := json.Marshal(mt.InputSchema)
			ro := mt.Annotations != nil && mt.Annotations.ReadOnlyHint
			entries = append(entries, ToolEntry{
				Namespace:   rec.Name,
				Name:        mt.Name,
				FullName:    rec.Name + "." + mt.Name,
				Description: mt.Description,
				InputSchema: schemaBytes,
				ReadOnly:    ro,
				HostMode:    "runner_local",
			})
		}
		p.mu.Lock()
		p.clearNamespaceCatalogLocked(rec.Name)
		p.catalog = append(p.catalog, entries...)
		p.hydratedAt[rec.Name] = time.Now()
		p.mu.Unlock()
	}

	ttl := time.Duration(rec.IdleTTLSec) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}

	p.mu.Lock()
	p.liveConns[rec.Name] = &liveLocalConn{
		session:    sess,
		lastUsedAt: time.Now(),
		idleTTL:    ttl,
	}
	p.mu.Unlock()
	return sess, nil
}

func (p *Pool) reapLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopReaper:
			return
		case now := <-ticker.C:
			p.ReapIdle(now)
		}
	}
}

// ReapIdle closes any spawned local stdio MCP sessions that have exceeded their IdleTTL.
func (p *Pool) ReapIdle(now time.Time) int {
	p.mu.Lock()
	var expired []*mcp.ClientSession
	for name, lc := range p.liveConns {
		if now.Sub(lc.lastUsedAt) >= lc.idleTTL {
			expired = append(expired, lc.session)
			delete(p.liveConns, name)
		}
	}
	p.mu.Unlock()
	for _, s := range expired {
		_ = s.Close()
	}
	return len(expired)
}

// Close shuts down the reaper and all active local MCP child processes.
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		close(p.stopReaper)
	})
	p.mu.Lock()
	conns := make([]*mcp.ClientSession, 0, len(p.liveConns))
	for k, lc := range p.liveConns {
		conns = append(conns, lc.session)
		delete(p.liveConns, k)
	}
	p.mu.Unlock()
	for _, s := range conns {
		_ = s.Close()
	}
}
