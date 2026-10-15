// telegram_runtime.go ports the polling loop of
// packages/services/src/bots/telegramChannelRuntime.ts.
//
// The whole file exists for ONE rule, which is the TS comment on
// writeTelegramOffset:
//
//	Bugfix: offset 是 Telegram 外部队列的消费确认点。业务回调失败前推进会让
//	用户消息、权限和 elicitation 响应永久跳过；成功后逐条提交才能安全重试。
//
// Therefore: AssertCallbackSucceeded is checked BEFORE WriteTelegramOffset, per
// update, and the cursor is committed one update at a time. A crash between the
// callback succeeding and the cursor write replays that one update — which the
// inbound delivery dedupe absorbs — instead of silently dropping it.
package bots

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// TelegramLongPollRequestTimeout is
// TELEGRAM_LONG_POLL_REQUEST_TIMEOUT_MS. Telegram holds the request for up to
// 25s; the client budget must cover the body read too, otherwise a half-open
// connection holds the polling lock forever (TS comment).
const TelegramLongPollRequestTimeout = 40 * time.Second

// TelegramRuntimeDeps are the seams the loop needs.
type TelegramRuntimeDeps struct {
	Provider   *TelegramProvider
	Credential CredentialLoader
	Status     *RuntimeStatusStore
	Logger     Logger
	// ReadConfig returns the current config; the loop reconciles against it.
	ReadConfig func(ctx context.Context) (ConfigFile, error)
	// ReadTelegramOffset / WriteTelegramOffset are the persisted cursor.
	ReadTelegramOffset  func(ctx context.Context, botID string) (*float64, error)
	WriteTelegramOffset func(ctx context.Context, botID string, offset float64) error
	// ProcessProviderCallback runs the business path; its result decides whether
	// the cursor may advance.
	ProcessProviderCallback func(ctx context.Context, payload map[string]any) (CallbackResult, error)
	// EnsureStorageMigrated runs before the FIRST poll of a refresh (TS Bugfix:
	// 启动轮询前必须先完成旧 state 迁移，否则迁移写回可能覆盖刚更新的第三方游标).
	EnsureStorageMigrated func(ctx context.Context) error
	// RunBackgroundTasks false suppresses every fire-and-forget refresh (the
	// desktop-attached remote case).
	RunBackgroundTasks bool
	// Sleep is the backoff; nil sleeps for real.
	Sleep func(ctx context.Context, d time.Duration)
}

// Logger is the bots service logger seam (logx.ServiceLogger).
type Logger interface {
	Debug(traceID, msg string, args ...any)
	Info(traceID, msg string, args ...any)
	Warn(traceID, msg string, args ...any)
	Error(traceID, msg string, args ...any)
}

// telegramRuntimeEntry is one running loop.
type telegramRuntimeEntry struct {
	cancel      context.CancelFunc
	fingerprint string
	done        chan struct{}
}

// TelegramRuntime owns the per-bot polling loops.
type TelegramRuntime struct {
	deps        TelegramRuntimeDeps
	refresh     *LatestRefreshQueue
	mu          sync.Mutex
	runtimes    map[string]*telegramRuntimeEntry
	stoppingAll bool
}

// NewTelegramRuntime builds the runtime.
func NewTelegramRuntime(deps TelegramRuntimeDeps) *TelegramRuntime {
	if !deps.RunBackgroundTasks {
		// The zero value means "no background tasks"; a caller that wants them
		// sets the flag explicitly so the zero value is the safe one.
		deps.RunBackgroundTasks = false
	}
	return &TelegramRuntime{
		deps:     deps,
		refresh:  NewLatestRefreshQueue(),
		runtimes: map[string]*telegramRuntimeEntry{},
	}
}

func (r *TelegramRuntime) sleep(ctx context.Context, d time.Duration) {
	if r.deps.Sleep != nil {
		r.deps.Sleep(ctx, d)
		return
	}
	WaitFor(ctx, d)
}

// connectionFingerprint ports getConnectionFingerprint.
func (r *TelegramRuntime) connectionFingerprint(ctx context.Context, bot Config) (string, error) {
	credential := ""
	if bot.CredentialRef != "" {
		value, err := r.deps.Credential.Load(ctx, bot.CredentialRef)
		if err != nil {
			return "", err
		}
		credential = value
	}
	return CreateBotConnectionFingerprint([]string{
		string(bot.Provider), bot.CredentialRef, credential,
	}), nil
}

// pollBot ports pollBot().
func (r *TelegramRuntime) pollBot(ctx context.Context, bot Config) {
	token, err := r.deps.Credential.Load(ctx, bot.CredentialRef)
	if err != nil || token == "" {
		// A missing token is a status, not a crash: the user must fix the config.
		r.deps.Status.SetRuntimeStatus(RuntimeInfo{
			BotID: bot.ID, Provider: ProviderTelegram, Status: "error",
			MessageID: "bots.runtime.telegramTokenMissing", Message: "Telegram bot token is missing.",
		})
		return
	}
	for ctx.Err() == nil {
		if err := r.pollOnce(ctx, bot, token); err != nil {
			if ctx.Err() != nil {
				return
			}
			if r.deps.Logger != nil {
				r.deps.Logger.Warn("", "Telegram polling failed; retrying bot="+bot.ID)
			}
			offset, _ := r.deps.ReadTelegramOffset(ctx, bot.ID)
			r.deps.Status.SetRuntimeStatus(RuntimeInfo{
				BotID: bot.ID, Provider: ProviderTelegram, Status: "error",
				MessageID: "bots.runtime.telegramPollingFailedRetrying",
				Message:   "Telegram polling failed; retrying.",
				Offset:    offset,
			})
			r.sleep(ctx, 5*time.Second)
		}
	}
}

