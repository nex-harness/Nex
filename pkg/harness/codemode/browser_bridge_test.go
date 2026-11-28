package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCodemodeBrowserBatchAutomation(t *testing.T) {
	tool := NewTool(nil, NewSessionKV(), nil)
	var calls []string
	tool.SetBrowserCaller(func(ctx context.Context, action string, args map[string]any) (string, error) {
		calls = append(calls, action)
		switch action {
		case "open":
			return `{"ok":true,"url":"https://example.com"}`, nil
		case "snapshot":
			return `{"title":"Example","elements":[{"ref":"e1","role":"button","name":"Submit"}]}`, nil
		case "fill":
			return `{"ok":true,"ref":"e2"}`, nil
		case "click":
			return `{"ok":true,"ref":"e1"}`, nil
		case "eval":
			return `{"value":"done-42"}`, nil
		default:
			return `{"ok":true}`, nil
		}
	})

	code := `
const opened = await browser.open("https://example.com");
const snap = await browser.snapshot({ max_elements: 20 });
await browser.fill("e2", "hello");
await browser.click("e1");
const res = await browser.eval("document.title");
return { opened, title: snap.title, evalVal: res.value };
`
	rawArgs, _ := json.Marshal(map[string]any{"code": code})
	res, err := tool.Execute(context.Background(), "", rawArgs, nil)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "done-42") || !strings.Contains(res.Output, "Example") {
		t.Fatalf("unexpected output: %s", res.Output)
	}
	if len(calls) != 5 {
		t.Fatalf("expected 5 browser calls in single codemode turn, got %d: %v", len(calls), calls)
	}
}
