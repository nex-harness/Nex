// Package permission ports the agent's permission classification and the
// interaction/requestPermission payload: rule matching (core/src/permission/
// rule-matching.ts, service.ts) and the option projection
// (bootstrap/src/permission-options.ts). It is pure logic with no I/O.
package permission

import (
	"regexp"
	"strings"
)

// RuleValue mirrors PermissionRuleValue.
type RuleValue struct {
	ToolName    string `json:"toolName"`
	RuleContent string `json:"ruleContent,omitempty"`
}

// Ruleset mirrors PermissionRuleset (only the behavior lists are used here).
type Ruleset struct {
	Allow []RuleValue
	Deny  []RuleValue
	Ask   []RuleValue
}

// wildcardToRegExp mirrors wildcardToRegExp(): each `*` becomes `.*` and every
// other regex metacharacter is escaped, anchored at both ends.
func wildcardToRegExp(pattern string) *regexp.Regexp {
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

// matchesRuleToolName mirrors matchesRuleToolName(): Write also matches an Edit rule.
func matchesRuleToolName(ruleToolName, contextToolName string) bool {
	if ruleToolName == contextToolName {
		return true
	}
	return contextToolName == "Write" && ruleToolName == "Edit"
}

// ruleSubjects mirrors ruleSubjects(): the first present string field of the
// subject-key list, or the input itself when it is a string.
func ruleSubjects(input any, toolName string) []string {
	if s, ok := input.(string); ok {
		return []string{s}
	}
	record, ok := input.(map[string]any)
	if !ok {
		return nil
	}
	if toolName == "WebFetch" {
		if url, ok := record["url"].(string); ok {
			if domain := domainRuleSubject(url); domain != "" {
				return []string{domain}
			}
			return nil
		}
	}
	for _, key := range []string{"command", "url", "file_path", "path", "pattern", "patch_text"} {
		if v, ok := record[key].(string); ok {
			return []string{v}
		}
	}
	return nil
}

func domainRuleSubject(url string) string {
	trimmed := strings.TrimSpace(url)
	host := hostnameOf(trimmed)
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return ""
	}
	return "domain:" + host
}

func hostnameOf(rawURL string) string {
	// Minimal URL host extraction: scheme://host[:port]/path.
	idx := strings.Index(rawURL, "://")
	if idx < 0 {
		return ""
	}
	rest := rawURL[idx+3:]
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		rest = rest[at+1:]
	}
	for _, sep := range []byte{'/', '?', '#'} {
		if i := strings.IndexByte(rest, sep); i >= 0 {
			rest = rest[:i]
		}
	}
	if colon := strings.LastIndexByte(rest, ':'); colon >= 0 && !strings.Contains(rest[colon+1:], "]") {
		rest = rest[:colon]
	}
	return rest
}

// matchesRuleContent mirrors matchesRuleContent().
func matchesRuleContent(subject, ruleContent string) bool {
	if strings.HasSuffix(ruleContent, ":*") {
		prefix := ruleContent[:len(ruleContent)-2]
		return subject == prefix || strings.HasPrefix(subject, prefix+" ") || strings.HasPrefix(subject, prefix+"\t")
	}
	if strings.Contains(ruleContent, "*") {
		return wildcardToRegExp(ruleContent).MatchString(subject)
	}
	return subject == ruleContent
}

// MatchRule mirrors matchesRule(): scope match plus optional content match.
func MatchRule(rule RuleValue, toolName string, input any) bool {
	if !matchesRuleToolName(rule.ToolName, toolName) {
		return false
	}
	if rule.RuleContent == "" {
		return true
	}
	subjects := ruleSubjects(input, toolName)
	if len(subjects) == 0 {
		return false
	}
	for _, subject := range subjects {
		if matchesRuleContent(subject, rule.RuleContent) {
			return true
		}
	}
	return false
}

// MatchesRuleset mirrors matchesProjectRules(): any matching rule of the behavior
// list matches.
func MatchesRuleset(rules []RuleValue, toolName string, input any) bool {
	for _, rule := range rules {
		if MatchRule(rule, toolName, input) {
			return true
		}
	}
	return false
}
