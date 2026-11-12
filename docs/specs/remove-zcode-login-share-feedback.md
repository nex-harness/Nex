# 移除 ZCode/Z.ai 账号登录、会话分享与工单反馈（2.0）

后续更新：营销触达与 Rewards 已整体移除，见 [remove-rewards-marketing-touch.md](remove-rewards-marketing-touch.md)。

状态：实现中。范围：`apps/nex-cli`（OAuth/Coding Plan 登录与 `/login`、`/logout`）、
`packages/{shared,services,client,ui,desktop,web,nex-server-cli}`（会话分享发布/导入/落地页、内置工单反馈）、
`packages/{web-share,share-ui}`（独立分享站点）、Highspeed 推广分享卡。

## 问题

2.0 不再绑定 ZCode / Z.ai 账号体系。当前代码里仍残留两族依赖该后端账号的功能：

1. **账号登录**：CLI/TUI 通过 `zcode.z.ai` 的 `/oauth/cli/*` 轮询换证登录 Z.ai / BigModel Coding Plan，
   并用 Z.ai 账号 access token 在 `api.z.ai` 换取项目级 API Key；桌面端仍保留登录入口 props、
   登录态 store 字段与 OAuth deep link 分支；Web 端仍保留设备授权码（`/oauth/cli`）与账号端口。
2. **会话分享**：旁路 `conversation-share` 服务把会话发布到 `zcode.z.ai/cn/share`，桌面/Main/CLI
   通过 `nex://share/import` 和 `nex:share-import` IPC 导入回本地，另有 `web-share` 独立站点与
   Highspeed 推广卡把 `zcode.z.ai` 链接复制到剪贴板。
3. **工单反馈**：应用内 `feedback` 服务把问题提交到 ZCode 托管的工单系统（`config/default.json`
   的 `feedback_url` 指向飞书表单），桌面 Main / Web 平台按远端配置决定打开内置对话框还是外链表单；
   UI 还包含工单列表、附件上传、日志归档上传等完整链路。

产品决定（最终）：「不再有任何与 ZCode 相关的登录或分享，全部移除。」以及
「应用内工单反馈也移除，Help → Feedback 改为打开 GitHub Issues。」

## 产品规则

1. 客户端不再发起任何 ZCode/Z.ai/BigModel 账号登录、登出、token 交换或 OAuth 回调处理；
   CLI 不再提供 `nex login`、`nex logout`、`/login`、`/logout` 命令与 TUI 登录选择面板。
2. 模型 Provider 仍可配置为普通 API Key Provider（用户在设置里粘贴 BigModel/Z.ai 的 API Key）。
   移除的只是「账号 OAuth / Coding Plan 账号换证」路径，Provider 注册表、个人 Provider 与
   `bigmodel` / `z.ai` API Key Provider 保持不变。
3. 客户端不再发布、导入或渲染会话分享：无分享按钮、无分享范围选择、无分享落地页、无分享预览、
   无 `nex://share/import` 深链处理。`nex://share/import` 与其他未知深链一样被忽略。
4. 客户端不再产生把 `zcode.z.ai` 链接复制到剪切板/社交平台的推广分享：移除 Highspeed 左下角
   分享卡、组尾分享弹窗、分享资格判定与分享图片/文案生成；Highspeed 加速本身（选型、倒计时、
   footer 统计、健康检查）保留。
5. 已持久化的数据保持原位、只读忽略：旧 `credentials.json` 中的 `oauth:*` / `nexjwttoken`、
   旧分享会话的 `v4/shared_context_import` 事实、`nex:global-notice:v1` 旧通知都不做数据迁移，
   也不再被新代码读取为登录态或分享状态。

## 移除清单

### 账号登录

