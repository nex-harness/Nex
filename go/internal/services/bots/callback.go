// callback.go ports processProviderCallback
// (packages/services/src/bots/botsService.ts line 2546) — the function the
// HTTP route, the telegram polling loop and the Feishu/WebSocket runtimes all
// funnel through.
//
// The status contract the route depends on, verbatim from the TS:
//
//	400 → unknown provider adapter
//	401 → credential/prepare failure (`{error: <message>}`)
//	503 → a business failure happened (hadBusinessFailure)
//	200 → everything else
//
// and the two documented rules that the tests pin:
//
//   - webhook secret mismatch does NOT produce a non-200 status; it produces a
//     user-facing "Webhook secret 校验失败。" reply and the callback is skipped;
//   - a business failure sets hadBusinessFailure, which makes the final result
//     ok=false + status=503 so the external cursor (Telegram offset / WeChat
//     buf) is never advanced.
package bots

import (
	"context"
	"strings"
	"sync"
	"time"
)

// InboundDeliveryDedupeTTL is BOT_INBOUND_DELIVERY_DEDUPE_TTL_MS.
const InboundDeliveryDedupeTTL = 2 * time.Minute

// SessionBridge is the SEAM for step 3.10.
//
// Bots create sessions and send prompts: that is INexTaskService territory
// (createTask / sendPrompt / respondPermission / respondElicitation /
// setConfigOption / stopGeneration / listTasks / getTaskConfigOptions), which
// the Go agent bridge does not own until step 3.10. Porting the whole
// 6.3k-line botsService.ts first would bind it to an interface that 3.10 then
// has to reshape.
//
// This step therefore owns everything that is NOT session creation — the config
// and state repositories, the provider adapters, the parser, the formatter, the
// cursor discipline and the HTTP contract — and drives the session leg through
// this interface. Step 3.10 supplies an implementation backed by the Go agent
// service port; until then the caller wires the Node-side bridge or leaves it
// nil, in which case a message that needs a session answers with the TS's
// taskRunning/failed text instead of pretending it ran.
type SessionBridge interface {
	// HandleInboundMessage processes one inbound message end to end: it may
	// create/resume a task, send the prompt, answer a permission or elicitation,
	// or reject a command. It returns the outbound replies in order.
	//
	// A returned error is a BUSINESS failure: the callback answers 503 and the
	// caller must not advance any external cursor.
	HandleInboundMessage(ctx context.Context, message InboundMessage) ([]OutboundMessage, error)
}

// SessionBridgeFunc adapts a function to SessionBridge.
type SessionBridgeFunc func(ctx context.Context, message InboundMessage) ([]OutboundMessage, error)

// HandleInboundMessage implements SessionBridge.
func (f SessionBridgeFunc) HandleInboundMessage(ctx context.Context, message InboundMessage) ([]OutboundMessage, error) {
	return f(ctx, message)
}

// CallbackDeps are the seams processProviderCallback needs.
type CallbackDeps struct {
	// ReadConfig is repo.readConfig.
	ReadConfig func(ctx context.Context) (ConfigFile, error)
	// LoadCredential resolves a credential ref (webhook secret / token).
	LoadCredential CredentialLoader
	// Bridge is the step-3.10 session seam; nil means "no session support".
	Bridge SessionBridge
	// Locale resolves the reply language; nil reads zh-CN (the TS default).
	Locale func(ctx context.Context) string
	// Logger receives the per-message log lines; nil discards.
	Logger Logger
	// Now pins the dedupe clock; nil uses time.Now.
	Now func() time.Time
	// Providers maps a provider to its adapter. A missing entry is the TS's
	// `if (!adapter) return {ok:false, replies:[], status:400}`.
	Providers map[Provider]ProviderAdapter
}

// CallbackPipeline is the stateful half of the callback path: the inbound
// delivery dedupe and the per-actor serialisation queue.
type CallbackPipeline struct {
	deps CallbackDeps

	mu             sync.Mutex
	recentDelivery map[string]time.Time
	actorQueue     map[string]chan struct{}
}

// NewCallbackPipeline builds a pipeline.
func NewCallbackPipeline(deps CallbackDeps) *CallbackPipeline {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &CallbackPipeline{
		deps:           deps,
		recentDelivery: map[string]time.Time{},
		actorQueue:     map[string]chan struct{}{},
	}
}

// buildInboundDeliveryKey ports buildInboundDeliveryKey: botId, provider, chat
// (chatId ?? providerUserId) and the provider message id. A message without a
// provider message id has NO key and is therefore never deduped — deliberately,
// because the TS cannot tell two identical texts apart either.
func buildInboundDeliveryKey(message InboundMessage) string {
	providerMessageID := strings.TrimSpace(message.Actor.ProviderMessageID)
	if providerMessageID == "" {
		return ""
	}
	chat := message.Actor.ChatID
	if chat == "" {
		chat = message.Actor.ProviderUserID
	}
	return strings.Join([]string{
		message.Actor.BotID, string(message.Actor.Provider), chat, providerMessageID,
	}, "::")
}

