package system

// sshconfig.go ports packages/services/src/system/sshConfigAlias.ts (708
// lines) — the ~/.ssh/config alias enumeration used by the remote-workspace
// picker: Host-pattern parsing with Include (glob, depth-capped), alias
// extraction (single non-negatable glob-free pattern), a pure-Go fallback for
// host/port/user/identityfile, and an optional `ssh -G` refinement that never
// trusts its identityfile output. The TS module cache (30s TTL) is an
// instance cache here.
//
// The 50 synthetic real-world-shaped configs live in sshconfig_test.go; none
// reference real hosts.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/probeio"
)

const (
	sshCacheTTL            = 30 * time.Second
	sshMaxAliasCount       = 200
	sshGConcurrency        = 3
	sshMaxIncludeDepth     = 8
	sshConfigRelativeParts = 2
)

// SSHAliasOption is SSHConfigAliasOption.
type SSHAliasOption struct {
	Alias          string  `json:"alias"`
	Host           string  `json:"host"`
	Port           *int    `json:"port,omitempty"`
	Username       *string `json:"username,omitempty"`
	PrivateKeyPath *string `json:"privateKeyPath,omitempty"`
	Source         string  `json:"source"`
}

type parsedDirective struct {
	key   string
	value string
}

type parsedBlock struct {
	patterns          []string
	directives        []parsedDirective
	source            string
	fromHostDirective bool
}

type aliasMeta struct {
	alias  string
	source string
}

// sshConfigCache is the per-instance alias cache (aliasCache in the TS).
type sshConfigCache struct {
	mu      sync.Mutex
	expires time.Time
	options []SSHAliasOption
}

// SSHConfig is the resolver. The zero value works; NewSSHConfig takes the
// home dir and an optional ssh binary path resolver for tests.
type SSHConfig struct {
	homeDir    string
	now        func() time.Time
	resolveSSH func() (string, bool)
	runSSHG    func(ctx context.Context, sshPath, configPath, alias string) (string, bool)
	cache      sshConfigCache
}

// NewSSHConfig builds the resolver. homeDir may be "" (uses os.UserHomeDir).
func NewSSHConfig(homeDir string) *SSHConfig {
	s := &SSHConfig{homeDir: homeDir, now: time.Now}
	s.resolveSSH = s.defaultResolveSSHExecutable
	s.runSSHG = s.defaultRunSSHConfigQuery
	return s
}

// SetClock pins the cache TTL checks in tests.
func (s *SSHConfig) SetClock(now func() time.Time) { s.now = now }

// SetSSHResolver overrides the ssh binary discovery (tests).
func (s *SSHConfig) SetSSHResolver(resolve func() (string, bool)) { s.resolveSSH = resolve }

// SetSSHGRunner overrides the `ssh -G` execution (tests).
func (s *SSHConfig) SetSSHGRunner(run func(ctx context.Context, sshPath, configPath, alias string) (string, bool)) {
	s.runSSHG = run
}

// ListAliases ports listSSHConfigAliasesFromLocalConfig: cached, parses
// ~/.ssh/config, falls back to pure-Go resolution when `ssh` is unavailable
// or its query fails.
func (s *SSHConfig) ListAliases(ctx context.Context) ([]SSHAliasOption, error) {
	s.cache.mu.Lock()
	if !s.cache.expires.IsZero() && s.cache.expires.After(s.now()) {
		options := s.cache.options
		s.cache.mu.Unlock()
		return cloneAliases(options), nil
	}
	s.cache.mu.Unlock()

	rootConfigPath := filepath.Join(s.homeDirOrDefault(), ".ssh", "config")
	if _, err := os.Stat(rootConfigPath); err != nil {
		s.setCache(nil)
		return []SSHAliasOption{}, nil
	}
	blocks, err := parseSSHConfigBlocks(rootConfigPath, map[string]bool{}, 0)
	if err != nil {
		return nil, err
	}
	aliasMetas := buildAliasMetas(blocks)
	if len(aliasMetas) == 0 {
		s.setCache(nil)
		return []SSHAliasOption{}, nil
	}
	fallbackOptions := buildFallbackOptions(blocks, aliasMetas)
	fallbackByAlias := map[string]SSHAliasOption{}
	for _, o := range fallbackOptions {
		fallbackByAlias[o.Alias] = o
	}

	sshPath, ok := s.resolveSSH()
	var options []SSHAliasOption
	if !ok {
		options = fallbackOptions
	} else {
		options = s.refineWithSSHG(ctx, sshPath, rootConfigPath, aliasMetas, fallbackByAlias)
	}
	s.setCache(options)
	return cloneAliases(options), nil
}

