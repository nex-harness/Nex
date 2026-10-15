package bots

import (
	"strings"
	"testing"
)

// The golden strings below were produced by running the TypeScript
// replyFormatter.ts directly (scripts/go-rewrite/vectors/bots-reply.mts) and
// pasting its JSON output. They are the bot-visible text, so a diff here is a
// user-visible change, not a refactor.
//
// Regenerate with:  node --import tsx scripts/go-rewrite/vectors/bots-reply.mts

func TestFormatToolCallSummaryLineGolden(t *testing.T) {
	cases := []struct {
		name     string
		toolCall ToolCallState
		options  *FormatOptions
		want     string
	}{
		{
			name: "edit with change stat, workspace-relative path",
			toolCall: ToolCallState{
				ToolID: "t1", Kind: "edit", Title: "Edit file", Status: "completed",
				Input: map[string]any{
					"path":       "/repo/src/a.ts",
					"old_string": "a\nb",
					"new_string": "a\nc\nd",
				},
			},
			options: &FormatOptions{WorkspacePath: "/repo"},
			want:    "- 完成 · Edit file · `src/a.ts` · 🟢 `+3` 🔴 `-2`",
		},
		{
			name: "edit with change stat, zh locale same output",
			toolCall: ToolCallState{
				ToolID: "t1", Kind: "edit", Title: "Edit file", Status: "completed",
				Input: map[string]any{
					"path":       "/repo/src/a.ts",
					"old_string": "a\nb",
					"new_string": "a\nc\nd",
				},
			},
			options: &FormatOptions{WorkspacePath: "/repo", Locale: "zh-CN"},
			want:    "- 完成 · Edit file · `src/a.ts` · 🟢 `+3` 🔴 `-2`",
		},
		{
			name: "failed bash with command input, error collapsed",
			toolCall: ToolCallState{
				ToolID: "t2", Kind: "bash", Title: "Run", Status: "failed",
				Input: map[string]any{"command": "pnpm typecheck"}, Error: "  exit   code 2  ",
			},
			options: &FormatOptions{Locale: "en-US"},
			want:    "- Failed: exit code 2 · Run · `pnpm typecheck`",
		},
		{
			name:     "pending with no title falls back to tool",
			toolCall: ToolCallState{ToolID: "t3", Status: "pending"},
			want:     "- 等待中 · tool",
		},
		{
			name:     "in progress",
			toolCall: ToolCallState{ToolID: "t4", Kind: "read", Title: "Read", Status: "in_progress"},
			options:  &FormatOptions{Locale: "en-US"},
			want:     "- Running · Read",
		},
		{
			name:     "denied",
			toolCall: ToolCallState{ToolID: "t5", Status: "denied"},
			options:  &FormatOptions{Locale: "en-US"},
			want:     "- Denied · tool",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := FormatToolCallSummaryLine(testCase.toolCall, testCase.options)
			if got != testCase.want {
				t.Errorf("FormatToolCallSummaryLine()\n got: %q\nwant: %q", got, testCase.want)
			}
		})
	}
}

