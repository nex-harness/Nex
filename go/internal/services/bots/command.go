// command.go ports packages/services/src/bots/commandParser.ts and
// commandOrder.ts verbatim.
//
// The parser is the single place where a chat line becomes an intent, so it is
// kept a pure function: no config, no service, no locale. Its table is asserted
// case by case in command_test.go, including the Chinese aliases, because a
// silent drift here changes what every third-party bot accepts.
package bots

import (
	"strings"
	"unicode"
)

// CommandType is the `type` discriminator of the BotCommand union.
type CommandType string

// The command types, in the order the TS union declares them.
const (
	CmdBind               CommandType = "bind"
	CmdHelp               CommandType = "help"
	CmdStatus             CommandType = "status"
	CmdNew                CommandType = "new"
	CmdReconnect          CommandType = "reconnect"
	CmdWorkspaceList      CommandType = "workspace.list"
	CmdWorkspaceSet       CommandType = "workspace.set"
	CmdModelList          CommandType = "model.list"
	CmdModelProviderSet   CommandType = "model.provider.set"
	CmdModelSet           CommandType = "model.set"
	CmdModeList           CommandType = "mode.list"
	CmdModeSet            CommandType = "mode.set"
	CmdThoughtLevelList   CommandType = "thoughtLevel.list"
	CmdThoughtLevelSet    CommandType = "thoughtLevel.set"
	CmdTaskList           CommandType = "task.list"
	CmdTaskSet            CommandType = "task.set"
	CmdReplyList          CommandType = "reply.list"
	CmdReplySet           CommandType = "reply.set"
	CmdStop               CommandType = "stop"
	CmdPermissionRespond  CommandType = "permission.respond"
	CmdElicitationRespond CommandType = "elicitation.respond"
	CmdElicitationSubmit  CommandType = "elicitation.submit"
	CmdApprove            CommandType = "approve"
	CmdDeny               CommandType = "deny"
	CmdUnknown            CommandType = "unknown"
	CmdSelectionCancel    CommandType = "selection.cancel"
	CmdMessage            CommandType = "message"
)

// Command is BotCommand. The value/requestId/optionId fields are only set for
// the types that carry them; the pointer shapes keep `unknown`'s zero-value
// fields out of the JSON encoding.
type Command struct {
	Type      CommandType
	Value     string
	RequestID string
	OptionID  string
	Name      string
	Raw       string
	Text      string
}

type parsedCommand struct {
	name string
	rest string
}

// splitCommand ports splitCommand: a leading "/" is required, the name is the
// first whitespace-delimited token lowercased, the rest is trimmed.
func splitCommand(text string) (parsedCommand, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return parsedCommand{}, false
	}
	body := trimmed[1:]
	// `body.search(/\s/u)` is the first whitespace index; Go's strings.Fields
	// would collapse runs, so the index is taken on the raw body and the rest is
	// trimmed afterwards, exactly like the TS.
	firstSpace := -1
	for i, r := range body {
		if isCommandSpace(r) {
			firstSpace = i
			break
		}
	}
	if firstSpace == -1 {
		return parsedCommand{name: strings.ToLower(body), rest: ""}, true
	}
	return parsedCommand{
		name: strings.ToLower(body[:firstSpace]),
		rest: strings.TrimSpace(body[firstSpace+1:]),
	}, true
}

// isCommandSpace mirrors the JS `\s` class for the characters a chat line can
// carry. Go's unicode.IsSpace is the superset that regexp's \s compiles to for
// the ASCII range plus the Unicode space separators, which is exactly what the
// TS regex matched.
func isCommandSpace(r rune) bool { return unicode.IsSpace(r) }

