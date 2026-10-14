package system

// sshmatch.go holds the alias matching and option building of the
// sshConfigAlias.ts port: Host-pattern glob matching with negation,
// first-match directive resolution, and the `ssh -G` output parser.

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/tylinux/nex/go/internal/adapters/probeio"
)

// matchHostPattern ports matchHostPattern: * and ? wildcards; regex metachars
// escaped (Teleport generates `Host *.teleport-*.example.com`).
var sshHostRegexMeta = regexp.MustCompile(`[\\^$.*+?()\[\]{}|]`)

func matchHostPattern(pattern, alias string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == alias
	}
	var body strings.Builder
	for _, r := range pattern {
		switch r {
		case '*':
			body.WriteString(".*")
		case '?':
			body.WriteString(".")
		default:
			body.WriteString(sshHostRegexMeta.ReplaceAllString(string(r), `\$0`))
		}
	}
	matched, err := regexp.MatchString("^"+body.String()+"$", alias)
	if err != nil {
		return false
	}
	return matched
}

// blockMatchesAlias ports blockMatchesAlias: negation wins immediately.
func blockMatchesAlias(patterns []string, alias string) bool {
	matchedPositive := false
	for _, rawPattern := range patterns {
		pattern := strings.TrimSpace(rawPattern)
		if pattern == "" {
			continue
		}
		if strings.HasPrefix(pattern, "!") {
			negate := pattern[1:]
			if negate != "" && matchHostPattern(negate, alias) {
				return false
			}
			continue
		}
		if matchHostPattern(pattern, alias) {
			matchedPositive = true
		}
	}
	return matchedPositive
}

func toValidPort(rawPort string) *int {
	p, err := strconv.Atoi(strings.TrimSpace(rawPort))
	if err != nil || p <= 0 || p > 65535 {
		return nil
	}
	return &p
}

func normalizeIdentityPath(rawValue, home string) *string {
	expanded := strings.TrimSpace(expandHomeToken(rawValue, home))
	if expanded == "" {
		return nil
	}
	return &expanded
}

// buildFallbackOptions ports buildFallbackOptions: first-wins directives
// across matching blocks (SSH first-match semantics).
func buildFallbackOptions(blocks []parsedBlock, metas []aliasMeta) []SSHAliasOption {
	options := make([]SSHAliasOption, 0, len(metas))
	for _, meta := range metas {
		var (
			host, username, privateKeyPath *string
			port                           *int
		)
		for _, block := range blocks {
			if !blockMatchesAlias(block.patterns, meta.alias) {
				continue
			}
			home, _ := os.UserHomeDir()
			for _, d := range block.directives {
				switch d.key {
				case "hostname":
					if host == nil {
						if t := strings.TrimSpace(d.value); t != "" {
							host = &t
						}
					}
				case "port":
					if port == nil {
						port = toValidPort(d.value)
					}
				case "user":
					if username == nil {
						if t := strings.TrimSpace(d.value); t != "" {
							username = &t
						}
					}
				case "identityfile":
					if privateKeyPath == nil {
						privateKeyPath = normalizeIdentityPath(d.value, home)
					}
				}
			}
		}
		option := SSHAliasOption{Alias: meta.alias, Source: meta.source}
		if host != nil {
			option.Host = *host
		} else {
			option.Host = meta.alias
		}
		option.Port = port
		option.Username = username
		option.PrivateKeyPath = privateKeyPath
		options = append(options, option)
	}
	return options
}

// parseSSHGOutput ports parseSshGOutput.
func parseSSHGOutput(output string) (result struct {
	host     string
	port     *int
	username *string
}) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), probeio.SSHGMaxOutput*2)
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if trimmed == "" {
			continue
		}
		var key, value string
		if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
			key, value = trimmed[:i], strings.TrimSpace(trimmed[i+1:])
		} else {
			continue
		}
		switch strings.ToLower(key) {
		case "hostname":
			if result.host == "" && value != "" {
				result.host = value
			}
		case "port":
			if result.port == nil {
				result.port = toValidPort(value)
			}
		case "user":
			if result.username == nil && value != "" {
				result.username = &value
			}
		}
	}
	return result
}