func TestFormatToolCallReplyGolden(t *testing.T) {
	got := FormatToolCallReply(ToolCallState{
		ToolID: "t1", Kind: "edit", Title: "Edit file", Status: "completed",
		Input: map[string]any{"path": "/repo/src/a.ts", "old_string": "a", "new_string": "a\nb"},
	}, &FormatOptions{Locale: "en-US"})
	want := "Tool calls:\n- Completed · Edit file · `/repo/src/a.ts` · 🟢 `+2` 🔴 `-1`"
	if got != want {
		t.Errorf("FormatToolCallReply()\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatPermissionRequestSummaryGolden(t *testing.T) {
	cases := []struct {
		name    string
		request PermissionRequest
		options *FormatOptions
		want    string
	}{
		{
			name: "command with args",
			request: PermissionRequest{
				Title: "Run command", Kind: "exec",
				Raw: map[string]any{"input": map[string]any{
					"command": "rm -rf /tmp/x", "args": []any{"-rf", "/tmp/x"},
				}},
			},
			options: &FormatOptions{Locale: "en-US"},
			// The command string is command + joined args — a quirk of
			// getCommandFromRecord that the port reproduces.
			want: "Permission required:\nRun command\n`rm -rf /tmp/x -rf /tmp/x`",
		},
		{
			name: "edit update renders Editing + relative path",
			request: PermissionRequest{
				Title: "Edit src/a.ts", Kind: "edit",
				Raw: map[string]any{"changes": map[string]any{
					"/repo/src/a.ts": map[string]any{"type": "update"},
				}},
			},
			options: &FormatOptions{WorkspacePath: "/repo", Locale: "en-US"},
			want:    "Permission required:\nEditing src/a.ts\n`src/a.ts`",
		},
		{
			name: "edit add renders Writing",
			request: PermissionRequest{
				Title: "Write src/b.ts", Kind: "edit",
				Raw: map[string]any{"changes": map[string]any{
					"/repo/src/b.ts": map[string]any{"type": "add"},
				}},
			},
			options: &FormatOptions{WorkspacePath: "/repo", Locale: "en-US"},
			want:    "Permission required:\nWriting src/b.ts\n`src/b.ts`",
		},
		{
			name:    "generic permission",
			request: PermissionRequest{Title: "Something happened", Kind: "other", Raw: map[string]any{}},
			options: &FormatOptions{Locale: "zh-CN"},
			want:    "需要权限：\nSomething happened",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := FormatPermissionRequestSummary(testCase.request, testCase.options)
			if got != testCase.want {
				t.Errorf("FormatPermissionRequestSummary()\n got: %q\nwant: %q", got, testCase.want)
			}
		})
	}
}

func TestExtractAssistantResponseMessagesGolden(t *testing.T) {
	messages, rest := ExtractAssistantResponseMessages(strings.Repeat("x", 4000), true)
	if len(messages) != 2 || len(messages[0]) != 3500 || len(messages[1]) != 500 {
		t.Fatalf("force chunking = %d messages with lengths %d/%d, want 2 with 3500/500",
			len(messages), len(messages[0]), len(messages[1]))
	}
	// Non-force keeps the buffer and normalises CRLF; the caller decides when to flush.
	messages, rest = ExtractAssistantResponseMessages("abc\r\ndef", false)
	if len(messages) != 0 || rest != "abc\ndef" {
		t.Errorf("non-force = %v, rest %q; want empty messages and rest %q", messages, rest, "abc\ndef")
	}
}

func TestFormatAssistantReplyBlocksGolden(t *testing.T) {
	got := FormatAssistantReplyBlocks([]AssistantReplyBlock{
		{Text: "hello"},
		{ToolCall: &ToolCallState{
			ToolID: "t1", Kind: "edit", Title: "Edit", Status: "completed",
			Input: map[string]any{"path": "/repo/a.ts", "old_string": "a", "new_string": "a\nb"},
		}},
		{ChangeSummary: &ChangeSummary{FileCount: 2, Files: []ChangeFileStat{
			{Path: "/repo/a.ts", Stat: ToolCallChangeStat{Added: 3, Removed: 1}},
			{Path: "/repo/b.ts", Stat: ToolCallChangeStat{Added: 2}},
		}}},
	}, &FormatOptions{Locale: "en-US", WorkspacePath: "/repo"})
	want := []string{
		"hello",
		"Tool calls:\n- Completed · Edit · `a.ts` · 🟢 `+2` 🔴 `-1`",
		// The header's aggregate diff count renders empty (matching the TS, which
		// reads summary-level added/removed that NexTaskChangeSummary does not carry).
		"Change summary: 2 files, \n- `/repo/a.ts` (🟢 `+3` 🔴 `-1`)\n- `/repo/b.ts` (🟢 `+2`)",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d blocks, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("block[%d]\n got: %q\nwant: %q", i, got[i], want[i])
		}
	}
}

func TestFormatAssistantReplyBlocksZhGolden(t *testing.T) {
	got := FormatAssistantReplyBlocks([]AssistantReplyBlock{
		{ChangeSummary: &ChangeSummary{FileCount: 1, Files: []ChangeFileStat{
			{Path: "/repo/a.ts", Stat: ToolCallChangeStat{Added: 1, Removed: 2}},
		}}},
	}, &FormatOptions{Locale: "zh-CN"})
	want := "变更摘要：1 个文件，\n- `/repo/a.ts` (🟢 `+1` 🔴 `-2`)"
	if len(got) != 1 || got[0] != want {
		t.Errorf("zh change summary\n got: %q\nwant: %q", got, want)
	}
}
