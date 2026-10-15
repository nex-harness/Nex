// config.go ports packages/services/src/bots/config.ts and the helpers of
// botConfigHelpers.ts / workspaceHelpers.ts that have no I/O.
//
// The file names are wire contract: the Node engine writes bot-config.v3.json /
// bot-state.v3.json and this port reads the same bytes while the strangler runs.
// The legacy names are read once, on the first read of the v3 file, exactly
// like BotsRepo does.
package bots

import (
	"strings"
)

// File names from config.ts. BOTS_LEGACY_MODEL_CACHE_FILE /
// BOTS_MODEL_CACHE_FILE belong to the model-option cache and are not part of
// this step (nothing in Go reads them yet).
const (
	ConfigFileName       = "bot-config.v3.json"
	LegacyConfigFileName = "bot-config.json"
	LegacyStateFileName  = "bot-state.json"
	V2StateFileName      = "bot-state.v2.json"
	StateFileName        = "bot-state.v3.json"
)

// credential key prefixes (config.ts buildBotCredentialKey /
// buildBotWebhookSecretKey): the credential store keys are part of the shared
// credentials.json, so both engines must mint identical keys.
const credentialKeyPrefix = "bot"

// BuildCredentialKey ports buildBotCredentialKey.
func BuildCredentialKey(botID string) string {
	return credentialKeyPrefix + ":" + botID + ":credential"
}

// BuildWebhookSecretKey ports buildBotWebhookSecretKey.
func BuildWebhookSecretKey(botID string) string {
	return credentialKeyPrefix + ":" + botID + ":webhook-secret"
}

// exclusiveCredentialProviders is BOT_EXCLUSIVE_CREDENTIAL_PROVIDERS: the
// polling providers whose token identifies the account, so two enabled bots may
// not share one.
var exclusiveCredentialProviders = map[Provider]bool{
	ProviderTelegram: true,
	ProviderFeishu:   true,
	ProviderLark:     true,
}

// NormalizeCommandPolicy ports normalizeBotCommandPolicy. Absent flags keep the
// DEFAULT_BOT_COMMANDS value; the legacy `cli` flag is dropped and never
// written back (TS Bugfix comment).
func NormalizeCommandPolicy(commands AllowedCommands) AllowedCommands {
	normalized := DefaultAllowedCommands()
	// Every flag of the struct is a value (bool) or an optional pointer. A
	// caller reading a config file gets `false` for the optional ones when they
	// are absent, which would silently disable /mode; the TS uses `??` on
	// possibly-undefined, so an absent optional flag keeps the default instead.
	if commands.Mode == nil {
		commands.Mode = BoolPtr(true)
	}
	if commands.SandboxMode == nil {
		commands.SandboxMode = BoolPtr(true)
	}
	if commands.ApprovalPolicy == nil {
		commands.ApprovalPolicy = BoolPtr(true)
	}
	normalized.Mode = commands.Mode
	normalized.SandboxMode = commands.SandboxMode
	normalized.ApprovalPolicy = commands.ApprovalPolicy
	normalized.Status = commands.Status
	normalized.New = commands.New
	normalized.Workspace = commands.Workspace
	normalized.Model = commands.Model
	normalized.ThoughtLevel = commands.ThoughtLevel
	normalized.Reply = commands.Reply
	return normalized
}

// NormalizeCurrentOptions ports normalizeBotCurrentOptions: only modelSelection
// / mode / sandboxMode / approvalPolicy survive; a modelSelection without both
// ids is dropped (the zod safeParse in the TS).
func NormalizeCurrentOptions(options CurrentOptions) CurrentOptions {
	normalized := CurrentOptions{}
	if IsValidModelSelection(options.ModelSelection) {
		normalized.ModelSelection = options.ModelSelection
	}
	normalized.Mode = options.Mode
	normalized.SandboxMode = options.SandboxMode
	normalized.ApprovalPolicy = options.ApprovalPolicy
	return normalized
}

// IsValidModelSelection is modelSelectionSchema's shape check: both ids are
// non-empty strings.
func IsValidModelSelection(selection *ModelSelection) bool {
	return selection != nil && selection.ProviderID != "" && selection.ModelID != ""
}

// NormalizeDraftOptions ports normalizeBotDraftOptions.
func NormalizeDraftOptions(options DraftOptions) DraftOptions {
	normalized := DraftOptions{Provider: options.Provider, Mode: options.Mode}
	if IsValidModelSelection(options.ModelSelection) {
		normalized.ModelSelection = options.ModelSelection
	}
	return normalized
}