// pollOnce runs one getUpdates round.
func (r *TelegramRuntime) pollOnce(ctx context.Context, bot Config, token string) error {
	offset, err := r.deps.ReadTelegramOffset(ctx, bot.ID)
	if err != nil {
		return err
	}
	body := map[string]any{
		"timeout":         25,
		"allowed_updates": []string{"message", "callback_query"},
	}
	if offset != nil {
		body["offset"] = *offset
	}
	client := r.deps.Provider.client()
	client.Timeout = TelegramLongPollRequestTimeout
	response, err := client.FetchJSON(ctx, http.MethodPost, r.deps.Provider.apiURL(token, "getUpdates"), body)
	if err != nil {
		return err
	}
	if !response.OK {
		message := "Telegram getUpdates failed: HTTP " + itoa(response.Status)
		if response.Status == 409 {
			message = "Telegram token is already used by another polling client."
		}
		r.deps.Status.SetRuntimeStatus(RuntimeInfo{
			BotID: bot.ID, Provider: ProviderTelegram, Status: "error", Message: message, Offset: offset,
		})
		backoff := 5 * time.Second
		if response.Status == 409 {
			backoff = 10 * time.Second
		}
		r.sleep(ctx, backoff)
		return nil
	}
	record, _ := response.Payload.(map[string]any)
	ok, _ := record["ok"].(bool)
	updates, hasUpdates := record["result"].([]any)
	if !ok || !hasUpdates {
		description, _ := record["description"].(string)
		if description == "" {
			description = "Telegram getUpdates returned an invalid response."
		}
		r.deps.Status.SetRuntimeStatus(RuntimeInfo{
			BotID: bot.ID, Provider: ProviderTelegram, Status: "error", Message: description, Offset: offset,
		})
		r.sleep(ctx, 5*time.Second)
		return nil
	}
	for _, rawUpdate := range updates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		update, ok := rawUpdate.(map[string]any)
		if !ok {
			continue
		}
		updateID, hasUpdateID := update["update_id"].(float64)
		result, err := r.deps.ProcessProviderCallback(ctx, map[string]any{
			"botId":  bot.ID,
			"update": update,
		})
		if err != nil {
			return err
		}
		// The cursor is the external queue's consumption ack. It moves ONLY after
		// the business callback reported success.
		if err := AssertCallbackSucceeded("Telegram", result); err != nil {
			return err
		}
		if hasUpdateID {
			next := updateID + 1
			if err := r.deps.WriteTelegramOffset(ctx, bot.ID, next); err != nil {
				return err
			}
		}
	}
	latest, _ := r.deps.ReadTelegramOffset(ctx, bot.ID)
	r.deps.Status.SetRuntimeStatus(RuntimeInfo{
		BotID: bot.ID, Provider: ProviderTelegram, Status: "polling",
		MessageID: "bots.runtime.telegramLongPollingRunning",
		Message:   "Telegram long polling is running.",
		Offset:    latest,
	})
	return nil
}

// itoa is strconv.Itoa, local so the status message reads as one expression.
func itoa(value int) string { return strconv.Itoa(value) }

// StopPolling ports stopPolling(): abort, WAIT for the loop to release its
// resources, then mark the runtime idle.
func (r *TelegramRuntime) StopPolling(ctx context.Context, botID string) error {
	r.mu.Lock()
	entry := r.runtimes[botID]
	if entry != nil {
		delete(r.runtimes, botID)
	}
	r.mu.Unlock()
	if entry != nil {
		entry.cancel()
		select {
		case <-entry.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if previous := r.deps.Status.GetRuntimeStatus(botID); previous != nil {
		updated := *previous
		updated.Status = "idle"
		updated.MessageID = "bots.runtime.telegramLongPollingStopped"
		updated.Message = "Telegram long polling is stopped."
		r.deps.Status.SetRuntimeStatus(updated)
	}
	return nil
}

// StopAll ports dispose(): invalidate the refresh queue and wait for every loop.
func (r *TelegramRuntime) StopAll(ctx context.Context) error {
	r.refresh.Invalidate()
	r.mu.Lock()
	r.stoppingAll = true
	entries := make([]*telegramRuntimeEntry, 0, len(r.runtimes))
	for _, entry := range r.runtimes {
		entries = append(entries, entry)
	}
	r.mu.Unlock()
	// Bugfix (TS): 服务销毁返回前必须等长轮询退出并释放 token 锁，
	// 避免新 host 被迫等待下一轮重试。
	for _, entry := range entries {
		entry.cancel()
	}
	var errs []error
	for _, entry := range entries {
		select {
		case <-entry.done:
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
		}
	}
	r.mu.Lock()
	for botID, entry := range r.runtimes {
		for _, active := range entries {
			if active == entry {
				delete(r.runtimes, botID)
				break
			}
		}
	}
	r.mu.Unlock()
	return errors.Join(errs...)
}
