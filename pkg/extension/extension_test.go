package extension

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/protocol"
)

type stubUIBridge struct {
	hasUI       bool
	selectedEnv string
	statuses    map[string]string
	sentMsgs    []string
}

func (u *stubUIBridge) HasUI() bool { return u.hasUI }

func (u *stubUIBridge) Prompt(_ context.Context, req protocol.UIPromptRequestPayload) (protocol.UIResolvePayload, error) {
	if req.Kind == "select" {
		return protocol.UIResolvePayload{Value: u.selectedEnv}, nil
	}
	return protocol.UIResolvePayload{Confirmed: true}, nil
}

func (u *stubUIBridge) SetStatus(_ context.Context, key, badge string, _ any) {
	if u.statuses == nil {
		u.statuses = make(map[string]string)
	}
	u.statuses[key] = badge
}

func (u *stubUIBridge) SendUserMessage(text string) {
	u.sentMsgs = append(u.sentMsgs, text)
}

func TestTSPluginHooksSlashCommandCustomToolAndRemoteUI(t *testing.T) {
	ctx := context.Background()
	ui := &stubUIBridge{hasUI: true, selectedEnv: "production"}
	reg := NewRegistry(t.TempDir(), ui)

	pluginTS := `
	interface ToolEvent { toolName: string; input: Record<string, unknown>; output?: string; }

	export default function (nex: any) {
		nex.on("tool_call", (ev: ToolEvent) => {
			if (ev.toolName === "bash" && String(ev.input.command).includes("rm -rf /")) {
				return { block: true, reason: "blocked root wipe" };
			}
		});

		nex.on("tool_result", (ev: ToolEvent, ctx: any) => {
			if (ev.toolName === "edit") {
				ctx.ui.setStatus("lint", "checked");
				return { appendText: "\n[plugin: auto-lint passed]" };
			}
		});

		nex.registerCommand("deploy-check", {
			description: "Select environment and run preflight",
			handler: async (_args: string, ctx: any) => {
				const env = await ctx.ui.select("Select target", ["staging", "production"], "staging");
				if (env) {
					ctx.sendUserMessage("Preflight for " + env);
				}
			}
		});

		nex.registerTool("echo_plugin", {
			description: "Echo from plugin",
			inputSchema: { type: "object", properties: { msg: { type: "string" } } },
			execute: (args: any) => "plugin:" + args.msg
		});
	}
	`

	if err := reg.LoadTSPlugin("safety-and-deploy", pluginTS, ""); err != nil {
		t.Fatalf("LoadTSPlugin failed: %v", err)
	}

	// 1. Pre-tool hook blocks dangerous command
	dec, err := reg.OnToolCall(ctx, "bash", json.RawMessage(`{"command":"sudo rm -rf /"}`))
	if err != nil || !dec.Block || !strings.Contains(dec.Reason, "root wipe") {
		t.Fatalf("expected block decision, got %+v err=%v", dec, err)
	}

	// 2. Post-tool hook appends diagnostic and updates remote ctx.ui status badge
	mut, err := reg.OnToolResult(ctx, "edit", json.RawMessage(`{"file_path":"a.go"}`), tools.Result{Output: "Edited a.go"})
	if err != nil || !strings.Contains(mut.AppendText, "auto-lint passed") || ui.statuses["lint"] != "checked" {
		t.Fatalf("post-tool hook or ctx.ui.setStatus failed: mut=%+v statuses=%+v", mut, ui.statuses)
	}

	// 3. Slash command exercises remote ctx.ui.select -> sends user prompt
	if err := reg.ExecuteCommand(ctx, "/deploy-check", ""); err != nil {
		t.Fatalf("ExecuteCommand failed: %v", err)
	}
	if len(ui.sentMsgs) != 1 || ui.sentMsgs[0] != "Preflight for production" {
		t.Fatalf("unexpected sentMsgs: %+v", ui.sentMsgs)
	}

	// 4. Headless mode (ctx.ui.hasUI == false) returns default value without blocking!
	ui.hasUI = false
	ui.sentMsgs = nil
	if err := reg.ExecuteCommand(ctx, "deploy-check", ""); err != nil {
		t.Fatal(err)
	}
	if len(ui.sentMsgs) != 1 || ui.sentMsgs[0] != "Preflight for staging" {
		t.Fatalf("headless defaultValue fallback failed: %+v", ui.sentMsgs)
	}

	// 5. Custom plugin tool execution
	pTools := reg.Tools()
	if len(pTools) != 1 || pTools[0].Name() != "echo_plugin" {
		t.Fatalf("expected custom tool echo_plugin, got %+v", pTools)
	}
	tRes, _ := pTools[0].Execute(ctx, "", json.RawMessage(`{"msg":"ok"}`), nil)
	if tRes.Output != "plugin:ok" {
		t.Fatalf("unexpected custom tool output: %q", tRes.Output)
	}
	if !reg.HasCommand("deploy-check") || !reg.HasCommand("/deploy-check") || reg.HasCommand("unknown") {
		t.Fatalf("HasCommand returned unexpected results")
	}

	// 6. SystemPrompt hook (before_agent_start) appends dynamic meta_context section
	sysPlugin := `
	export default function (nex: any) {
		nex.on("before_agent_start", (ev: any) => {
			if (ev.systemPrompt.includes("BasePrompt")) {
				return { appendSystemPrompt: "Dynamic policy from TS plugin" };
			}
		});
	}
	`
	if err := reg.LoadTSPlugin("sys-hook", sysPlugin, ""); err != nil {
		t.Fatalf("LoadTSPlugin sys-hook failed: %v", err)
	}
	outSecs, err := reg.OnSystemPrompt(ctx, []ai.SystemSection{{Category: "system_prompt", Content: "BasePrompt"}})
	if err != nil || len(outSecs) != 2 || !strings.Contains(outSecs[1].Content, "Dynamic policy from TS plugin") {
		t.Fatalf("unexpected OnSystemPrompt result: %+v err=%v", outSecs, err)
	}
}