// NormalizeConfig ports normalizeBotConfig. The weixin branch deletes webhookUrl
// (TS: 微信使用内置 iLink 地址，保存时清理 webhookUrl).
func NormalizeConfig(bot Config) Config {
	normalized := bot
	if normalized.AllowedWorkspaces == nil {
		normalized.AllowedWorkspaces = []string{}
	}
	// Bugfix (TS): allowedWorkspaces 是唯一 workspace 权限边界；空数组统一落成 "*"。
	normalized.AllowedWorkspaces = NormalizeAllowedWorkspaces(bot.AllowedWorkspaces)
	normalized.AllowedCommands = NormalizeCommandPolicy(bot.AllowedCommands)
	normalized.CurrentOptions = NormalizeCurrentOptions(bot.CurrentOptions)
	// Bugfix (TS): Feishu/Lark 的回复颗粒度依赖 Card JSON 2.0，旧配置不能继续保留普通消息模式。
	normalized.ReplyMode = NormalizeReplyGranularity(bot.Provider, bot.ReplyMode)
	if normalized.Provider != ProviderWeixin {
		return normalized
	}
	normalized.WebhookURL = ""
	return normalized
}

// NormalizeConfigBots ports normalizeConfigBots.
func NormalizeConfigBots(config ConfigFile) ConfigFile {
	bots := make([]Config, 0, len(config.Bots))
	for _, bot := range config.Bots {
		bots = append(bots, NormalizeConfig(bot))
	}
	return ConfigFile{Version: config.Version, Bots: bots}
}

// FindBot ports findBot.
func FindBot(config ConfigFile, botID string) *Config {
	for i := range config.Bots {
		if config.Bots[i].ID == botID {
			return &config.Bots[i]
		}
	}
	return nil
}

// FindCallbackBot ports findCallbackBot: an explicit payload botId wins, else the
// first enabled bot of the provider.
func FindCallbackBot(config ConfigFile, provider Provider, payload map[string]any) *Config {
	if payload == nil {
		return nil
	}
	if botID, _ := payload["botId"].(string); botID != "" {
		return FindBot(config, botID)
	}
	for i := range config.Bots {
		if config.Bots[i].Provider == provider && config.Bots[i].Enabled {
			return &config.Bots[i]
		}
	}
	return nil
}

// FindAuthorizedBot ports findAuthorizedBot. weixin binds by bot id (the actor
// is resolved from the iLink contact, not from a stored user id).
func FindAuthorizedBot(config ConfigFile, actor Actor) *Config {
	if actor.Provider == ProviderWeixin {
		for i := range config.Bots {
			if config.Bots[i].Enabled && config.Bots[i].Provider == ProviderWeixin && config.Bots[i].ID == actor.BotID {
				return &config.Bots[i]
			}
		}
		return nil
	}
	for i := range config.Bots {
		bot := config.Bots[i]
		if bot.Enabled && bot.Provider == actor.Provider && bot.ProviderUserID == actor.ProviderUserID {
			return &config.Bots[i]
		}
	}
	return nil
}

// FindBoundUser ports findBoundUser: weixin needs no stored user id.
func FindBoundUser(bot Config, actor Actor) *Config {
	if actor.Provider == ProviderWeixin || bot.ProviderUserID == actor.ProviderUserID {
		return &bot
	}
	return nil
}

// ValidateConfig ports validateBotConfig. The returned strings are the exact
// error messages the TS throws, so the UI can keep branching on them.
func ValidateConfig(config ConfigFile, candidate Config) error {
	if strings.TrimSpace(candidate.ID) == "" {
		return errString("Bot ID is required.")
	}
	if candidate.Enabled && strings.TrimSpace(candidate.ProviderUserID) != "" {
		for _, bot := range config.Bots {
			if bot.ID == candidate.ID {
				continue
			}
			if bot.Enabled && bot.Provider == candidate.Provider && bot.ProviderUserID == candidate.ProviderUserID {
				return errString("An enabled bot with this provider user already exists.")
			}
		}
	}
	if candidate.Enabled && strings.TrimSpace(candidate.CredentialRef) != "" &&
		exclusiveCredentialProviders[candidate.Provider] {
		for _, bot := range config.Bots {
			if bot.ID == candidate.ID {
				continue
			}
			if bot.Enabled && bot.Provider == candidate.Provider && bot.CredentialRef == candidate.CredentialRef {
				return errString("Enabled polling bots cannot share the same credential.")
			}
		}
	}
	return nil
}

// MessageError is the plain error the TS throws with a string message; the
// bots surface renders `err.message`, so no wrapping is wanted.
type MessageError string

func (e MessageError) Error() string { return string(e) }

func errString(message string) error { return MessageError(message) }

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
