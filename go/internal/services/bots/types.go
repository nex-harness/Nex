// Package bots is the step-3.9 port of IBotsService
// (packages/services/src/bots/*, 27 files) plus the two HTTP routes
// packages/server/src/http.ts registers for provider callbacks.
//
// Scope of this file: the @nex/shared contract types (packages/shared/src/bots.ts),
// re-declared the way every other service package declares its shared types.
// Provider identifiers, the reply-granularity rules and the bot config/state
// shapes are wire-compatible with the TS on purpose: bot-config.v3.json and
// bot-state.v3.json are read by both engines during the strangler.
package bots

import "encoding/json"

// ChannelName is the rpc channel name (ServiceChannels.Bots).
const ChannelName = "bots"

// Provider is BotProvider (packages/shared/src/bots.ts botProviders).
type Provider string

// The provider list. The order is the TS tuple order and matters: it is what
// the "Unsupported provider: X" route check and the provider lookups iterate.
const (
	ProviderTelegram Provider = "telegram"
	ProviderWebhook  Provider = "webhook"
	ProviderFeishu   Provider = "feishu"
	ProviderLark     Provider = "lark"
	ProviderWeixin   Provider = "weixin"
	ProviderDiscord  Provider = "discord"
	ProviderWecom    Provider = "wecom"
)

// Providers is botProviders, in declaration order.
var Providers = []Provider{
	ProviderTelegram,
	ProviderWebhook,
	ProviderFeishu,
	ProviderLark,
	ProviderWeixin,
	ProviderDiscord,
	ProviderWecom,
}

// IsProvider reports whether value is one of botProviders. The route's
// `!botProviders.includes(provider)` check is exactly this.
func IsProvider(value string) bool {
	for _, provider := range Providers {
		if string(provider) == value {
			return true
		}
	}
	return false
}

// IsFeishuProvider ports isFeishuBotProvider.
func IsFeishuProvider(provider Provider) bool {
	return provider == ProviderFeishu || provider == ProviderLark
}

// ReplyGranularity is BotReplyGranularity.
type ReplyGranularity string

// ReplyMode is BotReplyMode (an alias of ReplyGranularity in the TS).
type ReplyMode = ReplyGranularity

const (
	GranularityAssistantChanges          ReplyGranularity = "assistant_changes"
	GranularityAssistantToolCallsChanges ReplyGranularity = "assistant_toolcalls_changes"
	GranularitySummaryChanges            ReplyGranularity = "summary_changes"
	GranularityStreamingCard             ReplyGranularity = "streaming_card"
)

// ReplyGranularities is BOT_REPLY_GRANULARITIES in declaration order.
var ReplyGranularities = []ReplyGranularity{
	GranularityAssistantChanges,
	GranularityAssistantToolCallsChanges,
	GranularitySummaryChanges,
	GranularityStreamingCard,
}

// DefaultBotReplyGranularity is DEFAULT_BOT_REPLY_GRANULARITY.
const DefaultBotReplyGranularity = GranularityAssistantChanges

// GetSupportedBotReplyGranularities ports getSupportedBotReplyGranularities:
// Feishu/Lark only stream cards, every other provider gets everything except
// streaming_card.
func GetSupportedReplyGranularities(provider Provider) []ReplyGranularity {
	if IsFeishuProvider(provider) {
		return []ReplyGranularity{GranularityStreamingCard}
	}
	supported := make([]ReplyGranularity, 0, len(ReplyGranularities))
	for _, granularity := range ReplyGranularities {
		// Bugfix (TS comment, kept): streaming card 依赖 Feishu/Lark Card JSON 2.0，
		// 其他 channel 无法渲染或更新该消息形态。
		if granularity == GranularityStreamingCard {
			continue
		}
		supported = append(supported, granularity)
	}
	return supported
}

// NormalizeReplyGranularity ports normalizeBotReplyGranularity: unsupported
// candidates fall back to the FIRST supported entry, not to the default.
func NormalizeReplyGranularity(provider Provider, replyMode ReplyGranularity) ReplyGranularity {
	supported := GetSupportedReplyGranularities(provider)
	candidate := replyMode
	if candidate == "" {
		candidate = DefaultBotReplyGranularity
	}
	for _, granularity := range supported {
		if granularity == candidate {
			return candidate
		}
	}
	return supported[0]
}

