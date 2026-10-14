// mcp.go: MCP server config readers for the three source formats —
// mcpServersJson ({"mcpServers": {…}} / nex cli config {"mcp": {"servers": …}}),
// mcpJson (openCode {"mcp": {name: {command: [bin, args…]}}} with type
// normalization), codexToml ([mcp_servers.x] tables).
package settingssync

import (
	"encoding/json"
	"os"
	"strings"
)

// mcpServersMap is Record<string, McpServerConfig>; the config value stays a
// raw map (McpServerConfig is an open interface).
type mcpServersMap map[string]map[string]any

// readMcpServersFromSourceFile ports readMcpServersFromSourceFile: any parse
// failure yields an empty map (per-file isolation).
func readMcpServersFromSourceFile(filePath string, format mcpConfigFormat) mcpServersMap {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return mcpServersMap{}
	}
	if format == formatCodexToml {
		return readTomlMcpServers(raw)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return mcpServersMap{}
	}
	if format == formatMcpJSON {
		return readOpenCodeMcpServers(parsed)
	}
	return readWrappedMcpServers(parsed)
}

// normalizeMcpServerMap ports normalizeMcpServerMap: drop blank names and
// non-object configs.
func normalizeMcpServerMap(value any) mcpServersMap {
	obj, ok := value.(map[string]any)
	if !ok {
		return mcpServersMap{}
	}
	servers := mcpServersMap{}
	for name, config := range obj {
		if strings.TrimSpace(name) == "" {
			continue
		}
		configObj, ok := config.(map[string]any)
		if !ok {
			continue
		}
		servers[name] = configObj
	}
	return servers
}

// readWrappedMcpServers ports readWrappedMcpServers: parsed.mcpServers.
func readWrappedMcpServers(parsed map[string]any) mcpServersMap {
	return normalizeMcpServerMap(parsed["mcpServers"])
}

// readOpenCodeMcpServers ports readOpenCodeMcpServers: parsed.mcp with each
// config normalized (command array → command + args, type local/remote).
func readOpenCodeMcpServers(parsed map[string]any) mcpServersMap {
	servers := normalizeMcpServerMap(parsed["mcp"])
	out := mcpServersMap{}
	for name, config := range servers {
		out[name] = normalizeOpenCodeMcpConfig(config)
	}
	return out
}

// normalizeOpenCodeMcpConfig ports normalizeOpenCodeMcpConfig.
func normalizeOpenCodeMcpConfig(config map[string]any) map[string]any {
	rawCommand := config["command"]
	if arr, ok := rawCommand.([]any); ok {
		var strs []string
		for _, item := range arr {
			if s, ok := item.(string); ok {
				strs = append(strs, s)
			}
		}
		if len(strs) > 0 {
			next := copyMapShallow(config)
			delete(next, "command")
			next["command"] = strs[0]
			if len(strs) > 1 {
				next["args"] = toAnySlice(strs[1:])
			}
			switch t, _ := config["type"].(string); t {
			case "local":
				next["type"] = "stdio"
			case "remote":
				next["type"] = "http"
			}
			return next
		}
	}
	if t, _ := config["type"].(string); t == "local" {
		if command, ok := rawCommand.(string); ok && strings.TrimSpace(command) != "" {
			next := copyMapShallow(config)
			next["type"] = "stdio"
			next["command"] = strings.TrimSpace(command)
			return next
		}
	}
	return config
}

// readTomlMcpServers ports readTomlMcpServers: parsed.mcp_servers ?? parsed
// .mcpServers (smol-toml output shape).
func readTomlMcpServers(raw []byte) mcpServersMap {
	parsed, ok := parseTOMLTables(raw)
	if !ok {
		return mcpServersMap{}
	}
	if servers, ok := parsed["mcp_servers"]; ok {
		return normalizeMcpServerMap(servers)
	}
	if servers, ok := parsed["mcpServers"]; ok {
		return normalizeMcpServerMap(servers)
	}
	return mcpServersMap{}
}

// parseTOMLTables parses the subset of TOML the codex config uses: top-level
// key/values and [table] / [table.sub] headers with scalar values, arrays of
// scalars and inline tables. A parse failure (structure the subset does not
// understand) reports ok=false and the caller yields no servers, matching
// the TS's try/catch.
func parseTOMLTables(raw []byte) (map[string]any, bool) {
	root := map[string]any{}
	current := root
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			end := strings.Index(trimmed, "]")
			if end == -1 {
				return nil, false
			}
			name := strings.TrimSpace(trimmed[1:end])
			if strings.HasPrefix(name, "[") || strings.HasSuffix(name, "]") {
				// Array-of-tables: not supported by the subset.
				return nil, false
			}
			current = ensureTOMLTable(root, strings.Split(name, "."))
			continue
		}
		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			return nil, false
		}
		key = strings.TrimSpace(key)
		parsed, ok := parseTOMLValue(strings.TrimSpace(value))
		if !ok {
			return nil, false
		}
		current[key] = parsed
	}
	return root, true
}

func ensureTOMLTable(root map[string]any, path []string) map[string]any {
	current := root
	for _, part := range path {
		part = strings.TrimSpace(part)
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	return current
}

func parseTOMLValue(value string) (any, bool) {
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		inner := strings.TrimSpace(value[1 : len(value)-1])
		if inner == "" {
			return []any{}, true
		}
		var out []any
		for _, part := range splitTopLevel(inner, ',') {
			item, ok := parseTOMLValue(strings.TrimSpace(part))
			if !ok {
				return nil, false
			}
			out = append(out, item)
		}
		return out, true
	}
	if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		inner := strings.TrimSpace(value[1 : len(value)-1])
		out := map[string]any{}
		if inner == "" {
			return out, true
		}
		for _, part := range splitTopLevel(inner, ',') {
			key, val, found := strings.Cut(strings.TrimSpace(part), "=")
			if !found {
				return nil, false
			}
			parsed, ok := parseTOMLValue(strings.TrimSpace(val))
			if !ok {
				return nil, false
			}
			out[strings.TrimSpace(key)] = parsed
		}
		return out, true
	}
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) >= 2 {
		return unescapeBasicString(value[1 : len(value)-1]), true
	}
	if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") && len(value) >= 2 {
		return value[1 : len(value)-1], true
	}
	switch value {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	if n, ok := parseTOMLNumber(value); ok {
		return n, true
	}
	return nil, false
}

func unescapeBasicString(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			default:
				sb.WriteByte('\\')
				sb.WriteByte(s[i])
			}
			continue
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func parseTOMLNumber(value string) (any, bool) {
	if value == "" {
		return nil, false
	}
	dots := strings.Count(value, ".")
	if dots > 1 {
		return nil, false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r == '.' || r == '-' || r == '+') {
			return nil, false
		}
	}
	if dots == 1 {
		var f float64
		if _, err := parseFloat(value, &f); err == nil {
			return f, true
		}
		return nil, false
	}
	var n int64
	if _, err := parseInt(value, &n); err == nil {
		return n, true
	}
	return nil, false
}

// splitTopLevel splits on sep outside quotes/brackets.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth := 0
	inString := byte(0)
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString != 0 {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == inString {
				inString = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inString = c
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

func copyMapShallow(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func parseFloat(s string, out *float64) (int, error) {
	return fmtSscan(s, out)
}

func parseInt(s string, out *int64) (int, error) {
	return fmtSscan(s, out)
}

func fmtSscan(s string, out any) (int, error) {
	return fmtSscanImpl(s, out)
}
