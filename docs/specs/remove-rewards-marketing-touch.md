# 移除 Rewards 与营销触达（Marketing Touch）（2.0）

状态：实现中。范围：`packages/{shared,services,client,ui,desktop}`、`scripts/`、
`architecture-policy.yaml`、`docs/`、`.agents/skills/feature-boundary-planner`。

## 问题

2.0 已移除 ZCode 账号登录（见 [remove-zcode-login-share-feedback.md](remove-zcode-login-share-feedback.md)），
但仍残留两族只服务于 ZCode 后端的投放功能：

1. **营销触达（Marketing Touch）**：Host 服务轮询 `GET/POST https://zcode.z.ai/api/v1/marketing/touch*`，
   Renderer 由 `MarketingTouchProvider` 负责 Banner、独立弹窗、任务完成刷新、成功/失败结果弹窗与
   `navigate` 动作；Hero 图片/视频/ZIP 经 `CloudContentService` 下载、校验、解压并通过 loopback
   lease 提供给 `<webview>`；E2E 有独立 mock server、资源和故障 fixture 以及覆盖审计脚本。
2. **Rewards（奖励中心 / 邀请好友）**：菜单项与奖励角标、`<webview>` 邀请页
   （`https://zcode.z.ai/{cn,en}/rewards?embedded=app`）、专用 preload 桥（主题/语言/匿名鉴权注入）、
   独立 `persist:nex-rewards` partition、网络采集白名单条目与 E2E fixture。

账号体系移除后两者都拿不到身份，也没有任何本地入口能够产生投放，属于死代码；它们仍会周期性访问后端、
占用独立 session 与一份 ZIP 缓存目录，并让“Rewards/邀请”入口出现在 UI 里。

产品决定（最终）：「Rewards 和营销触达：移除。」

## 产品规则

1. 客户端不再发起任何 `marketing/touch` 查询、上报或资源下载；不存在 Banner、投放弹窗、投放导航
   动作与任务完成触发的投放刷新。
2. 客户端不再有 Rewards / 邀请好友入口、奖励角标、邀请页 `webview`、专用 preload、专用 partition
   与对应网络采集订阅。
3. 公共内容弹窗（`CloudContentDialog`）、Hero 渲染器、`CloudContentService` 及其 ZIP 缓存/lease、
   `cloudContent.ts` 协议在仓库内只有营销触达一个消费者，随营销触达一并删除；协议不再对外导出。
4. 与业务无关的“分享/复制文本”等 i18n 文案若还有其他使用点则保留，只删除随功能一起失去使用点的 key。
5. 已持久化的本地数据保持原位、只读忽略，不做迁移：旧 `persist:nex-rewards` / `persist:nex-coding-plan`
   partition 目录（Rewards 不再注册该 partition，也不再清空它）、旧 `content-bundles` 缓存目录、
   旧设置中的营销/奖励相关键（不存在读取方，也不会再被写入）都留给用户自行清理。

## 移除清单

| 层 | 内容 |
| -- | ---- |
| shared | `marketingTouch.ts`、`rewardsBridge.ts`、`rewardsEmbedded.ts`、`cloudContent.ts` 及其 `index.ts` 导出；`ServiceChannels.CloudContent` / `ServiceChannels.MarketingTouch` |
| services | `marketing-touch/**`（服务、资源注册表）、`cloud-content/**`（服务、ZIP 缓存）、`accessor.ts` 字段、`index.ts` / `node.ts` 注册、`disposeServiceResources*` 中的释放分支 |
| client | `remoteServiceAccess.ts` 中的 `marketingTouchService` / `cloudContentService` 代理 |
| ui | `rewards/**`、`components/marketing-touch/**`、`components/cloud-content-dialog/**`、`marketing-touch-contract/**`、`lib/marketingNavigation.ts`、assets `cloud-content/**`、`types/cloudLottie.d.ts`；`Root.tsx` / `App.tsx` / `SettingsPage.tsx` / `settings/PluginStorePage.tsx` 接线；`styles.css` 媒体关闭按钮规则；`lottie-web` 依赖；i18n `rewards.*`、`marketingTouch.*` 与失去使用点的 `manualClaimPlan.*` |
| desktop | `src/preload/rewardsWebview.ts`；`desktopWindowChrome.ts` 的 Rewards guest 识别/preload/导航守卫；`desktopCommandHandlers.ts` 的 Rewards partition 清理；`resourceManagerNetwork.ts` 的 Rewards session；`tsup.config.ts` / `vite.config.ts` / `scripts/ensure-e2e-desktop-build.mjs` 的 Rewards 入口与 `VITE_REWARDS_WEBVIEW_ORIGIN`；`wdio.conf.ts` 营销/邀请专用分支；`test/e2e/ui-shell/marketing-touch-*.test.ts`、`test/e2e/helpers/marketing-touch-*`、`rewards-website-fixture.ts` 与被删用例独占的 fixture manifest；`scripts/cloud-content-mock.*` |
| scripts | `marketing-touch-coverage-audit.mjs`、`marketing-executable-lines.mjs`、`marketing-source-map-evidence.mjs` 及其 `scripts/test/*.test.mjs` |

