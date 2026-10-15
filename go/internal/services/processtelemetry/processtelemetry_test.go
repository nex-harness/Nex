// processtelemetry_test.go (step 3.8): the whole-machine table parsers, the
// CPU differential (with its PID-reuse guard), and the Host descendant
// attribution. Platform-independent by construction: the parsers take the raw
// text, so the vectors run on every platform CI has.
package processtelemetry

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestParseProcStatHandlesCommWithSpacesAndParens(t *testing.T) {
	// comm is `(my proc (x))` — splitting on the FIRST ')' would corrupt
	// everything after it, so the parser splits on the LAST one.
	content := "1234 (my proc (x)) S 42 1234 1234 0 -1 4194304 100 0 0 0 " +
		"250 150 0 0 20 0 3 0 98765 12345678 900 18446744073709551615 1 1 1 1"
	row, ok := parseProcStat(content)
	if !ok {
		t.Fatal("parseProcStat failed on a comm containing parentheses")
	}
	if row.PID != 1234 || row.PPID != 42 {
		t.Fatalf("pid/ppid = %d/%d", row.PID, row.PPID)
	}
	if row.Command != "my proc (x)" {
		t.Fatalf("command = %q", row.Command)
	}
	// (utime 250 + stime 150) ticks at USER_HZ=100 → 4000ms.
	if row.CPUTimeMs != 4000 {
		t.Fatalf("cpuTimeMs = %d, want 4000", row.CPUTimeMs)
	}
}

func TestParseProcStatRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"no parens":         "1234 S 42",
		"truncated":         "1234 (cmd) S 42",
		"no fields":         "1234 (cmd) ",
		"zero pid":          "0 (cmd) S 42 " + strings.Repeat("1 ", 20),
		"negative ppid":     "1 (cmd) S -5 " + strings.Repeat("1 ", 20),
		"empty":             "",
		"close before open": ")x( (cmd) S 1 " + strings.Repeat("1 ", 20),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseProcStat(content); ok {
				t.Fatalf("parseProcStat accepted %q", content)
			}
		})
	}
}

func TestParseVmRssKb(t *testing.T) {
	status := "Name:\tnode\nState:\tS (sleeping)\nVmPeak:\t  400000 kB\nVmRSS:\t  12345 kB\nThreads:\t9\n"
	if got := parseVmRssKb(status); got != 12345 {
		t.Fatalf("VmRSS = %d, want 12345", got)
	}
	if got := parseVmRssKb("Name:\tnode\n"); got != 0 {
		t.Fatalf("missing VmRSS = %d, want 0", got)
	}
	if got := parseVmRssKb("VmRSS:\tnotanumber kB\n"); got != 0 {
		t.Fatalf("malformed VmRSS = %d, want 0", got)
	}
}

func TestParseProcStartTicksAndParentGroup(t *testing.T) {
	content := "77 (bash) S 42 77 77 0 -1 4194304 10 0 0 0 5 5 0 0 20 0 1 0 654321 987 654 18446744073709551615 1 2 3"
	ticks, ok := ParseProcStartTicks(content)
	if !ok || ticks != 654321 {
		t.Fatalf("start ticks = %d/%v, want 654321", ticks, ok)
	}
	ppid, pgid, ok := ParseProcParentAndGroup(content)
	if !ok || ppid != 42 || pgid != 77 {
		t.Fatalf("ppid/pgid = %d/%d/%v", ppid, pgid, ok)
	}
	if _, ok := ParseProcStartTicks("no parens"); ok {
		t.Fatal("missing comm must be rejected")
	}
	if _, _, ok := ParseProcParentAndGroup("77 (bash) S"); ok {
		t.Fatal("a truncated stat must be rejected")
	}
}

