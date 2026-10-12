package runtime

import (
	"testing"

	"github.com/tylinux/nex/go/internal/modelreq"
)

func TestJSStringifyDoesNotEscapeHTMLOrLineSeparators(t *testing.T) {
	got := string(jsStringify(map[string]any{"a": "<b>&\u2028\u2029"}))
	want := "{\"a\":\"<b>&\u2028\u2029\"}"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestContextUsageMessageCountsAngleBracketsAsOneChar(t *testing.T) {
	m := contextUsageMessage(modelreq.Message{Role: "tool", Content: "<x>"})
	if string(m.Content) != `"<x>"` {
		t.Fatalf("content = %s", m.Content)
	}
}
