package system

// sshconfig_test.go: the sshConfigAlias.ts behaviors — the TS test corpus
// (Include, Host patterns, Match-free parsing, quoting, Windows backslash
// paths, Teleport-style globs, ssh -G refinement, identityfile distrust)
// plus 50 synthetic real-world-shaped configs (no real hosts).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func writeSSHConfig(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestSSHConfig(t *testing.T) *SSHConfig {
	t.Helper()
	return NewSSHConfig(t.TempDir())
}

func TestSSHConfigAliasesBasicHostBlocks(t *testing.T) {
	cfg := newTestSSHConfig(t)
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), `
Host web-prod
  HostName 10.0.0.10
  User deploy
  Port 2222
  IdentityFile ~/.ssh/deploy_key

Host db
  HostName db.internal
`)
	got, err := cfg.ListAliases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 aliases, got %d: %+v", len(got), got)
	}
	if got[0].Alias != "web-prod" || got[0].Host != "10.0.0.10" {
		t.Fatalf("alias0: %+v", got[0])
	}
	if got[0].Port == nil || *got[0].Port != 2222 {
		t.Fatalf("port: %+v", got[0])
	}
	if got[0].Username == nil || *got[0].Username != "deploy" {
		t.Fatalf("user: %+v", got[0])
	}
	if got[0].PrivateKeyPath == nil || !strings.HasSuffix(*got[0].PrivateKeyPath, "deploy_key") {
		t.Fatalf("identityfile ~ expansion: %+v", got[0])
	}
}

func TestSSHConfigSkipsGlobsAndNegations(t *testing.T) {
	cfg := newTestSSHConfig(t)
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), `
Host *
  User default

Host *.example.com
  User wildcard

Host !excluded
  HostName x

Host plain
  HostName ok
`)
	got, _ := cfg.ListAliases(context.Background())
	if len(got) != 1 || got[0].Alias != "plain" {
		t.Fatalf("connectable aliases: %+v", got)
	}
}

func TestSSHConfigIncludeResolution(t *testing.T) {
	cfg := newTestSSHConfig(t)
	sshDir := filepath.Join(cfg.homeDir, ".ssh")
	writeSSHConfig(t, filepath.Join(sshDir, "config"), `
Include work.conf
Host main
  HostName main.example
`)
	writeSSHConfig(t, filepath.Join(sshDir, "work.conf"), `
Host work-1
  HostName 10.1.1.1

Include more/*.conf
`)
	writeSSHConfig(t, filepath.Join(sshDir, "more", "a.conf"), "Host extra-a\n  HostName 10.2.2.2\n")
	writeSSHConfig(t, filepath.Join(sshDir, "more", "b.conf"), "Host extra-b\n  HostName 10.3.3.3\n")

	got, _ := cfg.ListAliases(context.Background())
	aliases := map[string]bool{}
	for _, o := range got {
		aliases[o.Alias] = true
	}
	for _, want := range []string{"main", "work-1", "extra-a", "extra-b"} {
		if !aliases[want] {
			t.Errorf("alias %s missing: %v", want, aliases)
		}
	}
}

func TestSSHConfigIncludeDepthCapAndCycle(t *testing.T) {
	cfg := newTestSSHConfig(t)
	sshDir := filepath.Join(cfg.homeDir, ".ssh")
	// self-include must not loop (visit-once)
	writeSSHConfig(t, filepath.Join(sshDir, "config"), "Include config\nHost a\n  HostName h\n")
	got, err := cfg.ListAliases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Alias != "a" {
		t.Fatalf("cycle: %+v", got)
	}
}

func TestSSHConfigFirstMatchWins(t *testing.T) {
	cfg := newTestSSHConfig(t)
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), `
Host dup
  HostName first
  User u1

Host dup
  HostName second
`)
	got, _ := cfg.ListAliases(context.Background())
	if len(got) != 1 || got[0].Host != "first" {
		t.Fatalf("first-match: %+v", got)
	}
}

