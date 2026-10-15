package bots

import "context"

// telegram_runtime_start.go holds the lifecycle half of the Telegram polling
// runtime: reconciling the running loops against the config, starting a loop,
// and stopping it.
//
// The refresh queue (LatestRefreshQueue) is what makes a burst of config saves
// safe: each reconcile waits for the previous one to release its connection, and
// a stale generation returns without touching provider state.
// activeTelegramBots are the bots the loop should be polling.
func activeTelegramBots(config ConfigFile) []Config {
	var active []Config
	for _, bot := range config.Bots {
		if bot.Provider == ProviderTelegram && bot.Enabled && bot.CredentialRef != "" {
			active = append(active, bot)
		}
	}
	return active
}

// Refresh ports refresh(): reconcile the running loops against the config.
func (r *TelegramRuntime) Refresh(ctx context.Context, config *ConfigFile) error {
	return r.refresh.Enqueue(ctx, func(isLatest func() bool) error {
		return r.reconcile(ctx, config, isLatest)
	})
}

// ScheduleRefresh ports scheduleRefresh(): fire-and-forget, suppressed entirely
// when background tasks are disabled.
func (r *TelegramRuntime) ScheduleRefresh(ctx context.Context, config *ConfigFile) {
	if !r.deps.RunBackgroundTasks {
		// 修复原因：配置变更后的轮询刷新也是 bot runtime 后台任务；
		// attached remote 不能绕过构造期保护启动 Telegram polling。
		return
	}
	go func() {
		if err := r.Refresh(ctx, config); err != nil && r.deps.Logger != nil {
			r.deps.Logger.Warn("", "refresh Telegram polling failed: "+err.Error())
		}
	}()
}

// reconcile ports reconcile().
func (r *TelegramRuntime) reconcile(ctx context.Context, config *ConfigFile, isLatest func() bool) error {
	if r.deps.EnsureStorageMigrated != nil {
		// Bugfix (TS): polling 游标和微信 buf 会写入 bot-state.v3.json.
		// 启动轮询前必须先完成旧 state 迁移，否则迁移写回可能覆盖刚更新的第三方游标。
		if err := r.deps.EnsureStorageMigrated(ctx); err != nil {
			return err
		}
	}
	currentConfig := ConfigFile{}
	if config != nil {
		currentConfig = *config
	} else {
		read, err := r.deps.ReadConfig(ctx)
		if err != nil {
			return err
		}
		currentConfig = read
	}
	if !isLatest() {
		return nil
	}
	active := map[string]bool{}
	for _, bot := range activeTelegramBots(currentConfig) {
		active[bot.ID] = true
	}
	r.mu.Lock()
	var staleIDs []string
	for botID := range r.runtimes {
		if !active[botID] {
			staleIDs = append(staleIDs, botID)
		}
	}
	r.mu.Unlock()
	for _, botID := range staleIDs {
		if err := r.StopPolling(ctx, botID); err != nil {
			return err
		}
		if !isLatest() {
			return nil
		}
	}
	for _, bot := range currentConfig.Bots {
		if bot.Provider != ProviderTelegram {
			continue
		}
		if bot.Enabled && bot.CredentialRef != "" {
			if err := r.syncCommands(ctx, bot); err != nil {
				if r.deps.Logger != nil {
					r.deps.Logger.Warn("", "sync Telegram commands failed bot="+bot.ID+": "+err.Error())
				}
			}
			if !isLatest() {
				return nil
			}
			fingerprint, err := r.connectionFingerprint(ctx, bot)
			if err != nil {
				return err
			}
			if !isLatest() {
				return nil
			}
			r.mu.Lock()
			existing := r.runtimes[bot.ID]
			r.mu.Unlock()
			if existing != nil && existing.fingerprint != fingerprint {
				// Bugfix (TS): Bot id 不变不代表连接身份不变。必须等旧 token 的轮询和锁
				// 完全退出，再启动新凭据，避免配置已更新但后台仍消费旧账号。
				if err := r.StopPolling(ctx, bot.ID); err != nil {
					return err
				}
				if !isLatest() {
					return nil
				}
			}
			r.startPolling(bot, fingerprint)
			continue
		}
		if !bot.Enabled {
			if err := r.syncCommands(ctx, bot); err != nil && r.deps.Logger != nil {
				r.deps.Logger.Warn("", "sync Telegram commands failed bot="+bot.ID+": "+err.Error())
			}
			r.deps.Status.SetRuntimeStatus(RuntimeInfo{
				BotID: bot.ID, Provider: bot.Provider, Status: "disabled",
				MessageID: "bots.runtime.botDisabled", Message: "Bot is disabled.",
			})
		}
	}
	return nil
}

func (r *TelegramRuntime) syncCommands(ctx context.Context, bot Config) error {
	if !r.deps.RunBackgroundTasks {
		// 修复原因：desktop-attached 远端不拥有 Telegram runtime；删除或禁用 bot
		// 时也不能为了清命令访问第三方 API。
		return nil
	}
	return r.deps.Provider.SyncCommands(ctx, bot)
}

// startPolling ports startPolling().
func (r *TelegramRuntime) startPolling(bot Config, fingerprint string) {
	r.mu.Lock()
	if _, exists := r.runtimes[bot.ID]; exists {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	entry := &telegramRuntimeEntry{cancel: cancel, fingerprint: fingerprint, done: make(chan struct{})}
	r.runtimes[bot.ID] = entry
	r.mu.Unlock()

	r.deps.Status.SetRuntimeStatus(RuntimeInfo{
		BotID: bot.ID, Provider: ProviderTelegram, Status: "polling",
		MessageID: "bots.runtime.telegramLongPollingStarting", Message: "Telegram long polling is starting.",
	})
	go func() {
		defer close(entry.done)
		// Bugfix (TS): 后台 runtime 的最终 goroutine 必须显式收口，避免 panic
		// 升级为进程级崩溃。
		defer func() {
			if recovered := recover(); recovered != nil && r.deps.Logger != nil {
				r.deps.Logger.Warn("", "Telegram polling stopped unexpectedly bot="+bot.ID)
			}
			r.mu.Lock()
			if current := r.runtimes[bot.ID]; current == entry {
				delete(r.runtimes, bot.ID)
				if previous := r.deps.Status.GetRuntimeStatus(bot.ID); previous != nil && previous.Status == "polling" {
					r.deps.Status.SetRuntimeStatus(RuntimeInfo{
						BotID: bot.ID, Provider: ProviderTelegram, Status: "idle",
						MessageID: "bots.runtime.telegramLongPollingStopped",
						Message:   "Telegram long polling is stopped.",
						Offset:    previous.Offset,
					})
				}
			}
			r.mu.Unlock()
		}()
		r.pollBot(ctx, bot)
	}()
}
