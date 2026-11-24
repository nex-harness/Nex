package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestSecretRedactorAndStreamFilter(t *testing.T) {
	secret := "ghp_SuperSecretTokenValue9988"
	r := NewSecretRedactor(map[string]string{
		"GH_TOKEN": secret,
	})
	if r == nil {
		t.Fatal("expected non-nil redactor")
	}

	// Raw, URL-encoded, and Base64 variants
	b64 := base64.StdEncoding.EncodeToString([]byte(secret))
	esc := url.QueryEscape(secret)
	in := "raw=" + secret + " b64=" + b64 + " url=" + esc
	out := r.Redact(in)
	if strings.Contains(out, secret) || strings.Contains(out, b64) {
		t.Fatalf("secret leaked in redacted output: %q", out)
	}
	if !strings.Contains(out, "***[REDACTED:GH_TOKEN]***") {
		t.Fatalf("expected redacted placeholder, got %q", out)
	}

	// Split secret across multiple stream chunks
	sf := NewStreamFilter(r)
	var collected strings.Builder
	full := "prefix-" + secret + "-suffix"
	for i := 0; i < len(full); i += 4 {
		end := i + 4
		if end > len(full) {
			end = len(full)
		}
		collected.WriteString(sf.Push(full[i:end]))
	}
	collected.WriteString(sf.Flush())
	got := collected.String()
	if strings.Contains(got, secret) {
		t.Fatalf("secret leaked across chunk boundary: %q", got)
	}
	if got != "prefix-***[REDACTED:GH_TOKEN]***-suffix" {
		t.Fatalf("unexpected stream output: %q", got)
	}
}

func TestBashToolInjectsAndRedactsSecret(t *testing.T) {
	secret := "nex_test_secret_value_123456"
	bt := NewBashTool()
	bt.SetExtraEnv(map[string]string{
		"NEX_TEST_SECRET": secret,
	})

	args, _ := json.Marshal(map[string]any{
		"command": "echo value=$NEX_TEST_SECRET",
	})
	var streamed strings.Builder
	res, err := bt.Execute(context.Background(), t.TempDir(), args, func(delta string) {
		streamed.WriteString(delta)
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Output)
	}
	if strings.Contains(res.Output, secret) || strings.Contains(streamed.String(), secret) {
		t.Fatalf("secret leaked! output=%q streamed=%q", res.Output, streamed.String())
	}
	if !strings.Contains(res.Output, "***[REDACTED:NEX_TEST_SECRET]***") {
		t.Fatalf("expected injected+redacted output, got %q", res.Output)
	}
}