| 区域          | 移除内容                                                                                                                                                                                                                                                                           |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| CLI adapters  | `auth/{cli-oauth,bigmodel-oauth,browser,localhost-callback,coding-plan-api-key,zai-business-token-cache}.ts` 及其测试；`shared-credentials.ts` 中仅供登录使用的键与 `saveZaiLoginCredentials` / `clearZaiLoginCredentials`（保留通用凭据 store，MCP OAuth 与个人 Provider 仍在用） |
| CLI bootstrap | `auth-login*.ts`（`loginNexCli`、`loginBigmodelCodingPlan`、`configureCodingPlanApiKey`、`logoutNexCli`）；OAuth 来源的账号 Provider 运行时（`standalone-account-provider-runtime` 中 `oauth` 来源、`x-coding-plan-api-key` 换证、`readNexJwt`）                                   |
| CLI cli       | `login-command.ts`、`tui-auth.ts`、`command-center/login-flow.ts`、`run.ts` 的 `login`/`logout` 分支、`prompt-command.ts` 的 `/login` `/logout` 分支、`command-center` 的登录选择与 `logout` 处理、`provider-runtime-env.ts` 的 `login`/`logout` 命令特判                          |
| CLI 引导态    | TUI 的 `loginRequired` 面板与「没有可用模型」时阻断发送的行为：改为正常提交（由 Provider 注册表给出模型不可用提示）。`tui-login-state.ts` 中仅剩的 `loginRequiredResponse` 删除                                                                                                    |
| UI            | `root/oauthCachedSessionRestore.ts`、`root/useProviderAvailabilityLoginEntryGuard.ts`、`handleOpenLoginEntry` / `handleLogout` 及 `onLogin`/`onLogout` props、快捷入口登录/登出项、store 中仅由登录写入的 `user` / `authSessionSeq` / OAuth 登录态字段与 actions                   |
| Desktop       | `desktopOAuthDeepLink.ts` 中 OAuth 回调分支与 `nex://oauth/callback` 识别；仅为 OAuth 回调存在的 `oauthCachedSession` / `getLogin` 凭据 IPC 路径                                                                                                                                   |
| Web           | 设备授权码登录跳转与账号端口：删除 `INERT_WEB_AUTH_PORT` 及远端能力页对其的依赖，远控入口改为不需要登录态（见「兼容」）                                                                                                                                                            |
| Rewards       | 不再向邀请页注入 OAuth/JWT 凭据（`createRewardsInjectionScript` 的 `credentials` 参数与 `installRewardsPageBridge` 的凭据清理列表）                                                                                                                                                |

### 会话分享

| 区域             | 移除内容                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| shared           | `conversation-share.ts`、`conversation-share-content.ts`、`conversation-share-tool-presentation.ts`；`channels.ts` 的 `ServiceChannels.ConversationShare` 与 `PlatformChannels.ShareImport`（含 payload 映射）                                                                                                                                                                                                                                                                   |
| services         | `conversation-share/**` 全部实现、`IConversationShareService` 描述符、`createUnsupportedConversationShareService`、`accessor.ts` 字段、`index.ts` / `node.ts` 导出与 DI 注册                                                                                                                                                                                                                                                                                                     |
| protocol         | `NexSessionImportHistory` 的 `sharedContext` 分支（只保留 `claudeCode` 导入）；`ConversationInputIntent` 的 `sharedContextRefs` / `discardSharedContext`；`ConversationSnapshot` 的 `sharedContextImport`；CLI 侧对应的导入写入、ref 校验与 snapshot 恢复                                                                                                                                                                                                                        |
| client / desktop | `RemoteServiceAccess.conversationShareService` 代理、Host 频道注册与 attachment 作用域包装、远程 workspace 服务集合与 RPC 契约、RPC 策略白名单                                                                                                                                                                                                                                                                                                                                   |
| desktop main     | 深链 `nex://share/import` 路由、pending share 导入队列与 preload 缓冲、`nex://share/import` 在 argv/单实例参数中的完整性校验、`NEX_CONVERSATION_SHARE_WEB_URL`                                                                                                                                                                                                                                                                                                                   |
| UI               | `ConversationShareMenu`、`ConversationSharePermissionPicker`、`ConversationShareLinkRow`、`conversation-share-interaction/**`、`ConversationShareConfirmationDock`、`ConversationShareSelectionPanel*`、`ConversationShareSuccessDock`、`ConversationShareImportNotice`、只读分享时间线、`conversationShareSelectionStore`、Header 分享按钮、SessionPane/Composer/Timeline 分享接线、`onShareImport` 平台效果、`IPlatformService.onShareImport` 及 Web/Desktop 实现与 preload 桥 |
| Web              | `packages/web/src/share/**` 与 `main.tsx` 的 `/cn/share`、`/share` 路由分发；`packages/web-share`、`packages/share-ui` 两个包                                                                                                                                                                                                                                                                                                                                                    |
| Highspeed 推广   | `HighspeedShareNoticeCard` / `HighspeedShareDialog`、`highspeedShare.ts`、`highspeedShareDiagnostics.ts`、`highspeedToastClaims.ts`、`global-notice/**`、`assets/highspeed/**`、组尾与左下角的分享入口；`chat.highspeed.share*`、`globalNotice.highspeed.*` i18n                                                                                                                                                                                                                 |

### 工单反馈