// AllBotWorkspaces is ALL_BOT_WORKSPACES: the wildcard workspace grant.
const AllBotWorkspaces = "*"

// BindCodeTTLMillis is BOT_BIND_CODE_TTL_MS.
const BindCodeTTLMillis = 30_000

// ConfigVersion and StateVersion are the two literals both engines write.
const (
	ConfigVersion = 3
	StateVersion  = 3
)

// ChatType is the private/group discriminator.
type ChatType string

const (
	ChatPrivate ChatType = "private"
	ChatGroup   ChatType = "group"
)

// WorkspaceRef is BotWorkspaceRef. ID is the workspace KEY
// (identity || path, see workspaceHelpers.getWorkspaceKey).
type WorkspaceRef struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	WorkspacePath     string `json:"workspacePath"`
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
}

// AllowedCommands is BotAllowedCommands. The optional flags are optional in
// the TS schema too (mode/sandboxMode/approvalPolicy/cli); cli is never written
// back by normalizeBotCommandPolicy.
type AllowedCommands struct {
	Status         bool  `json:"status"`
	New            bool  `json:"new"`
	Workspace      bool  `json:"workspace"`
	Model          bool  `json:"model"`
	Mode           *bool `json:"mode,omitempty"`
	ThoughtLevel   bool  `json:"thoughtLevel"`
	SandboxMode    *bool `json:"sandboxMode,omitempty"`
	ApprovalPolicy *bool `json:"approvalPolicy,omitempty"`
	Reply          bool  `json:"reply"`
}

// DefaultAllowedCommands is DEFAULT_BOT_COMMANDS.
func DefaultAllowedCommands() AllowedCommands {
	return AllowedCommands{
		Status:       true,
		New:          true,
		Workspace:    true,
		Model:        true,
		Mode:         boolPtr(true),
		ThoughtLevel: true,
		Reply:        true,
	}
}

// ModelSelection is the shared ModelSelection ({providerId, modelId, options?}).
// It is declared here (not imported) because the bots contract only reads it.
type ModelSelection struct {
	ProviderID string         `json:"providerId"`
	ModelID    string         `json:"modelId"`
	Options    map[string]any `json:"options,omitempty"`
}

// CurrentOptions is BotCurrentOptions.
type CurrentOptions struct {
	ModelSelection *ModelSelection `json:"modelSelection,omitempty"`
	Mode           string          `json:"mode,omitempty"`
	SandboxMode    string          `json:"sandboxMode,omitempty"`
	ApprovalPolicy string          `json:"approvalPolicy,omitempty"`
}

// Config is BotConfig — one entry of bot-config.v3.json.
type Config struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Provider              Provider        `json:"provider"`
	Enabled               bool            `json:"enabled"`
	CredentialRef         string          `json:"credentialRef,omitempty"`
	WebhookSecretRef      string          `json:"webhookSecretRef,omitempty"`
	WebhookURL            string          `json:"webhookUrl,omitempty"`
	WebhookAuthHeaderName string          `json:"webhookAuthHeaderName,omitempty"`
	FeishuAppID           string          `json:"feishuAppId,omitempty"`
	ProviderUserID        string          `json:"providerUserId,omitempty"`
	DisplayName           string          `json:"displayName,omitempty"`
	AllowedWorkspaces     []string        `json:"allowedWorkspaces"`
	AllowedCommands       AllowedCommands `json:"allowedCommands"`
	CurrentOptions        CurrentOptions  `json:"currentOptions"`
	ReplyMode             ReplyMode       `json:"replyMode"`
}

// ConfigFile is BotsConfigFile.
type ConfigFile struct {
	Version int      `json:"version"`
	Bots    []Config `json:"bots"`
}

// DefaultConfigFile is createDefaultBotsConfig.
func DefaultConfigFile() ConfigFile {
	return ConfigFile{Version: ConfigVersion, Bots: []Config{}}
}