func (s *SSHConfig) refineWithSSHG(ctx context.Context, sshPath, configPath string, metas []aliasMeta, fallback map[string]SSHAliasOption) []SSHAliasOption {
	options := make([]SSHAliasOption, len(metas))
	sem := make(chan struct{}, sshGConcurrency)
	var wg sync.WaitGroup
	for i, meta := range metas {
		wg.Add(1)
		go func(i int, meta aliasMeta) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fallbackOption, ok := fallback[meta.alias]
			if !ok {
				fallbackOption = SSHAliasOption{Alias: meta.alias, Host: meta.alias, Source: meta.source}
			}
			output, ok := s.runSSHG(ctx, sshPath, configPath, meta.alias)
			if !ok {
				options[i] = fallbackOption
				return
			}
			parsed := parseSSHGOutput(output)
			option := SSHAliasOption{
				Alias:  meta.alias,
				Source: meta.source,
			}
			if parsed.host != "" {
				option.Host = parsed.host
			} else if fallbackOption.Host != "" {
				option.Host = fallbackOption.Host
			} else {
				option.Host = meta.alias
			}
			option.Port = firstNonNilInt(parsed.port, fallbackOption.Port)
			option.Username = firstNonNilString(parsed.username, fallbackOption.Username)
			// `ssh -G` 会返回默认 identityfile（例如 ~/.ssh/id_rsa），
			// 即使 alias 并未显式配置 IdentityFile。privateKeyPath 只信任
			// ssh config 显式解析结果。
			option.PrivateKeyPath = fallbackOption.PrivateKeyPath
			options[i] = option
		}(i, meta)
	}
	wg.Wait()
	return options
}

func firstNonNilInt(values ...*int) *int {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func firstNonNilString(values ...*string) *string {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func (s *SSHConfig) homeDirOrDefault() string {
	if s.homeDir != "" {
		return s.homeDir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func (s *SSHConfig) setCache(options []SSHAliasOption) {
	s.cache.mu.Lock()
	s.cache.options = cloneAliases(options)
	s.cache.expires = s.now().Add(sshCacheTTL)
	s.cache.mu.Unlock()
}

func cloneAliases(options []SSHAliasOption) []SSHAliasOption {
	if options == nil {
		return []SSHAliasOption{}
	}
	out := make([]SSHAliasOption, len(options))
	copy(out, options)
	return out
}

// ---------------------------------------------------------------------------
// parsing
// ---------------------------------------------------------------------------

// buildAliasMetas ports buildAliasMetas: single-pattern connectable Host
// lines, first occurrence wins, capped at 200.
func buildAliasMetas(blocks []parsedBlock) []aliasMeta {
	var aliases []aliasMeta
	seen := map[string]bool{}
	for _, block := range blocks {
		if !block.fromHostDirective || len(block.patterns) != 1 {
			continue
		}
		alias := strings.TrimSpace(block.patterns[0])
		if alias == "" || !isConnectableAliasPattern(alias) || seen[alias] {
			continue
		}
		seen[alias] = true
		aliases = append(aliases, aliasMeta{alias: alias, source: block.source})
		if len(aliases) >= sshMaxAliasCount {
			break
		}
	}
	return aliases
}

// defaultResolveSSHExecutable ports resolveSshExecutablePath.
func (s *SSHConfig) defaultResolveSSHExecutable() (string, bool) {
	if runtimeGOOS != "windows" {
		return findExecutableFromPath("ssh")
	}
	if path, ok := findExecutableFromPath("ssh.exe"); ok {
		return path, true
	}
	windowsDir := envOr("WINDIR", `C:\Windows`)
	programFiles := envOr("ProgramFiles", `C:\Program Files`)
	programFilesX86 := envOr("ProgramFiles(x86)", `C:\Program Files (x86)`)
	candidates := []string{
		filepath.Join(windowsDir, "System32", "OpenSSH", "ssh.exe"),
		filepath.Join(programFiles, "OpenSSH", "ssh.exe"),
		filepath.Join(programFiles, "Git", "usr", "bin", "ssh.exe"),
		filepath.Join(programFilesX86, "Git", "usr", "bin", "ssh.exe"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func findExecutableFromPath(binaryName string) (string, bool) {
	rawPath := os.Getenv("PATH")
	if rawPath == "" {
		return "", false
	}
	for _, entry := range filepath.SplitList(rawPath) {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		fullPath := filepath.Join(trimmed, binaryName)
		if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
			return fullPath, true
		}
	}
	return "", false
}

// defaultRunSSHConfigQuery ports runSshConfigQuery — the subprocess call
// lives in the probeio adapter (os/exec is not importable from services).
func (s *SSHConfig) defaultRunSSHConfigQuery(ctx context.Context, sshExecutablePath, configPath, alias string) (string, bool) {
	return probeio.RunSSHConfigQuery(ctx, sshExecutablePath, configPath, alias)
}