func TestParseDarwinProcessTable(t *testing.T) {
	// `ps -axo pid=,ppid=,rss=,cputime=,comm=` — comm holds spaces and the
	// cputime column carries a day/hour/millisecond form.
	stdout := "  100     1  2048  0:01.50 /Applications/Nex.app/Contents/MacOS/Nex\n" +
		"  200   100  4096 12:34:56 /usr/local/bin/node helper name\n" +
		"  300   200  1024 1-02:03:04 /bin/sh\n" +
		"garbage line\n" +
		"    0     0     0 0:00.00 /sbin/launchd\n" +
		"  400   300   512  bad  /bin/bad\n"
	rows := ParseDarwinProcessTable(stdout)
	if len(rows) != 3 {
		t.Fatalf("parsed %d rows, want 3: %#v", len(rows), rows)
	}
	if rows[0].PID != 100 || rows[0].PPID != 1 || rows[0].RSSKb != 2048 {
		t.Fatalf("row 0 = %#v", rows[0])
	}
	// 0:01.50 → 1500ms (the fraction is dropped, matching Number arithmetic).
	if rows[0].CPUTimeMs != 1500 {
		t.Fatalf("cpu time = %d, want 1500", rows[0].CPUTimeMs)
	}
	if rows[0].Command != "/Applications/Nex.app/Contents/MacOS/Nex" {
		t.Fatalf("command = %q", rows[0].Command)
	}
	if rows[1].Command != "/usr/local/bin/node helper name" {
		t.Fatalf("a comm with spaces must survive: %q", rows[1].Command)
	}
	// 12:34:56 → 12h34m56s.
	if rows[1].CPUTimeMs != (12*60+34)*60_000+56_000 {
		t.Fatalf("cpu time = %d", rows[1].CPUTimeMs)
	}
	// 1-02:03:04 → 1 day + 2h03m04s.
	want := ((26*60+3)*60 + 4) * 1000
	if rows[2].CPUTimeMs != int64(want) {
		t.Fatalf("cpu time with day prefix = %d, want %d", rows[2].CPUTimeMs, want)
	}
	// pid 0 (kernel) and an unparsable cputime are both dropped.
	for _, row := range rows {
		if row.PID == 0 {
			t.Fatal("pid 0 must be dropped")
		}
		if row.PID == 400 {
			t.Fatal("an unparsable cputime must be dropped")
		}
	}
}

func TestParseCPUTimeText(t *testing.T) {
	cases := []struct {
		text string
		want int64
		ok   bool
	}{
		{"0:01.50", 1500, true},
		{"12:34:56", 45296000, true},
		{"1-02:03:04", 93784000, true},
		{"0:00.00", 0, true},
		{"", 0, false},
		{"nonsense", 0, false},
		{"1:2:3:4", 0, false},
		{"-1:00", 0, false},
		{"aa:bb", 0, false},
	}
	for _, testCase := range cases {
		got, ok := ParseCPUTimeText(testCase.text)
		if got != testCase.want || ok != testCase.ok {
			t.Errorf("ParseCPUTimeText(%q) = %d/%v, want %d/%v",
				testCase.text, got, ok, testCase.want, testCase.ok)
		}
	}
}

func TestParseWindowsProcessTable(t *testing.T) {
	stdout := "  100   1  2097152  50000000  Nex.exe\r\n" +
		"  200 100  1048576 100000000  node helper.exe\r\n" +
		"broken\r\n"
	rows := ParseWindowsProcessTable(stdout)
	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2: %#v", len(rows), rows)
	}
	if rows[0].RSSKb != 2048 {
		t.Fatalf("working set = %d KiB, want 2048", rows[0].RSSKb)
	}
	// 50,000,000 × 100ns = 5,000,000,000ns = 5000ms.
	if rows[0].CPUTimeMs != 5000 {
		t.Fatalf("cpu time = %d, want 5000", rows[0].CPUTimeMs)
	}
	if rows[1].Command != "node helper.exe" {
		t.Fatalf("a name with spaces must survive: %q", rows[1].Command)
	}
}

