package bots

import "testing"

// TestParseCommandTable is the commandParser.ts table, case by case. Each row is
// one line from the TS switch (plus the splitCommand pre-conditions), so a drift
// in a single alias shows up as a single failing row instead of a behavioural
// complaint from a chat.
func TestParseCommandTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Command
	}{
		// splitCommand pre-conditions.
		{"plain text", "hello", Command{Type: CmdMessage, Text: "hello"}},
		{"plain text keeps spacing", "  hello world ", Command{Type: CmdMessage, Text: "  hello world "}},
		{"bare zero cancels", "0", Command{Type: CmdSelectionCancel}},
		{"bare zero with spaces cancels", "  0 ", Command{Type: CmdSelectionCancel}},
		{"zero with text is a message", "0 items", Command{Type: CmdMessage, Text: "0 items"}},
		{"lone slash is unknown", "/", Command{Type: CmdUnknown, Name: "", Raw: "/"}},

		// bind
		{"bind", "/bind ab12", Command{Type: CmdBind, Value: "ab12"}},
		{"bind uppercase", "/BIND ab12", Command{Type: CmdBind, Value: "ab12"}},
		{"bind without code", "/bind", Command{Type: CmdUnknown, Name: "bind", Raw: "/bind"}},

		// help
		{"help", "/help", Command{Type: CmdHelp}},
		{"help chinese", "/帮助", Command{Type: CmdHelp}},
		{"help ignores arguments", "/help me", Command{Type: CmdHelp}},

		// cancel
		{"cancel", "/cancel", Command{Type: CmdSelectionCancel}},
		{"cancel chinese", "/取消", Command{Type: CmdSelectionCancel}},

		// status
		{"status", "/status", Command{Type: CmdStatus}},
		{"status chinese", "/状态", Command{Type: CmdStatus}},

		// new
		{"new", "/new", Command{Type: CmdNew}},
		{"clear", "/clear", Command{Type: CmdNew}},
		{"new chinese", "/新建", Command{Type: CmdNew}},
		{"new task argument", "/new task", Command{Type: CmdNew}},

		// reconnect
		{"reconnect", "/reconnect", Command{Type: CmdReconnect}},
		{"reconnect chinese", "/重连", Command{Type: CmdReconnect}},

		// workspace
		{"workspace list", "/workspace", Command{Type: CmdWorkspaceList}},
		{"workspace set", "/workspace api", Command{Type: CmdWorkspaceSet, Value: "api"}},
		{"project alias", "/project", Command{Type: CmdWorkspaceList}},
		{"project chinese", "/项目", Command{Type: CmdWorkspaceList}},
		{"project chinese with value", "/项目 nex", Command{Type: CmdWorkspaceSet, Value: "nex"}},

		// model
		{"model list", "/model", Command{Type: CmdModelList}},
		{"model set", "/model gpt-4", Command{Type: CmdModelSet, Value: "gpt-4"}},
		{"model provider set", "/model provider openai", Command{Type: CmdModelProviderSet, Value: "openai"}},
		{"model model set", "/model model gpt-4", Command{Type: CmdModelSet, Value: "gpt-4"}},
		{"model chinese", "/模型", Command{Type: CmdModelList}},
		{"model chinese set", "/模型 claude", Command{Type: CmdModelSet, Value: "claude"}},

		// mode
		{"mode list", "/mode", Command{Type: CmdModeList}},
		{"mode set", "/mode yolo", Command{Type: CmdModeSet, Value: "yolo"}},
		{"mode chinese", "/模式", Command{Type: CmdModeList}},

		// thought level (five aliases)
		{"thoughtLevel list", "/thoughtLevel", Command{Type: CmdThoughtLevelList}},
		{"thought_level alias", "/thought_level", Command{Type: CmdThoughtLevelList}},
		{"thought-level alias", "/thought-level", Command{Type: CmdThoughtLevelList}},
		{"think alias", "/think", Command{Type: CmdThoughtLevelList}},
		{"chinese alias", "/思考", Command{Type: CmdThoughtLevelList}},
		{"thoughtLevel set", "/think high", Command{Type: CmdThoughtLevelSet, Value: "high"}},

		// task
		{"task list", "/task", Command{Type: CmdTaskList}},
		{"task set", "/task 42", Command{Type: CmdTaskSet, Value: "42"}},

		// reply
		{"reply list", "/reply", Command{Type: CmdReplyList}},
		{"reply set", "/reply summary", Command{Type: CmdReplySet, Value: "summary"}},
		{"reply chinese", "/回复", Command{Type: CmdReplyList}},

		// stop
		{"stop", "/stop", Command{Type: CmdStop}},
		{"stop chinese", "/停止", Command{Type: CmdStop}},

		// permission
		{"permission respond", "/permission yes", Command{Type: CmdPermissionRespond, Value: "yes"}},
		{"permission without value", "/permission", Command{Type: CmdUnknown, Name: "permission", Raw: "/permission"}},

		// elicitation
		{"elicitation without value", "/elicitation", Command{Type: CmdUnknown, Name: "elicitation", Raw: "/elicitation"}},
		{"elicitation respond", "/elicitation token1 opt1", Command{Type: CmdElicitationRespond, Value: "token1 opt1"}},
		{"elicitation submit", "/elicitation submit", Command{Type: CmdElicitationSubmit}},
		{"elicitation done", "/elicitation done", Command{Type: CmdElicitationSubmit}},
		{"elicitation chinese submit", "/elicitation 完成", Command{Type: CmdElicitationSubmit}},
		{"elicitation submit case insensitive", "/elicitation SUBMIT", Command{Type: CmdElicitationSubmit}},
		{"answer alias", "/answer yes", Command{Type: CmdElicitationRespond, Value: "yes"}},
		{"chinese answer alias", "/回答 yes", Command{Type: CmdElicitationRespond, Value: "yes"}},
		{"chinese answer without value", "/回答", Command{Type: CmdUnknown, Name: "回答", Raw: "/回答"}},

		// approve / deny
		{"approve with both ids", "/approve req-1 opt-1", Command{Type: CmdApprove, RequestID: "req-1", OptionID: "opt-1"}},
		{"approve with extra tokens ignores them", "/approve req-1 opt-1 extra", Command{Type: CmdApprove, RequestID: "req-1", OptionID: "opt-1"}},
		{"approve with only request id", "/approve req-1", Command{Type: CmdUnknown, Name: "approve", Raw: "/approve req-1"}},
		{"approve without value", "/approve", Command{Type: CmdUnknown, Name: "approve", Raw: "/approve"}},
		{"deny", "/deny req-1", Command{Type: CmdDeny, RequestID: "req-1"}},
		{"deny without value", "/deny", Command{Type: CmdUnknown, Name: "deny", Raw: "/deny"}},

		// unknown
		{"unknown command", "/nope", Command{Type: CmdUnknown, Name: "nope", Raw: "/nope"}},
		{"unknown command with args", "/nope a b", Command{Type: CmdUnknown, Name: "nope", Raw: "/nope a b"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ParseCommand(testCase.in)
			if got != testCase.want {
				t.Errorf("ParseCommand(%q) = %+v, want %+v", testCase.in, got, testCase.want)
			}
		})
	}
}

