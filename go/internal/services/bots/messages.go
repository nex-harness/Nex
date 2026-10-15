package bots

import "strings"

// messages.go ports packages/services/src/bots/messages.ts: the bilingual bot reply text.
//
// The bilingual text table lives in messages_table.go (generated from the TS
// table); this file owns the substitution rule. messages_test.go asserts the
// two locales carry the same id set, so a key can never go missing in one
// language.

// MessageID is BotMessageId.
type MessageID string

// DefaultMessageLocale is DEFAULT_BOT_MESSAGE_LOCALE: anything that is not
// "en-US" is zh-CN.
const DefaultMessageLocale = "zh-CN"

// NormalizeLocale ports normalizeBotMessageLocale.
func NormalizeLocale(locale string) string {
	if locale == "en-US" {
		return "en-US"
	}
	return DefaultMessageLocale
}

// FormatMessage ports formatBotMessage: values substitute every occurrence of
// {key}; a missing value substitutes the empty string (String(undefined ?? "")).
func FormatMessage(locale string, id MessageID, values map[string]string) string {
	message := messages[NormalizeLocale(locale)][id]
	for key, value := range values {
		message = strings.ReplaceAll(message, "{"+key+"}", value)
	}
	return message
}

// MessageIDs lists every id in the table, in the zh-CN declaration order.
func MessageIDs() []MessageID {
	return messageIDOrder
}

var messageIDOrder = []MessageID{
	"botDisabled",
	"privateChatOnly",
	"bindPrivateOnly",
	"userNotBound",
	"commandNotAllowed",
	"noWorkspaceAllowed",
	"workspaceOutOfScope",
	"bindCodeInvalid",
	"bindBotMissing",
	"bindSuccess",
	"weixinActivatedWelcome",
	"helpTitle",
	"helpHelp",
	"helpBind",
	"helpStatus",
	"helpNew",
	"helpWorkspace",
	"helpModel",
	"helpMode",
	"helpThoughtLevel",
	"helpReply",
	"webhookSecretInvalid",
	"callbackFailed",
	"sessionExpiredNewTaskHint",
	"deletedTaskReplaced",
	"received",
	"attachmentOnlyPrompt",
	"attachmentRejected",
	"attachmentDownloadUnavailable",
	"attachmentTooLarge",
	"selectionCancelled",
	"selectionCancelOption",
	"selectionTextHint",
	"selectionTextHintNoCancel",
	"newTaskDraft",
	"workspaceSelectTitle",
	"workspaceMissing",
	"modelSelectTitle",
	"modelProviderSelectTitle",
	"modelModelSelectTitle",
	"modelMissing",
	"sessionModelUnavailable",
	"modeSelectTitle",
	"modeMissing",
	"modeChanged",
	"modeLocked",
	"thoughtLevelSelectTitle",
	"thoughtLevelMissing",
	"thoughtLevelChanged",
	"modelProviderMissing",
	"modelChanged",
	"taskMissing",
	"taskChanged",
	"noActiveTask",
	"permissionExpired",
	"permissionHandled",
	"permissionDenied",
	"permissionSubmitted",
	"elicitationExpired",
	"elicitationHandled",
	"elicitationSubmitted",
	"elicitationCancelled",
	"elicitationCustomOption",
	"elicitationCustomPlaceholder",
	"elicitationQuestionTitle",
	"planApprovalTitle",
	"planApprovalHeader",
	"planApprovalApprove",
	"planApprovalApproveDescription",
	"elicitationCancelledCard",
	"elicitationSubmitOption",
	"elicitationSkipOption",
	"elicitationMultiSelectHint",
	"elicitationTextHint",
	"statusWorkspace",
	"statusModel",
	"statusTask",
	"statusState",
	"statusWorked",
	"statusProgress",
	"statusDraft",
	"statusRemoteDisconnected",
	"statusCancelled",
	"statusStopped",
	"streamingStatusRunning",
	"streamingStatusCompleted",
	"streamingStatusFailed",
	"streamingWorking",
	"streamingToolSummaries",
	"stopSubmitted",
	"unknownCommand",
	"taskFailed",
	"taskRunning",
	"taskSelectTitle",
	"noHistoryTasks",
	"remoteDisconnected",
	"remoteDisconnectedStatus",
	"remoteWorkspaceSelectedDisconnected",
	"remoteReconnectStarting",
	"remoteReconnectFailed",
	"remoteReconnectUnavailable",
	"remoteReconnectLocal",
	"remoteReconnectAlreadyConnected",
	"replySelectTitle",
	"replyMissing",
	"replyChanged",
}