func TestSamplerComputesNormalizedCPUPercent(t *testing.T) {
	// 4 logical cores: 2000ms of CPU over 1000ms of wall time on one process
	// across 4 cores is 50% of the machine.
	now := int64(1_000_000)
	table := [][]ResourceRow{
		{{PID: 1, PPID: 0, RSSKb: 100, CPUTimeMs: 10_000, Command: "node"}},
		{{PID: 1, PPID: 0, RSSKb: 100, CPUTimeMs: 12_000, Command: "node"}},
	}
	sampler := NewSampler(SamplerOptions{
		ReadTable:       scriptedTable(table...),
		Now:             func() int64 { return now },
		LogicalCPUCount: func() int { return 4 },
	})
	first, ok := sampler.Sample(context.Background())
	if !ok {
		t.Fatal("first sample was skipped")
	}
	// The first sample has no baseline: 0%, never a fabricated spike.
	if first[1].CPUPercent != 0 {
		t.Fatalf("first sample cpu = %v, want 0", first[1].CPUPercent)
	}
	if first[1].RSSKb != 100 {
		t.Fatalf("rss = %d", first[1].RSSKb)
	}
	now += 1000
	second, ok := sampler.Sample(context.Background())
	if !ok {
		t.Fatal("second sample was skipped")
	}
	if second[1].CPUPercent != 50 {
		t.Fatalf("cpu = %v, want 50 (2000ms over 1000ms on 4 cores)", second[1].CPUPercent)
	}
}

func TestSamplerRoundsAndClampsCPUPercent(t *testing.T) {
	now := int64(0)
	sampler := NewSampler(SamplerOptions{
		ReadTable: scriptedTable(
			// 1 tick over 3ms across 1 core → 33.333…% → 33.3
			[]ResourceRow{{PID: 1, PPID: 0, RSSKb: 1, CPUTimeMs: 0, Command: "x"}},
			[]ResourceRow{{PID: 1, PPID: 0, RSSKb: 1, CPUTimeMs: 1, Command: "x"}},
		),
		Now:             func() int64 { return now },
		LogicalCPUCount: func() int { return 1 },
	})
	_, _ = sampler.Sample(context.Background())
	now += 3
	samples, _ := sampler.Sample(context.Background())
	if samples[1].CPUPercent != 33.3 {
		t.Fatalf("cpu = %v, want 33.3 (one decimal place)", samples[1].CPUPercent)
	}
}

func TestSamplerResetsBaselineOnPIDReuse(t *testing.T) {
	now := int64(0)
	sampler := NewSampler(SamplerOptions{
		ReadTable: scriptedTable(
			[]ResourceRow{{PID: 7, PPID: 1, RSSKb: 1, CPUTimeMs: 1000, Command: "old-agent"}},
			// Same pid, DIFFERENT command → the pid was recycled.
			[]ResourceRow{{PID: 7, PPID: 1, RSSKb: 2, CPUTimeMs: 5000, Command: "new-agent"}},
		),
		Now:             func() int64 { return now },
		LogicalCPUCount: func() int { return 1 },
	})
	_, _ = sampler.Sample(context.Background())
	now += 1000
	samples, _ := sampler.Sample(context.Background())
	if samples[7].CPUPercent != 0 {
		t.Fatalf("a recycled pid must not inherit the old delta, got %v", samples[7].CPUPercent)
	}
}

func TestSamplerResetsBaselineWhenCPUTimeGoesBackwards(t *testing.T) {
	now := int64(0)
	sampler := NewSampler(SamplerOptions{
		ReadTable: scriptedTable(
			[]ResourceRow{{PID: 7, PPID: 1, RSSKb: 1, CPUTimeMs: 9000, Command: "same"}},
			[]ResourceRow{{PID: 7, PPID: 1, RSSKb: 1, CPUTimeMs: 10, Command: "same"}},
		),
		Now:             func() int64 { return now },
		LogicalCPUCount: func() int { return 1 },
	})
	_, _ = sampler.Sample(context.Background())
	now += 1000
	samples, _ := sampler.Sample(context.Background())
	if samples[7].CPUPercent != 0 {
		t.Fatalf("a backwards cputime must re-baseline, got %v", samples[7].CPUPercent)
	}
}

