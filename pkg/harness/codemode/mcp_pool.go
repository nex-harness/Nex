package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nex-harness/nex/pkg/store"
)

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
	catalog      []ToolEntry
	liveConns    map[string]*liveLocalConn
	remoteCaller RemoteCaller
	customLocal  map[string]func(ctx context.Context, toolName string, args json.RawMessage) (any, error)
	stopReaper   chan struct{}
	closeOnce    sync.Once
}

// NewPool constructs a lazy-loading MCP connection pool and starts the background idle reaper.
func NewPool(remote RemoteCaller) *Pool {
	p := &Pool{
		servers:     make(map[string]store.MCPServerRecord),
		liveConns:   make(map[string]*liveLocalConn),
		remoteCaller: remote,
		customLocal: make(map[string]func(ctx context.Context, toolName string, args json.RawMessage) (any, error)),
		stopReaper:  make(chan struct{}),
	}
	go p.reapLoop()
	return p
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

	nextMap := make(map[string]store.MCPServerRecord, len(records))
	var rebuilt []ToolEntry
	// Preserve in-process namespaces
	for _, e := range p.catalog {
		if _, ok := p.customLocal[e.Namespace]; ok {
			rebuilt = append(rebuilt, e)
		}
	}

	for _, rec := range records {
		if !rec.Enabled {
			continue
		}
		nextMap[rec.Name] = rec
		var cached []ToolEntry
		if len(rec.CatalogCache) > 0 {
			_ = json.Unmarshal(rec.CatalogCache, &cached)
		}
		for _, t := range cached {
			t.Namespace = rec.Name
			t.FullName = rec.Name + "." + t.Name
			t.HostMode = rec.HostMode
			if len(t.InputSchema) == 0 {
				t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			rebuilt = append(rebuilt, t)
		}
	}

	// Close any live connections removed from config
	for name, lc := range p.liveConns {
		if _, stillPresent := nextMap[name]; !stillPresent {
			_ = lc.session.Close()
			delete(p.liveConns, name)
		}
	}

	p.servers = nextMap
	p.catalog = rebuilt
}

// NamespaceSummaryPrompt builds the ultra-compact MCP namespace summary (~few dozen tokens) for the System Prompt.
func (p *Pool) NamespaceSummaryPrompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.catalog) == 0 && len(p.servers) == 0 {
		return ""
	}
	counts := make(map[string]int)
	modes := make(map[string]string)
	for name, srv := range p.servers {
		modes[name] = srv.HostMode
	}
	for _, t := range p.catalog {
		counts[t.Namespace]++
		if modes[t.Namespace] == "" {
			modes[t.Namespace] = t.HostMode
		}
	}
	var sb strings.Builder
	sb.WriteString("Enabled MCP Namespaces (accessible lazily via `codemode` tool -> searchTools / describeTool / tools.<ns>.<tool>):\n")
	for ns, mode := range modes {
		fmt.Fprintf(&sb, "- %s (%s, %d tools)\n", ns, mode, counts[ns])
	}
	return strings.TrimRight(sb.String(), "\n")
}

// SearchTools searches the cached MCP catalog without starting any subprocesses.
func (p *Pool) SearchTools(query, namespace string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 20
	}
	q := strings.ToLower(strings.TrimSpace(query))
	terms := strings.Fields(q)
	online := p.remoteCaller != nil && p.remoteCaller.IsOnline()

	p.mu.Lock()
	snapshot := make([]ToolEntry, len(p.catalog))
	copy(snapshot, p.catalog)
	p.mu.Unlock()

	var out []map[string]any
	for _, t := range snapshot {
		if namespace != "" && t.Namespace != namespace {
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
	p.mu.Unlock()

	if strings.TrimSpace(rec.Command) == "" {
		return nil, fmt.Errorf("local MCP server %q has empty command", rec.Name)
	}

	cmd := exec.Command(rec.Command, rec.Args...)
	envList := os.Environ()
	for k, v := range rec.Env {
		envList = append(envList, k+"="+v)
	}
	cmd.Env = envList

	client := mcp.NewClient(&mcp.Implementation{Name: "nex-runner", Version: "0.3.0"}, nil)
	transport := &mcp.CommandTransport{Command: cmd}
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("spawn local MCP %q failed: %w", rec.Name, err)
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
		var kept []ToolEntry
		for _, existing := range p.catalog {
			if existing.Namespace != rec.Name {
				kept = append(kept, existing)
			}
		}
		p.catalog = append(kept, entries...)
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