// TestBotMenuCommandOrder pins the menu order: help, then the policy order,
// then bind. The order is what a Telegram menu renders, so a reordering is a
// user-visible change.
func TestBotMenuCommandOrder(t *testing.T) {
	want := []string{"help", "status", "new", "workspace", "model", "mode", "thoughtLevel", "reply", "bind"}
	if len(BotMenuCommandOrder) != len(want) {
		t.Fatalf("BotMenuCommandOrder has %d entries, want %d", len(BotMenuCommandOrder), len(want))
	}
	for i, key := range want {
		if BotMenuCommandOrder[i] != key {
			t.Errorf("BotMenuCommandOrder[%d] = %q, want %q", i, BotMenuCommandOrder[i], key)
		}
	}
}

// TestIsUserCommandAllowed covers the unconditional set, the workspace-derived
// reconnect and a disabled flag.
func TestIsUserCommandAllowed(t *testing.T) {
	bot := Config{AllowedCommands: DefaultAllowedCommands()}
	for _, command := range []string{"help", "message", "approve", "task", "stop"} {
		if !IsUserCommandAllowed(bot, command) {
			t.Errorf("IsUserCommandAllowed(%q) = false, want true (always allowed)", command)
		}
	}
	if !IsUserCommandAllowed(bot, "reconnect") {
		t.Error("reconnect should follow the workspace flag, which is enabled here")
	}
	disabled := DefaultAllowedCommands()
	disabled.Workspace = false
	disabled.Status = false
	botWithDisabled := Config{AllowedCommands: disabled}
	if IsUserCommandAllowed(botWithDisabled, "reconnect") {
		t.Error("reconnect must be denied when the workspace flag is off")
	}
	if IsUserCommandAllowed(botWithDisabled, "status") {
		t.Error("status must be denied when its flag is off")
	}
	if !IsUserCommandAllowed(botWithDisabled, "new") {
		t.Error("new must stay allowed")
	}
}