func TestSamplerExpiresBaselinesAfterTTL(t *testing.T) {
	now := int64(0)
	sampler := NewSampler(SamplerOptions{
		ReadTable: scriptedTable(
			[]ResourceRow{{PID: 7, PPID: 1, RSSKb: 1, CPUTimeMs: 100, Command: "gone"}},
			[]ResourceRow{},
		),
		Now:             func() int64 { return now },
		LogicalCPUCount: func() int { return 1 },
	})
	if _, ok := sampler.Sample(context.Background()); !ok {
		t.Fatal("sample skipped")
	}
	if sampler.BaselineCount() != 1 {
		t.Fatalf("baselines = %d", sampler.BaselineCount())
	}
	// Inside the TTL the baseline survives: a briefly unreadable /proc entry
	// must not reset the delta to zero.
	now += CPUBaselineTTLMs
	_, _ = sampler.Sample(context.Background())
	if sampler.BaselineCount() != 1 {
		t.Fatal("a baseline must survive up to the TTL")
	}
	now += 1
	_, _ = sampler.Sample(context.Background())
	if sampler.BaselineCount() != 0 {
		t.Fatal("a baseline past the TTL must be dropped")
	}
}

func TestSamplerSkipsTheRoundWhenTheTableReadFails(t *testing.T) {
	sampler := NewSampler(SamplerOptions{
		ReadTable:       func(context.Context) ([]ResourceRow, bool) { return nil, false },
		LogicalCPUCount: func() int { return 1 },
	})
	samples, ok := sampler.Sample(context.Background())
	if ok || samples != nil {
		t.Fatalf("a failed table read must skip the round, got %v/%v", samples, ok)
	}
}