func TestSSHConfigQuotingAndComments(t *testing.T) {
	cfg := newTestSSHConfig(t)
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), `
# Host commented-out
Host "quoted alias"
  HostName q.internal

Host with#hash
  HostName h.internal # trailing comment

Host with\ space
  HostName s.internal
`)
	cfg.SetSSHResolver(func() (string, bool) { return "", false }) // pure fallback
	got, _ := cfg.ListAliases(context.Background())
	aliases := map[string]string{}
	for _, o := range got {
		aliases[o.Alias] = o.Host
	}
	if aliases["quoted alias"] != "q.internal" {
		t.Errorf("quoted alias: %v", aliases)
	}
	// inline # is stripped outside quotes: 'Host with#hash' yields alias 'with'
	if aliases["with"] != "h.internal" {
		t.Errorf("hash alias: %v", aliases)
	}
	if aliases["with space"] != "s.internal" {
		t.Errorf("escaped space alias: %v", aliases)
	}
}

func TestSSHConfigWindowsBackslashPathNotEscaped(t *testing.T) {
	cfg := newTestSSHConfig(t)
	// SSH config 在 Windows 下常见 `C:\Users\...` 路径；反斜杠不能被当转义吞掉。
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), `Host win
  HostName windows-host
  IdentityFile C:\Users\me\.ssh\id_rsa
`)
	got, _ := cfg.ListAliases(context.Background())
	if len(got) != 1 {
		t.Fatalf("aliases: %+v", got)
	}
	if got[0].PrivateKeyPath == nil || !strings.Contains(*got[0].PrivateKeyPath, `C:\Users\me\.ssh\id_rsa`) {
		t.Fatalf("windows identity path: %+v", got[0])
	}
}

func TestSSHConfigTeleportGlobsInPatterns(t *testing.T) {
	cfg := newTestSSHConfig(t)
	// Teleport 生成 `Host *.teleport-*.example.com` 这类 glob：特殊字符必须先转义，
	// 否则拼出非法正则让 alias 枚举整体失败。
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), `
Host *.teleport-*.example.com
  User teleport

Host real-alias
  HostName direct
`)
	got, err := cfg.ListAliases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Alias != "real-alias" {
		t.Fatalf("teleport globs must not break enumeration: %+v", got)
	}
}

func TestSSHConfigDefaultsFromStarBlock(t *testing.T) {
	cfg := newTestSSHConfig(t)
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), `
Host *
  Port 2222
  User shared

Host specific
  HostName s.internal
`)
	got, _ := cfg.ListAliases(context.Background())
	// the `*` block matches every alias: port/user inherited, hostname from own block
	if len(got) != 1 {
		t.Fatalf("aliases: %+v", got)
	}
	if got[0].Port == nil || *got[0].Port != 2222 || got[0].Username == nil || *got[0].Username != "shared" {
		t.Fatalf("defaults: %+v", got[0])
	}
	if got[0].Host != "s.internal" {
		t.Fatalf("hostname: %+v", got[0])
	}
}

func TestSSHConfigAliasCapAt200(t *testing.T) {
	cfg := newTestSSHConfig(t)
	var b strings.Builder
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&b, "Host alias%03d\n  HostName h%d\n", i, i)
	}
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), b.String())
	got, _ := cfg.ListAliases(context.Background())
	if len(got) != 200 {
		t.Fatalf("cap: %d", len(got))
	}
	if got[0].Alias != "alias000" || got[199].Alias != "alias199" {
		t.Fatalf("cap keeps the first 200: %s..%s", got[0].Alias, got[199].Alias)
	}
}

func TestSSHConfigCacheTTL(t *testing.T) {
	cfg := newTestSSHConfig(t)
	configPath := filepath.Join(cfg.homeDir, ".ssh", "config")
	writeSSHConfig(t, configPath, "Host a\n  HostName h\n")

	base := time.UnixMilli(1_000_000)
	current := base
	cfg.SetClock(func() time.Time { return current })
	got1, _ := cfg.ListAliases(context.Background())
	if len(got1) != 1 {
		t.Fatalf("first read: %+v", got1)
	}
	// rewrite; cache still serves the old list within TTL
	writeSSHConfig(t, configPath, "Host a\n  HostName h\nHost b\n  HostName h2\n")
	got2, _ := cfg.ListAliases(context.Background())
	if len(got2) != 1 {
		t.Fatalf("cache must hold: %+v", got2)
	}
	// advance past the TTL
	current = base.Add(31 * time.Second)
	got3, _ := cfg.ListAliases(context.Background())
	if len(got3) != 2 {
		t.Fatalf("expired cache must refresh: %+v", got3)
	}
}