| 区域              | 移除内容                                                                                                                                                                                                                                                                                                                                                              |
| ----------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| services          | `feedback/**`（`feedbackService`、`feedbackHttpClient`、`feedbackLocalTicketStore`、`feedbackLogArchive`、`compactLogArchive`、`feedback.ts`）、`IFeedbackService` 描述符、`createUnsupportedConversationShareService` 外的 DI 注册与 accessor 字段、`index.ts` / `node.ts` 导出、`getFeedbackRootDir` / `getFeedbackAttachmentDir` / `getFeedbackLogArchiveDir` 路径 |
| shared            | `feedback.ts` 工单类型与 schema、`feedbackPrivacy.ts`、`ServiceChannels.Feedback`、`OpenFeedbackDialog` / `OpenTicketsPanel` IPC 频道、`FeedbackLogArchiveRequest` / `FeedbackLogArchiveResult` host 通道与 zod 校验、`feedback_url` / `feedback_use_external_form` / `feedback_api_base` 配置读取                                                                    |
| desktop main/host | Host 的反馈服务注册、反馈日志归档委托（`createFullFeedbackLogArchiveViaMain`、`pendingFeedbackLogArchiveRequests`、Main 侧 `createFeedbackLogArchiveFromExportLogs`）、`FeedbackLogArchive*` host 消息处理；`exportLogs`（Help → Export Logs）保持可用                                                                                                                |
| desktop bridge    | preload 的 `onOpenFeedbackDialog` / `onOpenTicketsPanel`、`desktopPlatform` 对应实现、`desktopCommandHandlers` 的飞书表单分支                                                                                                                                                                                                                                         |
| UI                | `packages/ui/src/feedback/**`（反馈中心、工单列表/详情、提交表单、附件上传、后台提交指示、截图选择、草稿与提交任务）、`FeedbackHost`、`feedbackStore` 及其在任务菜单、错误横幅、远程连接失败面板、Quick Pick 中的提交入口、`errorFeedbackDraft` / `taskFeedbackDraft`、`TID_FEEDBACK_LOGS_OPT_IN` test id                                                             |
| Web               | `createWebPlatform` 的 `resolveFeedbackUrl`，改为直接打开 GitHub Issues                                                                                                                                                                                                                                                                                               |
| 配置              | `config/default.json` 的 `feedback_url` / `feedback_use_external_form`                                                                                                                                                                                                                                                                                                |
| E2E               | `feedback-upload-privacy.test.ts`、`help-client-config` 的 HC-03 反馈弹窗用例、相关 fixture 字段                                                                                                                                                                                                                                                                      |

### 反馈入口替换

- 新增唯一常量 `NEX_GITHUB_ISSUES_NEW_URL = "https://github.com/nex-harness/Nex/issues/new"`
  （`packages/shared/src/productLinks.ts`）。
- Desktop `DesktopCommandIds.OpenFeedback` → `shell.openExternal(NEX_GITHUB_ISSUES_NEW_URL)`；
  Web `IPlatformService.openFeedback` → `window.open(..., "noopener,noreferrer")`。
- Help 菜单与错误横幅、远程连接失败面板的反馈按钮都走同一常量；i18n 文案改为「Report on GitHub /
  在 GitHub 反馈」，不再暗示内置工单与"我的反馈"。
- 本地既存的工单/附件/日志归档目录（`<dataRoot>/feedback/**`）保留在磁盘上不再读取，也不删除。

### 随移除变为无引用的端点常量

`packages/shared/src/nexEndpoint.ts`：`webShareCallbackUrl`、`nexPlanOpenAiBaseUrl`、
`nexPlanAnthropicBaseUrl`、`nexPlanBillingCurrentUrl`、`nexPlanBillingBalanceUrl`、
Z.ai OAuth origin/client id 解析与 `buildRuntimeZaiOAuthUrl`、Z.ai 业务域 `buildRuntimeZaiBusinessUrl`
（若确认无引用）、个人 Coding Plan 管理页 URL 构造器，以及 `packages/web/vite.config.ts` 中对应的
`import.meta.env.VITE_*` define。仍有引用的常量保留。

## 保留（不属于本次移除）

- 自动更新、manifest、强制升级守门、远端 CDN 资源与 `nexEndpoint` origin 解析。
- Web 远控 relay 与设备配对（`web-remote-relay`、`web-remote-control`、Android）。relay 配对若仍需
  JWT 供应商，保留该供应商并在报告中标注其依赖已移除的登录。
- 官方插件市场/CDN、内置 Provider 配置下载、client-config / scenes / feedback / marketing-touch /
  coding-plan-subscription 服务、rewards webview 页面本身、nex-site-plugin、帮助/文档/社区链接。