func TestSamplerSkipsTheRoundWhenTheContextEnded(t *testing.T) {
	read := false
	sampler := NewSampler(SamplerOptions{
		ReadTable: func(ctx context.Context) ([]ResourceRow, bool) {
			read = true
			return []ResourceRow{{PID: 1}}, true
		},
		LogicalCPUCount: func() int { return 1 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := sampler.Sample(ctx); ok {
		t.Fatal("a cancelled context must skip the round")
	}
	if !read {
		t.Fatal("the reader is still consulted (it owns the ctx check)")
	}
}

func TestAttributeHostProcessTreeAttributesByNearestAncestor(t *testing.T) {
	// Topology:
	//   host(100)
	//    ├─ agent(200) ── mcp-root(300) ── mcp-child(301)
	//    └─ orphan(400)
	//   unrelated(900) — ppid 1, not under the host
	samples := map[int]Sample{
		100: {PID: 100, PPID: 1, Command: "/opt/nex/host"},
		200: {PID: 200, PPID: 100, RSSKb: 5000, CPUPercent: 10, Command: "/opt/nex/nex-agent-nex-demo"},
		300: {PID: 300, PPID: 200, RSSKb: 3000, CPUPercent: 5, Command: "/opt/nex/mcp-server"},
		301: {PID: 301, PPID: 300, RSSKb: 1000, CPUPercent: 1, Command: "/opt/nex/mcp-helper"},
		400: {PID: 400, PPID: 100, RSSKb: 200, CPUPercent: 0.5, Command: "/bin/sh"},
		900: {PID: 900, PPID: 1, RSSKb: 42, CPUPercent: 2, Command: "/bin/false"},
	}
	rows := AttributeHostProcessTree(AttributeOptions{
		Samples: samples, HostPID: 100,
		Agents: []Agent{{
			PID: 200, Provider: "nex-demo", WorkspacePath: "/home/alice/my-project",
			Children: []ChildProcess{{
				PID: 300, ServerName: "filesystem", MCPSource: MCPSourceBuiltin, PluginName: "files",
			}},
		}},
	})
	byPID := map[int]UsageProcess{}
	for _, row := range rows {
		byPID[row.PID] = row
	}
	// The Host itself is never in the result.
	if _, present := byPID[100]; present {
		t.Fatal("the host process must not appear in its own subtree report")
	}
	if rows[0].PID != 200 || rows[len(rows)-1].PID != 400 {
		t.Fatalf("rows are not sorted by pid: %#v", rows)
	}
	agent := byPID[200]
	// Only the workspace LEAF is exposed, never the path.
	if agent.Name != "nex-agent-nex-demo-my-project" {
		t.Fatalf("agent name = %q", agent.Name)
	}
	if agent.GroupKey != "cli" || agent.Category != CategoryBase {
		t.Fatalf("agent group = %q/%q", agent.GroupKey, agent.Category)
	}
	if agent.MemoryBytes != 5000*1024 {
		t.Fatalf("memory = %d", agent.MemoryBytes)
	}
	mcpRoot := byPID[300]
	if mcpRoot.Name != "filesystem" || mcpRoot.Category != CategoryBuiltinPlugin ||
		mcpRoot.GroupKey != "builtin:files" || mcpRoot.GroupLabel != "files" {
		t.Fatalf("mcp root = %#v", mcpRoot)
	}
	// A child of the MCP root keeps the plain command name but inherits the group.
	mcpChild := byPID[301]
	if mcpChild.Name != "mcp-helper" || mcpChild.GroupKey != "builtin:files" {
		t.Fatalf("mcp child = %#v", mcpChild)
	}
	orphan := byPID[400]
	if orphan.GroupKey != "host" || orphan.Category != CategoryBase || orphan.Name != "sh" {
		t.Fatalf("orphan = %#v", orphan)
	}
	// A process outside the host subtree and outside the agent registry is
	// NOT reported: only the host's descendants and registered owner roots are.
	if _, present := byPID[900]; present {
		t.Fatalf("an unrelated process leaked into the report: %#v", byPID[900])
	}
}

func TestAttributeHostProcessTreeClassifiesCommunityPlugins(t *testing.T) {
	samples := map[int]Sample{
		100: {PID: 100, PPID: 1},
		200: {PID: 200, PPID: 100, Command: "agent"},
		300: {PID: 300, PPID: 200, Command: "custom-mcp"},
		310: {PID: 310, PPID: 200, Command: "plugin-mcp"},
	}
	rows := AttributeHostProcessTree(AttributeOptions{
		Samples: samples, HostPID: 100,
		Agents: []Agent{{PID: 200, Provider: "p", Children: []ChildProcess{
			{PID: 300, ServerName: "my-server", MCPSource: MCPSourceCustom},
			{PID: 310, ServerName: "third-party", MCPSource: MCPSourcePlugin, PluginName: "thirdparty"},
		}}},
	})
	byPID := map[int]UsageProcess{}
	for _, row := range rows {
		byPID[row.PID] = row
	}
	// A custom MCP has no plugin name: the server name is the group label.
	if byPID[300].Category != CategoryCommunityPlugin || byPID[300].GroupLabel != "my-server" ||
		byPID[300].GroupKey != "custom:my-server" {
		t.Fatalf("custom mcp = %#v", byPID[300])
	}
	// Only the official marketplace counts as builtin.
	if byPID[310].Category != CategoryCommunityPlugin {
		t.Fatalf("third-party marketplace plugins are community: %#v", byPID[310])
	}
}

func TestAttributeHostProcessTreeHandlesBuiltinPluginPids(t *testing.T) {
	samples := map[int]Sample{
		100: {PID: 100, PPID: 1},
		500: {PID: 500, PPID: 100, Command: "CuaHelper.exe"},
	}
	rows := AttributeHostProcessTree(AttributeOptions{
		Samples: samples, HostPID: 100,
		BuiltinPluginPids: map[int]string{500: "cua"},
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Category != CategoryBuiltinPlugin || rows[0].GroupKey != "cua" ||
		rows[0].Name != "CuaHelper" {
		t.Fatalf("builtin plugin row = %#v", rows[0])
	}
}

func TestAttributeHostProcessTreeSurvivesPPIDCycles(t *testing.T) {
	// A recycled PID can make ppid point at a descendant; the visited set must
	// keep the walk finite and every process reported exactly once.
	samples := map[int]Sample{
		100: {PID: 100, PPID: 1},
		200: {PID: 200, PPID: 300, Command: "a"},
		300: {PID: 300, PPID: 200, Command: "b"},
	}
	rows := AttributeHostProcessTree(AttributeOptions{Samples: samples, HostPID: 100})
	seen := map[int]int{}
	for _, row := range rows {
		seen[row.PID]++
	}
	for pid, count := range seen {
		if count != 1 {
			t.Fatalf("pid %d reported %d times", pid, count)
		}
	}
}

func TestCommandDisplayName(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/node":      "node",
		`C:\Program Files\Nex.exe`: "Nex",
		"  /bin/sh  ":              "sh",
		"":                         "process",
		"   ":                      "process",
		"node":                     "node",
	}
	for input, want := range cases {
		if got := CommandDisplayName(input); got != want {
			t.Errorf("CommandDisplayName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatNexAgentProcessName(t *testing.T) {
	cases := []struct {
		provider, workspace, want string
	}{
		{"nex-demo", "/home/alice/my-project", "nex-agent-nex-demo-my-project"},
		{"claude", `C:\Users\alice\work\proj`, "nex-agent-claude-proj"},
		{"openai", "", "nex-agent-openai"},
		{"openai", "   ", "nex-agent-openai"},
		{"", "/w", "nex-agent-w"},
		{"Some Provider!!", "/w", "nex-agent-some-provider-w"},
	}
	for _, testCase := range cases {
		got := FormatNexAgentProcessName(testCase.provider, testCase.workspace)
		if got != testCase.want {
			t.Errorf("FormatNexAgentProcessName(%q, %q) = %q, want %q",
				testCase.provider, testCase.workspace, got, testCase.want)
		}
	}
}

func TestSanitizeProcessNameSegmentCapsLength(t *testing.T) {
	long := strings.Repeat("a", 40)
	got := sanitizeProcessNameSegment(long)
	if len(got) != maxProcessNameSegmentChars {
		t.Fatalf("segment length = %d, want %d", len(got), maxProcessNameSegmentChars)
	}
	// Nothing survives as empty-but-nonempty input.
	if sanitizeProcessNameSegment("!!!") != "" {
		t.Fatal("a punctuation-only segment must sanitize to empty")
	}
}

func TestRoundPercentHandlesNonFinite(t *testing.T) {
	if got := roundPercent(math.NaN()); got != 0 {
		t.Fatalf("NaN = %v", got)
	}
	if got := roundPercent(math.Inf(1)); got != 0 {
		t.Fatalf("+Inf = %v", got)
	}
	if got := roundPercent(12.34); got != 12.3 {
		t.Fatalf("12.34 = %v", got)
	}
	if got := clampPercent(150); got != 100 {
		t.Fatalf("clampPercent(150) = %v, want 100", got)
	}
	if got := clampPercent(-5); got != 0 {
		t.Fatalf("clampPercent(-5) = %v, want 0", got)
	}
}

func TestCurrentPlatformMatchesTheProtocolEnum(t *testing.T) {
	switch CurrentPlatform() {
	case PlatformLinux, PlatformDarwin, PlatformWin32:
	default:
		t.Fatalf("CurrentPlatform() = %q is not in the protocol enum", CurrentPlatform())
	}
}

// scriptedTable walks the tables in order; the final entry is reused once the
// sequence is exhausted.
func scriptedTable(tables ...[]ResourceRow) TableReader {
	index := 0
	return func(context.Context) ([]ResourceRow, bool) {
		if index >= len(tables) {
			index = len(tables) - 1
		}
		table := tables[index]
		index++
		return table, true
	}
}