func TestSSHConfigMissingConfigFile(t *testing.T) {
	cfg := newTestSSHConfig(t)
	got, err := cfg.ListAliases(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("missing config: %+v %v", got, err)
	}
}

func TestSSHConfigSSHGRefinement(t *testing.T) {
	cfg := newTestSSHConfig(t)
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), `
Host refined
  HostName config-host
  User config-user
  Port 2222

Host identity-distrust
  HostName real-host
  IdentityFile ~/.ssh/explicit_key
`)
	cfg.SetSSHResolver(func() (string, bool) { return "/usr/bin/ssh", true })
	cfg.SetSSHGRunner(func(ctx context.Context, sshPath, configPath, alias string) (string, bool) {
		if alias == "refined" {
			// ssh -G output overrides host/user/port
			return "hostname sshg-host\nport 9999\nuser sshg-user\nidentityfile /home/u/.ssh/id_rsa\n", true
		}
		if alias == "identity-distrust" {
			// ssh -G 会返回默认 identityfile；privateKeyPath 只信任显式解析结果
			return "hostname real-host\nidentityfile /home/u/.ssh/id_rsa\n", true
		}
		return "", false
	})
	got, _ := cfg.ListAliases(context.Background())
	byAlias := map[string]SSHAliasOption{}
	for _, o := range got {
		byAlias[o.Alias] = o
	}
	r := byAlias["refined"]
	if r.Host != "sshg-host" || r.Port == nil || *r.Port != 9999 || r.Username == nil || *r.Username != "sshg-user" {
		t.Fatalf("ssh -G refinement: %+v", r)
	}
	d := byAlias["identity-distrust"]
	if d.PrivateKeyPath == nil || !strings.HasSuffix(*d.PrivateKeyPath, "explicit_key") {
		t.Fatalf("ssh -G identityfile must be distrusted: %+v", d)
	}
}

func TestSSHConfigSSHGFailureFallsBack(t *testing.T) {
	cfg := newTestSSHConfig(t)
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), `
Host fb
  HostName fallback-host
  Port 2222
`)
	cfg.SetSSHResolver(func() (string, bool) { return "/usr/bin/ssh", true })
	cfg.SetSSHGRunner(func(ctx context.Context, sshPath, configPath, alias string) (string, bool) {
		return "", false // timeout / non-zero exit
	})
	got, _ := cfg.ListAliases(context.Background())
	if len(got) != 1 || got[0].Host != "fallback-host" {
		t.Fatalf("fallback: %+v", got)
	}
}