// State is BotState / BotContextState (the same interface in the TS).
type State struct {
	BotID               string          `json:"botId"`
	WorkspacePath       string          `json:"workspacePath"`
	WorkspaceIdentity   string          `json:"workspaceIdentity,omitempty"`
	WorkspaceID         string          `json:"workspaceId,omitempty"`
	Mode                string          `json:"mode"`
	ActiveTaskID        *string         `json:"activeTaskId"`
	DraftOptions        *DraftOptions   `json:"draftOptions,omitempty"`
	PendingPermissions  []PendingPermit `json:"pendingPermissionOptions,omitempty"`
	PendingElicitation  json.RawMessage `json:"pendingElicitation,omitempty"`
	TelegramOffset      *float64        `json:"telegramOffset,omitempty"`
	WeixinGetUpdatesBuf string          `json:"weixinGetUpdatesBuf,omitempty"`
	WeixinActivatedAt   *float64        `json:"weixinActivatedAt,omitempty"`
	UpdatedAt           float64         `json:"updatedAt"`
}

// DraftOptions is BotDraftOptions.
type DraftOptions struct {
	Provider       string          `json:"provider"`
	ModelSelection *ModelSelection `json:"modelSelection,omitempty"`
	Mode           string          `json:"mode,omitempty"`
}

// PendingPermit is BotPendingPermissionOption. Response stays raw: its shape is
// nexPermissionResponseSchema and the UI branches on its decision strings.
type PendingPermit struct {
	RequestID string          `json:"requestId"`
	OptionID  string          `json:"optionId"`
	Command   string          `json:"command"`
	Label     string          `json:"label"`
	Response  json.RawMessage `json:"response"`
	HandledAt *float64        `json:"handledAt,omitempty"`
}

// StateFile is BotsStateFile.
type StateFile struct {
	Version int              `json:"version"`
	Bots    map[string]State `json:"bots"`
}

// DefaultStateFile is the `{version:3, bots:{}}` the repo writes on first read.
func DefaultStateFile() StateFile {
	return StateFile{Version: StateVersion, Bots: map[string]State{}}
}

// Actor is BotActor — the identity an inbound message is attributed to.
type Actor struct {
	Provider             Provider `json:"provider"`
	BotID                string   `json:"botId"`
	ProviderUserID       string   `json:"providerUserId"`
	DisplayName          string   `json:"displayName,omitempty"`
	ChatType             ChatType `json:"chatType"`
	ChatID               string   `json:"chatId,omitempty"`
	ProviderMessageID    string   `json:"providerMessageId,omitempty"`
	ProviderContextToken string   `json:"providerContextToken,omitempty"`
}

// AttachmentKind is BotInboundAttachmentKind.
type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentAudio AttachmentKind = "audio"
	AttachmentVideo AttachmentKind = "video"
	AttachmentFile  AttachmentKind = "file"
)

// Attachment is BotInboundAttachment.
type Attachment struct {
	ID             string            `json:"id"`
	Kind           AttachmentKind    `json:"kind"`
	Filename       string            `json:"filename"`
	MimeType       string            `json:"mimeType"`
	SizeBytes      *float64          `json:"sizeBytes,omitempty"`
	ProviderFileID string            `json:"providerFileId,omitempty"`
	DownloadURL    string            `json:"downloadUrl,omitempty"`
	DataBase64     string            `json:"dataBase64,omitempty"`
	LocalPath      string            `json:"localPath,omitempty"`
	ProviderMeta   map[string]string `json:"providerMetadata,omitempty"`
}

// StructuredElicitationResponse is BotStructuredElicitationResponse.
type StructuredElicitationResponse struct {
	RequestID string         `json:"requestId"`
	Action    string         `json:"action"`
	Content   map[string]any `json:"content,omitempty"`
}

// InboundMessage is BotInboundMessage.
type InboundMessage struct {
	BotID               string                         `json:"botId"`
	Actor               Actor                          `json:"actor"`
	Text                string                         `json:"text"`
	Attachments         []Attachment                   `json:"attachments,omitempty"`
	ElicitationResponse *StructuredElicitationResponse `json:"elicitationResponse,omitempty"`
	ReceivedAt          *float64                       `json:"receivedAt,omitempty"`
}