// ParseCommand ports parseBotCommand.
func ParseCommand(text string) Command {
	parsed, ok := splitCommand(text)
	if !ok {
		// A bare "0" cancels a pending selection without a slash.
		if strings.TrimSpace(text) == "0" {
			return Command{Type: CmdSelectionCancel}
		}
		return Command{Type: CmdMessage, Text: text}
	}
	name, rest := parsed.name, parsed.rest
	unknown := func() Command { return Command{Type: CmdUnknown, Name: name, Raw: text} }
	switch name {
	case "bind":
		if rest != "" {
			return Command{Type: CmdBind, Value: rest}
		}
		return unknown()
	case "help", "帮助":
		return Command{Type: CmdHelp}
	case "cancel", "取消":
		return Command{Type: CmdSelectionCancel}
	case "status", "状态":
		return Command{Type: CmdStatus}
	case "new", "clear", "新建":
		return Command{Type: CmdNew}
	case "reconnect", "重连":
		return Command{Type: CmdReconnect}
	case "workspace", "project", "项目":
		if rest != "" {
			return Command{Type: CmdWorkspaceSet, Value: rest}
		}
		return Command{Type: CmdWorkspaceList}
	case "model", "模型":
		if rest == "" {
			return Command{Type: CmdModelList}
		}
		if strings.HasPrefix(rest, "provider ") {
			return Command{Type: CmdModelProviderSet, Value: strings.TrimSpace(strings.TrimPrefix(rest, "provider "))}
		}
		if strings.HasPrefix(rest, "model ") {
			return Command{Type: CmdModelSet, Value: strings.TrimSpace(strings.TrimPrefix(rest, "model "))}
		}
		return Command{Type: CmdModelSet, Value: rest}
	case "mode", "模式":
		if rest != "" {
			return Command{Type: CmdModeSet, Value: rest}
		}
		return Command{Type: CmdModeList}
	case "thoughtlevel", "thought_level", "thought-level", "think", "思考":
		if rest != "" {
			return Command{Type: CmdThoughtLevelSet, Value: rest}
		}
		return Command{Type: CmdThoughtLevelList}
	case "task":
		if rest != "" {
			return Command{Type: CmdTaskSet, Value: rest}
		}
		return Command{Type: CmdTaskList}
	case "reply", "回复":
		if rest != "" {
			return Command{Type: CmdReplySet, Value: rest}
		}
		return Command{Type: CmdReplyList}
	case "stop", "停止":
		return Command{Type: CmdStop}
	case "permission":
		if rest != "" {
			return Command{Type: CmdPermissionRespond, Value: rest}
		}
		return unknown()
	case "elicitation", "answer", "回答":
		if rest == "" {
			return unknown()
		}
		switch strings.ToLower(rest) {
		case "submit", "done", "完成", "提交":
			return Command{Type: CmdElicitationSubmit}
		}
		return Command{Type: CmdElicitationRespond, Value: rest}
	case "approve":
		// `rest.split(/\s+/u)` on a trimmed string yields at least one element.
		fields := strings.Fields(rest)
		if len(fields) >= 2 && fields[0] != "" && fields[1] != "" {
			return Command{Type: CmdApprove, RequestID: fields[0], OptionID: fields[1]}
		}
		return unknown()
	case "deny":
		if rest != "" {
			return Command{Type: CmdDeny, RequestID: rest}
		}
		return unknown()
	default:
		return unknown()
	}
}

// BotPolicyCommandOrder is BOT_POLICY_COMMAND_ORDER: the order the menu uses
// for the commands that are also policy flags.
var BotPolicyCommandOrder = []string{
	"status",
	"new",
	"workspace",
	"model",
	"mode",
	"thoughtLevel",
	"reply",
}

// BotMenuCommandOrder is BOT_MENU_COMMAND_ORDER: policy order, prefixed by help
// and suffixed by bind.
var BotMenuCommandOrder = func() []string {
	order := make([]string, 0, len(BotPolicyCommandOrder)+2)
	order = append(order, "help")
	order = append(order, BotPolicyCommandOrder...)
	order = append(order, "bind")
	return order
}()

// IsUserCommandAllowed ports isUserCommandAllowed: help/message/approve/task/
// stop are always allowed, reconnect follows the workspace flag, everything else
// follows its own policy flag.
func IsUserCommandAllowed(bot Config, requestedCommand string) bool {
	switch requestedCommand {
	case "help", "message", "approve", "task", "stop":
		return true
	case "reconnect":
		return NormalizeCommandPolicy(bot.AllowedCommands).Workspace
	}
	policy := NormalizeCommandPolicy(bot.AllowedCommands)
	switch requestedCommand {
	case "status":
		return policy.Status
	case "new":
		return policy.New
	case "workspace":
		return policy.Workspace
	case "model":
		return policy.Model
	case "mode":
		return policy.Mode == nil || *policy.Mode
	case "thoughtLevel":
		return policy.ThoughtLevel
	case "reply":
		return policy.Reply
	}
	// An unknown command name has no flag; the TS would read undefined !== false
	// → true, so it stays allowed.
	return true
}