- 自建 Nex Server 的 Web 登录（`webAuth`/`webAccessToken`/`serveConfig`/`staticWeb`、`WebLoginPage`、
  `webAuthSession`、`docs/specs/server-web-login.md`、`webLogin.*` i18n）。
- 1.x 升级读取器（`legacyAccountConnectionSettings.ts`、`legacyNexConfigProviderReader.ts`）。

## 状态所有者与接口

| 状态 / 接口                   | 变化                                                                                                                                                                                                                   |
| ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 服务 DI（`IServiceAccessor`） | 删除 `conversationShareService`；其他服务不变                                                                                                                                                                          |
| RPC 频道                      | 删除 `conversation-share`（`ServiceChannels.ConversationShare`）                                                                                                                                                       |
| IPC / preload                 | 删除 `nex:share-import`（`PlatformChannels.ShareImport`）与 `IPlatformService.onShareImport`                                                                                                                           |
| Deep link                     | `nex://share/import` 不再被识别；`nex://workspace/open`、`nex://payment/callback` 不变；`nex://oauth/callback` 识别分支删除                                                                                            |
| nex-protocol                  | `importedHistory` 只保留 `claudeCode` 来源；`ConversationInputIntent` 删除 `sharedContextRefs`；`ConversationSnapshot`/delta 删除 `sharedContextImport`。CLI 的 zod 校验与 shared 类型同步删除，双方启动握手仍严格校验 |
| CLI 命令面                    | 删除 `nex login` / `nex logout` 与 `/login` / `/logout`；`/login` 变为未知命令提示                                                                                                                                     |
| i18n                          | 删除分享与 Highspeed 分享相关键、CLI 的 `tui.loginSetup` / `tui.loginRequired`；仅删除确认无引用的键                                                                                                                   |
| test ids                      | 删除分享相关的 `@nex/shared/test-ids` 条目                                                                                                                                                                             |

事件顺序（分享，移除后）：不存在发布/导入链路；`nex:` 深链只路由 workspace 打开与支付回调。

## 兼容与迁移

- 已存在的 `credentials.json` 键（`oauth:*`、`nexjwttoken`）不删除、不读取；用户如需改用 API Key，
  在 Provider 设置中自行配置。
- 已存在的 `v4/shared_context_import` 会话事实与会话行仍可读取（行渲染按普通文本行处理），
  但不再有只读分享块、导入提示或「discard」动作。
- `nex://share/import` 深链从外部浏览器打开时被忽略（与未知深链一致），不再导入。
- Web 远控入口不再要求登录态：`WebRemoteControlEntry` 不再通过 `auth` 端口判断登录；设备列表与
  配对改用 relay 自身的设备凭据流程（relay 侧另行处理）。
- assistant 数据里的 `source: "sharedContext"` 历史导入记录不再被解析为分享导入。
- 本地已存在的工单与附件目录（`<dataRoot>/feedback/**`）不再被读取或清理，等于只读遗留数据；
  旧 `feedback_url` 远端/本地配置键被忽略。

## 验收

1. `pnpm typecheck` 通过；`pnpm lint` 0 error 且 warning 数不超过基线 69；`pnpm architecture:check --changed --base 9bcd529f` 无新增违规。
2. `pnpm --filter @nex/web build` 成功，构建产物中不含 `/cn/share` 入口。
3. `apps/nex-cli`：`pnpm --dir apps/nex-cli run typecheck` 通过；改动的包 `pnpm exec vitest run tests` 通过。
4. `git grep`（排除 `*.md`、`docs/`、`node_modules`、lockfile）不再出现
   `ConversationShare`、`conversation-share`、`nex:share-import`、`ShareImport`、`cn/share`、
   `cli-oauth`、`coding-plan-api-key` 登录换证解析器、`bigmodel-oauth`、`HighspeedShare`、
   `IFeedbackService`、`FeedbackHost`、`feedback_url`。
5. CLI 交互：`nex login` 报「未知命令」；TUI 没有登录选择面板，`/login` 走未知命令提示；模型不可用时
   提交不再被登录门阻断。
6. Desktop 冷启动/二次实例传入 `nex://share/import?code=x` 时窗口正常打开、无导入动作、无报错。
7. Desktop Help → Feedback（菜单命令 `openFeedback`）与 Web 反馈入口都只在系统浏览器打开
   `https://github.com/nex-harness/Nex/issues/new`；Help → Export Logs 仍能打包并定位日志。