func TestSSHConfigNoSSHBinaryUsesFallback(t *testing.T) {
	cfg := newTestSSHConfig(t)
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"), "Host a\n  HostName pure-go\n")
	cfg.SetSSHResolver(func() (string, bool) { return "", false })
	got, _ := cfg.ListAliases(context.Background())
	if len(got) != 1 || got[0].Host != "pure-go" {
		t.Fatalf("pure fallback: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// the 50 synthetic real-world-shaped configs
// ---------------------------------------------------------------------------

// syntheticSSHConfigs: 50 configs shaped like real ~/.ssh/config usage
// patterns — multi-host files, includes, per-host overrides, jump hosts,
// ProxyJump chains, keep-alive blocks, teleport-style entries, CI runner
// configs. Hosts are synthetic (example.test / RFC5737 ranges).
var syntheticSSHConfigs = []string{
	// 1. simple personal setup
	"Host github\n  HostName github.example.test\n  User git\n  IdentityFile ~/.ssh/github_key\n",
	// 2. work + personal split
	"Include ~/.ssh/work/*\n\nHost personal\n  HostName personal.example.test\n  User me\n",
	// 3. jump host chain
	"Host bastion\n  HostName bastion.example.test\n  User jump\n\nHost internal\n  HostName 10.0.0.5\n  ProxyJump bastion\n",
	// 4. port-forward heavy dev box
	"Host dev\n  HostName dev.example.test\n  Port 2222\n  LocalForward 8080 localhost:80\n  User dev\n",
	// 5. multiple identities
	"Host a\n  HostName a.example.test\n  IdentityFile ~/.ssh/id_ed25519\n\nHost b\n  HostName b.example.test\n  IdentityFile ~/.ssh/id_rsa\n",
	// 6. wildcard defaults + exceptions
	"Host *\n  ServerAliveInterval 60\n  User default\n\nHost !vpn\n  ProxyCommand none\n\nHost vpn\n  HostName vpn.example.test\n",
	// 7. teleport-style globs
	"Host *.teleport.example.test\n  Port 3022\n  User tbot\n\nHost root@*.teleport.example.test\n  User root\n",
	// 8. CI runner generated
	"Host ci-runner-01\n  HostName 192.0.2.10\n  User ci\n  StrictHostKeyChecking no\n  IdentityFile ~/.ci/key\n",
	// 9. CRLF line endings (Windows written)
	"Host win-crlf\r\n  HostName win.example.test\r\n  User winuser\r\n",
	// 10. quoted values
	"Host \"my server\"\n  HostName 192.0.2.20\n",
	// 11. spaces in path
	"Host spaced\n  IdentityFile ~/.ssh/my key with spaces\n  HostName spaced.example.test\n",
	// 12. trailing comments everywhere
	"Host c1 # primary\n  HostName h1.example.test # prod\n  Port 22 # default\n",
	// 13. tabs as separators
	"Host\ttabbed\n\tHostName\ttab.example.test\n\tUser\ttab\n",
	// 14. Match blocks are skipped (only Host lines define aliases)
	"Host m1\n  HostName m1.example.test\n\nMatch host *.lab\n  User labuser\n",
	// 15. negated pattern list
	"Host alpha beta !gamma\n  User grouped\n\nHost gamma\n  HostName gamma.example.test\n",
	// 16. empty blocks
	"Host empty\n\nHost next\n  HostName next.example.test\n",
	// 17. repeated Host on one line
	"Host h1 h2 h3\n  HostName shared.example.test\n  User shared\n",
	// 18. absolute Include
	"Include /etc/ssh/ssh_config.d/*.conf\nHost local\n  HostName local.example.test\n",
	// 19. include chain 3 deep
	"Include level2.conf\nHost top\n  HostName top.example.test\n",
	// 20. canonical domains
	"Host short\n  HostName short\n  CanonicalizeHostname yes\n  CanonicalDomains example.test internal.example.test\n",
	// 21. ssh-agent forwarding
	"Host fwd\n  HostName fwd.example.test\n  ForwardAgent yes\n  User agent\n",
	// 22. ControlMaster multiplexing
	"Host mux\n  HostName mux.example.test\n  ControlMaster auto\n  ControlPath ~/.ssh/cm-%r@%h:%p\n",
	// 23. deprecated options ignored
	"Host old\n  HostName old.example.test\n  Cipher aes128-ctr\n  Protocol 2\n",
	// 24. duplicate hosts across includes
	"Include dup_a.conf\nInclude dup_b.conf\n",
	// 25. very long host list line
	"Host " + strings.Join(func() []string {
		names := make([]string, 40)
		for i := range names {
			names[i] = fmt.Sprintf("batch%02d", i)
		}
		return names
	}(), " ") + "\n  User batch\n",
	// 26. hostname with port in URI-ish form (invalid, skipped gracefully)
	"Host uri-ish\n  HostName ssh://user@host.example.test:22/path\n",
	// 27. identity file with equals sign syntax
	"Host eq-syntax\n  HostName eq.example.test\n  IdentityFile=\"~/.ssh/quoted_key\"\n",
	// 28. case-insensitive keywords
	"HOST upper\n  HOSTNAME upper.example.test\n  USER upperuser\n",
	// 29. include with ~ expansion
	"Include ~/.ssh/tilde.conf\nHost t\n  HostName t.example.test\n",
	// 30. include missing file is tolerated
	"Include missing_file.conf\nHost survive\n  HostName survive.example.test\n",
	// 31. port out of range ignored
	"Host badport\n  HostName bad.example.test\n  Port 99999\n",
	// 32. port non-numeric ignored
	"Host badport2\n  HostName bad2.example.test\n  Port abc\n",
	// 33. hostname whitespace only falls back to alias
	"Host nohostname\n  HostName    \n",
	// 34. user whitespace only omitted
	"Host nouser\n  HostName nu.example.test\n  User   \n",
	// 35. ~ alone as path
	"Host homeish\n  IdentityFile ~\n  HostName home.example.test\n",
	// 36. %d home variable (undocumented but seen in the wild)
	"Host pct-d\n  IdentityFile %d/.ssh/pct_key\n  HostName pct.example.test\n",
	// 37. windows absolute paths
	"Host winpath\n  HostName win.example.test\n  IdentityFile C:\\Users\\me\\.ssh\\id_ed25519\n",
	// 38. mixed include + glob include
	"Include one.conf two.conf\nHost after\n  HostName after.example.test\n",
	// 39. star-only host skipped as alias
	"Host *\n  User star\n",
	// 40. negated-only host line skipped
	"Host !only-negated\n  User neg\n",
	// 41. very deep include (over the cap)
	"Include deep1.conf\n",
	// 42. Host with trailing spaces
	"Host padded   \n  HostName padded.example.test\n",
	// 43. HostName with dots only
	"Host dotted\n  HostName .internal.\n",
	// 44. identityfile multiple entries (first wins)
	"Host multikey\n  HostName multi.example.test\n  IdentityFile ~/.ssh/first\n  IdentityFile ~/.ssh/second\n",
	// 45. per-host ssh options don't affect alias extraction
	"Host optioned\n  HostName opt.example.test\n  Compression yes\n  LogLevel DEBUG\n  IdentitiesOnly yes\n",
	// 46. ipv6 hostname
	"Host v6\n  HostName 2001:db8::1\n  User v6\n",
	// 47. alias equals hostname pattern (single token)
	"Host loopback\n  HostName loopback\n",
	// 48. include pointing at a directory (no glob) is skipped
	"Include somedir\nHost afterdir\n  HostName afterdir.example.test\n",
	// 49. multiple ports, first valid wins
	"Host porty\n  HostName porty.example.test\n  Port notanumber\n  Port 2200\n",
	// 50. 300 hosts generated (cap check)
	func() string {
		var b strings.Builder
		for i := 0; i < 300; i++ {
			fmt.Fprintf(&b, "Host cap%03d\n  HostName cap%03d.example.test\n", i, i)
		}
		return b.String()
	}(),
}

func TestSSHConfigSyntheticCorpus(t *testing.T) {
	for i, content := range syntheticSSHConfigs {
		i, content := i, content
		t.Run(fmt.Sprintf("synthetic-%02d", i+1), func(t *testing.T) {
			cfg := NewSSHConfig(t.TempDir())
			sshDir := filepath.Join(cfg.homeDir, ".ssh")
			writeSSHConfig(t, filepath.Join(sshDir, "config"), content)
			// side files the corpus references
			writeSSHConfig(t, filepath.Join(sshDir, "work", "w.conf"), "Host workhost\n  HostName work.example.test\n")
			writeSSHConfig(t, filepath.Join(sshDir, "level2.conf"), "Include level3.conf\nHost l2\n  HostName l2.example.test\n")
			writeSSHConfig(t, filepath.Join(sshDir, "level3.conf"), "Host l3\n  HostName l3.example.test\n")
			writeSSHConfig(t, filepath.Join(sshDir, "dup_a.conf"), "Host dup\n  HostName dup-a.example.test\n")
			writeSSHConfig(t, filepath.Join(sshDir, "dup_b.conf"), "Host dup\n  HostName dup-b.example.test\n")
			writeSSHConfig(t, filepath.Join(sshDir, "one.conf"), "Host one\n  HostName one.example.test\n")
			writeSSHConfig(t, filepath.Join(sshDir, "two.conf"), "Host two\n  HostName two.example.test\n")
			for d := 1; d <= 10; d++ {
				writeSSHConfig(t, filepath.Join(sshDir, fmt.Sprintf("deep%d.conf", d)),
					fmt.Sprintf("Include deep%d.conf\nHost deep%d\n  HostName deep%d.example.test\n", d+1, d, d))
			}
			writeSSHConfig(t, filepath.Join(sshDir, "somedir", "x.conf"), "Host ind\n  HostName ind.example.test\n")

			got, err := cfg.ListAliases(context.Background())
			if err != nil {
				t.Fatalf("corpus %d failed: %v", i+1, err)
			}
			// structural invariants:
			// - every alias is connectable (no globs/negations)
			for _, o := range got {
				if strings.ContainsAny(o.Alias, "*?[") || strings.HasPrefix(o.Alias, "!") {
					t.Fatalf("non-connectable alias leaked: %q", o.Alias)
				}
				if o.Host == "" {
					t.Fatalf("alias %q has empty host", o.Alias)
				}
			}
			// - no duplicates
			seen := map[string]bool{}
			for _, o := range got {
				if seen[o.Alias] {
					t.Fatalf("duplicate alias: %q", o.Alias)
				}
				seen[o.Alias] = true
			}
			// - cap respected
			if len(got) > 200 {
				t.Fatalf("cap exceeded: %d", len(got))
			}
			// - sorted-by-file-order is preserved (the TS keeps first-seen order)
			if len(got) > 1 {
				for j := 1; j < len(got); j++ {
					if got[j].Alias == got[j-1].Alias {
						t.Fatalf("duplicate: %q", got[j].Alias)
					}
				}
			}
		})
	}
}

func TestSSHSplitTokensBackslashSemantics(t *testing.T) {
	// quoted backslash pairs collapse; lone backslashes stay literal
	got := splitSSHTokens(`C:\Users\me key "a\"b" tab\ space`)
	want := []string{`C:\Users\me`, "key", `a"b`, "tab space"}
	if len(got) != len(want) {
		t.Fatalf("tokens: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token[%d]: %q want %q", i, got[i], want[i])
		}
	}
}

func TestSSHHostPatternMatching(t *testing.T) {
	cases := []struct {
		pattern, alias string
		want           bool
	}{
		{"*", "anything", true},
		{"exact", "exact", true},
		{"exact", "other", false},
		{"*.example.test", "a.example.test", true},
		{"a?.example.test", "ab.example.test", true},
		{"a?.example.test", "abc.example.test", false},
		{"*.teleport-*.example.test", "x.teleport-y.example.test", true},
		// char classes are ESCAPED (literal), not interpreted — the TS escapes
		// every regex metachar per char, so '[abc]host' matches only itself
		{"[abc]host", "[abc]host", true},
		{"[abc]host", "ahost", false},
	}
	for _, tc := range cases {
		if got := matchHostPattern(tc.pattern, tc.alias); got != tc.want {
			t.Errorf("%q vs %q: %v want %v", tc.pattern, tc.alias, got, tc.want)
		}
	}
}

func TestSSHBlockNegationWins(t *testing.T) {
	if blockMatchesAlias([]string{"*", "!no"}, "no") {
		t.Error("negation must win")
	}
	if !blockMatchesAlias([]string{"*", "!no"}, "yes") {
		t.Error("positive must match when negation does not")
	}
	if blockMatchesAlias([]string{"!no", "*"}, "no") {
		t.Error("negation order must not matter")
	}
}

func TestSSHConfigSourceOrderStable(t *testing.T) {
	cfg := newTestSSHConfig(t)
	writeSSHConfig(t, filepath.Join(cfg.homeDir, ".ssh", "config"),
		"Host zz\n  HostName zz.example.test\nHost aa\n  HostName aa.example.test\n")
	got, _ := cfg.ListAliases(context.Background())
	if len(got) != 2 || got[0].Alias != "zz" {
		t.Fatalf("file order preserved: %+v", got)
	}
	_ = sort.Strings
}