// MarkInboundDelivery ports markInboundDelivery: false means "duplicate, skip".
//
// Bugfix (TS): 飞书 WebSocket 可能重投同一条 im.message.receive_v1，微信/Telegram
// 也可能在重试后重放同一 message id。普通消息有创建/发送任务的副作用，必须在进入
// 业务处理前按 provider message id 幂等，避免同一句 hello 被执行两轮。
func (p *CallbackPipeline) MarkInboundDelivery(message InboundMessage) bool {
	now := p.deps.Now()
	key := buildInboundDeliveryKey(message)
	if key == "" {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for existing, at := range p.recentDelivery {
		if now.Sub(at) >= InboundDeliveryDedupeTTL {
			delete(p.recentDelivery, existing)
		}
	}
	if _, seen := p.recentDelivery[key]; seen {
		return false
	}
	p.recentDelivery[key] = now
	return true
}

// ReleaseInboundDelivery ports releaseInboundDelivery: a message that FAILED
// business processing must be reprocessable, so its dedupe key is released.
func (p *CallbackPipeline) ReleaseInboundDelivery(message InboundMessage) {
	key := buildInboundDeliveryKey(message)
	if key == "" {
		return
	}
	p.mu.Lock()
	delete(p.recentDelivery, key)
	p.mu.Unlock()
}

// actorContextKey is getActorContextKey: one serial lane per bot+user, so two
// fast replies cannot both read the same pendingElicitation.
func actorContextKey(actor Actor) string {
	return actor.BotID + "::" + string(actor.Provider) + "::" + actor.ProviderUserID
}

// enqueueActor runs fn in the actor's serial lane.
//
// Bugfix (TS): 同一个用户可能连续点击 AskUserQuestion 按钮或快速回复多条消息。
// 这里按 actor 串行化入站处理，避免两个并发请求同时读取同一个 pendingElicitation。
func (p *CallbackPipeline) enqueueActor(ctx context.Context, actor Actor, fn func() error) error {
	key := actorContextKey(actor)
	p.mu.Lock()
	if p.actorQueue == nil {
		p.actorQueue = map[string]chan struct{}{}
	}
	gate, ok := p.actorQueue[key]
	if !ok {
		gate = make(chan struct{}, 1)
		p.actorQueue[key] = gate
	}
	p.mu.Unlock()

	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() {
		<-gate
		p.mu.Lock()
		if p.actorQueue[key] == gate {
			delete(p.actorQueue, key)
		}
		p.mu.Unlock()
	}()
	return fn()
}

// HandleProviderCallbackResponse ports processProviderCallback.
func (p *CallbackPipeline) HandleProviderCallbackResponse(ctx context.Context, provider Provider, payload map[string]any) CallbackResult {
	adapter := p.deps.Providers[provider]
	if adapter == nil {
		return CallbackResult{OK: false, Replies: nil, Status: 400}
	}
	locale := ""
	if p.deps.Locale != nil {
		locale = p.deps.Locale(ctx)
	}
	config, err := p.deps.ReadConfig(ctx)
	if err != nil {
		if p.deps.Logger != nil {
			p.deps.Logger.Error("", "read bots config failed: "+err.Error())
		}
		return CallbackResult{OK: false, Replies: nil, Status: 503,
			ResponseBody: map[string]any{"error": err.Error()}}
	}
	callbackBot := FindCallbackBot(config, provider, payload)
	prepared := payload
	if callbackBot != nil {
		prepareCtx := ctx
		transformed, prepareErr := adapter.PrepareCallbackPayload(prepareCtx, *callbackBot, payload)
		if prepareErr != nil {
			return CallbackResult{
				OK: false, Replies: nil, Status: 401,
				ResponseBody: map[string]any{"error": prepareErr.Error()},
			}
		}
		if transformed != nil {
			prepared = transformed
		}
	}
	// A provider that answers the HTTP call itself (Feishu card action) short
	// circuits here: its own responseBody+status is the whole answer.
	if callbackBot != nil {
		callbackResponse, err := adapter.HandleCallbackResponse(ctx, *callbackBot, prepared)
		if err != nil {
			return CallbackResult{OK: false, Replies: nil, Status: 503,
				ResponseBody: map[string]any{"error": err.Error()}}
		}
		if callbackResponse != nil && callbackResponse.ResponseBody != nil {
			status := callbackResponse.Status
			if status == 0 {
				status = 200
			}
			return CallbackResult{
				OK:           status < 400,
				Replies:      nil,
				ResponseBody: callbackResponse.ResponseBody,
				Status:       status,
			}
		}
	}
	// Feishu/lark keep their provider in the payload so one adapter serves both.
	parsePayload := prepared
	if IsFeishuProvider(provider) && prepared != nil {
		parsePayload = map[string]any{"nexProvider": string(provider)}
		for key, value := range prepared {
			parsePayload[key] = value
		}
	}
	parsedInbound := adapter.ParseCallback(parsePayload)
	replies := make([]OutboundMessage, 0, len(parsedInbound))
	hadBusinessFailure := false
	inboundSecret, _ := prepared["webhookSecret"].(string)
	for _, inbound := range parsedInbound {
		bot := FindBot(config, inbound.BotID)
		if bot != nil && bot.Provider == ProviderWebhook && bot.WebhookSecretRef != "" && p.deps.LoadCredential != nil {
			expectedSecret, err := p.deps.LoadCredential.Load(ctx, bot.WebhookSecretRef)
			if err != nil {
				return CallbackResult{OK: false, Replies: nil, Status: 503,
					ResponseBody: map[string]any{"error": err.Error()}}
			}
			if expectedSecret != "" && expectedSecret != inboundSecret {
				replies = append(replies, createOutbound(inbound.Actor,
					FormatMessage(locale, "webhookSecretInvalid", nil)))
				continue
			}
		}
		if bot != nil && IsFeishuProvider(bot.Provider) && bot.WebhookSecretRef != "" && p.deps.LoadCredential != nil {
			expectedToken, err := p.deps.LoadCredential.Load(ctx, bot.WebhookSecretRef)
			if err != nil {
				return CallbackResult{OK: false, Replies: nil, Status: 503,
					ResponseBody: map[string]any{"error": err.Error()}}
			}
			if expectedToken != "" && expectedToken != readFeishuInboundToken(prepared) {
				replies = append(replies, createOutbound(inbound.Actor,
					FormatMessage(locale, "webhookSecretInvalid", nil)))
				continue
			}
		}
		inboundMessage := inbound
		if !p.MarkInboundDelivery(inboundMessage) {
			if p.deps.Logger != nil {
				p.deps.Logger.Info("", "provider callback duplicated provider="+string(provider)+
					" bot="+inboundMessage.BotID+" user="+inboundMessage.Actor.ProviderUserID+
					" messageId="+inboundMessage.Actor.ProviderMessageID)
			}
			continue
		}
		var outbound []OutboundMessage
		// Bugfix (TS): 同一个用户可能连续点击 AskUserQuestion 按钮或快速回复多条消息。
		// 这里按 actor 串行化入站处理，避免两个并发请求同时读取同一个 pendingElicitation。
		if err := p.enqueueActor(ctx, inboundMessage.Actor, func() error {
			var businessFailure bool
			outbound, businessFailure = p.runInbound(ctx, locale, provider, inboundMessage)
			if businessFailure {
				hadBusinessFailure = true
			}
			return nil
		}); err != nil {
			return CallbackResult{OK: false, Replies: nil, Status: 503,
				ResponseBody: map[string]any{"error": err.Error()}}
		}
		replies = append(replies, outbound...)
	}
	if hadBusinessFailure {
		// Bugfix (TS): 错误提示发送成功不等于业务消息已经消费成功。返回 ok=false +
		// status=503 阻止 Telegram/微信提交外部游标。
		return CallbackResult{OK: false, Replies: replies, Status: 503}
	}
	return CallbackResult{OK: true, Replies: replies}
}

// runInbound executes the bridge for one inbound message and returns its replies
// plus the business-failure flag. The dedupe key is released on failure so the
// provider's retry can reprocess the message.
func (p *CallbackPipeline) runInbound(
	ctx context.Context,
	locale string,
	provider Provider,
	inbound InboundMessage,
) ([]OutboundMessage, bool) {
	if p.deps.Bridge == nil {
		return nil, false
	}
	outbound, err := p.deps.Bridge.HandleInboundMessage(ctx, inbound)
	if err != nil {
		p.ReleaseInboundDelivery(inbound)
		if p.deps.Logger != nil {
			p.deps.Logger.Warn("", "provider callback failed provider="+string(provider)+
				" bot="+inbound.BotID+" user="+inbound.Actor.ProviderUserID+": "+err.Error())
		}
		return []OutboundMessage{createOutbound(inbound.Actor,
			FormatMessage(locale, "callbackFailed", map[string]string{"message": err.Error()}))}, true
	}
	return outbound, false
}

// readFeishuInboundToken ports the Feishu inbound token resolution: payload.token,
// then header.token.
func readFeishuInboundToken(payload map[string]any) string {
	if token, ok := payload["token"].(string); ok && token != "" {
		return token
	}
	if header, ok := payload["header"].(map[string]any); ok {
		if token, ok := header["token"].(string); ok {
			return token
		}
	}
	return ""
}

// createOutbound ports createOutbound: every reply is addressed to the actor and
// carries the resolved locale.
func createOutbound(actor Actor, text string) OutboundMessage {
	return OutboundMessage{
		BotID:                actor.BotID,
		Provider:             actor.Provider,
		ProviderUserID:       actor.ProviderUserID,
		Text:                 text,
		ProviderContextToken: actor.ProviderContextToken,
	}
}

// CreateOutbound exposes createOutbound for the service and the tests.
func CreateOutbound(actor Actor, text string) OutboundMessage { return createOutbound(actor, text) }
