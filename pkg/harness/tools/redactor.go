package tools

import (
	"context"
	"encoding/base64"
	"net/url"
	"sort"
	"strings"
)

const (
	minRedactedSecretLen = 6
	maxStreamHoldback    = 256
)

type secretPattern struct {
	needle      string
	replacement string
}

// SecretRedactor scrubs injected secret values (raw, URL-encoded, and Base64 variants)
// from tool stdout/stderr, streaming deltas, and spilled log files.
type SecretRedactor struct {
	patterns []secretPattern
	maxLen   int
}

// NewSecretRedactor constructs a SecretRedactor from a map of envKey -> plaintextValue.
func NewSecretRedactor(secrets map[string]string) *SecretRedactor {
	if len(secrets) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var pats []secretPattern
	add := func(needle, key string) {
		if len(needle) < minRedactedSecretLen || seen[needle] {
			return
		}
		seen[needle] = true
		pats = append(pats, secretPattern{
			needle:      needle,
			replacement: "***[REDACTED:" + key + "]***",
		})
	}
	for k, v := range secrets {
		if len(v) < minRedactedSecretLen {
			continue
		}
		add(v, k)
		if esc := url.QueryEscape(v); esc != v {
			add(esc, k)
		}
		if b64 := base64.StdEncoding.EncodeToString([]byte(v)); len(b64) >= minRedactedSecretLen {
			add(b64, k)
		}
	}
	if len(pats) == 0 {
		return nil
	}
	// Replace longest secrets first to prevent partial prefix masking
	sort.Slice(pats, func(i, j int) bool {
		return len(pats[i].needle) > len(pats[j].needle)
	})
	maxLen := len(pats[0].needle)
	if maxLen > maxStreamHoldback {
		maxLen = maxStreamHoldback
	}
	return &SecretRedactor{
		patterns: pats,
		maxLen:   maxLen,
	}
}

// Redact replaces all registered secret values in s.
func (r *SecretRedactor) Redact(s string) string {
	if r == nil || len(r.patterns) == 0 || s == "" {
		return s
	}
	out := s
	for _, p := range r.patterns {
		if strings.Contains(out, p.needle) {
			out = strings.ReplaceAll(out, p.needle, p.replacement)
		}
	}
	return out
}

// StreamFilter buffers a small sliding tail so secrets split across pipe Read chunks
// are still redacted cleanly before emitting live onUpdate deltas.
type StreamFilter struct {
	redactor *SecretRedactor
	pending  string
}

func NewStreamFilter(r *SecretRedactor) *StreamFilter {
	return &StreamFilter{redactor: r}
}

// Push feeds a new chunk and returns the safe-to-emit redacted prefix.
func (f *StreamFilter) Push(chunk string) string {
	if f == nil || f.redactor == nil {
		return chunk
	}
	combined := f.redactor.Redact(f.pending + chunk)
	hold := f.redactor.maxLen - 1
	if hold <= 0 || len(combined) <= hold {
		f.pending = combined
		return ""
	}
	cut := len(combined) - hold
	emit := combined[:cut]
	f.pending = combined[cut:]
	return emit
}

// Flush returns any remaining buffered bytes after redaction when the stream closes.
func (f *StreamFilter) Flush() string {
	if f == nil || f.redactor == nil || f.pending == "" {
		return ""
	}
	out := f.redactor.Redact(f.pending)
	f.pending = ""
	return out
}

// mergeProcessEnv overlays extraEnv onto baseEnv, replacing existing keys cleanly.
func mergeProcessEnv(baseEnv []string, extraEnv map[string]string) []string {
	if len(extraEnv) == 0 {
		return baseEnv
	}
	out := make([]string, 0, len(baseEnv)+len(extraEnv))
	for _, kv := range baseEnv {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			if _, override := extraEnv[kv[:eq]]; override {
				continue
			}
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(extraEnv))
	for k := range extraEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+extraEnv[k])
	}
	return out
}

// RunShellWithEnv runs a one-shot shell command with extraEnv and redacts its combined output.
// Used by Runner credential probe RPCs.
func RunShellWithEnv(ctx context.Context, dir, command string, extraEnv map[string]string) (int, string, error) {
	redactor := NewSecretRedactor(extraEnv)
	collector := newBoundedCollector(maxInlineOutputBytes, nil, redactor)
	defer collector.Close()
	exitCode, err := runPlatformShellCommand(ctx, dir, command, collector, extraEnv)
	output, _, _ := collector.Finalize()
	return exitCode, output, err
}