## 所有者与接口变化

| 接口 | 变化前 | 变化后 |
| ---- | ------ | ------ |
| DI（`ServiceCollection`） | `IMarketingTouchService`、`ICloudContentService` | 不注册；`accessor` 无对应字段 |
| RPC 通道 | `ServiceChannels.MarketingTouch`、`ServiceChannels.CloudContent` | 删除；远端调用方不再存在 |
| Host 释放链 | `disposeServiceResources*` 调用 CloudContent 的异步释放与缓存清理 | 删除该分支，其余服务释放顺序不变 |
| Electron preload | `preload/rewardsWebview.ts` → `out/preload/rewardsWebview.cjs` | 删除入口与产物 |
| Electron guest 路由 | `will-attach-webview` / `did-attach-webview` 中的 Rewards 分支、`persist:nex-rewards` 会话 | 删除；Coding Plan 与内置浏览器 guest 逻辑不变 |
| 菜单 / 命令 | 无独立 `DesktopCommandId`；邀请入口由 Renderer 菜单项 + Rewards 状态驱动 | 入口与状态一起删除 |
| Renderer 导航 | `lib/marketingNavigation.ts` 请求/确认握手 | 删除；`SettingsPage`、`PluginStorePage` 不再响应投放导航 |
| i18n | `rewards.*`、`marketingTouch.*`、投放专用 `manualClaimPlan.*` | 删除；保留仍被其他入口使用的文案 |
| Test ID | 无随功能新增的 `@nex/shared/test-ids` 常量 | 无变化 |

## 兼容性

- 无数据迁移、无配置 schema 变化；不做版本兼容分支或残留 flag。
- 旧版本客户端向新 Host 查询 `marketing-touch` / `cloud-content` 通道会得到“通道未注册”的调用错误；
  Nex 不支持混用不同版本的 App 与 Host。
- 开发/E2E 环境变量 `VITE_REWARDS_WEBVIEW_ORIGIN` 不再被读取；E2E 构建缓存指纹随之去除该变量。

## 验收场景

1. `pnpm typecheck`、`pnpm lint`、`pnpm architecture:check --changed --base 381cc003` 通过，
   `pnpm --filter @nex/web build` 与 `apps/nex-cli` typecheck 通过。
2. 全仓库（排除文档、`node_modules`、锁文件、`scripts/architecture/upstream-snapshot.json`）内不再出现
   Rewards / marketing-touch 功能标识与 `marketing/touch` 端点。
3. 桌面应用启动后没有营销请求（网络采集与 Host 日志中无 `api/v1/marketing/touch`），
   任务完成、locale 或主题切换都不触发投放刷新；设置页与插件市场不再存在投放导航握手。
4. 清除全部数据（`clear-all-data`）仍只清理主窗口 session；Coding Plan webview 的 partition 清理保持不变。
5. 其余依赖公共弹窗导航的入口（Start Plan 手动领取、Coding Plan 升级页、插件市场）不改变行为。
