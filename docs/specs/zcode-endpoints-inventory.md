# ZCode / Z.ai / BigModel 外部请求与链接清单（2.0）

状态：2026-11-12 基于 `feature/2.0`（b33df93f）的只读审计，用于决定哪些外部依赖保留、替换或移除。
范围：产品代码（不含测试、`node_modules`、`dist`、文档、锁文件）。每一行都由读代码确认；标注"未验证"的是没有读到调用链的结论。

## 总览

所有 ZCode 后端请求都从 `packages/shared/src/nexEndpoint.ts` 的几个常量派生：

| 常量                                    | 值                                            | 派生的请求                                           |
| --------------------------------------- | --------------------------------------------- | ---------------------------------------------------- |
| `DEFAULT_NEX_ENDPOINT_ORIGIN`           | `https://zcode.z.ai`                          | 绝大多数 `/api/v1/*`：更新、配置、反馈、营销、中继等 |
| `TEST_NEX_ENDPOINT_ORIGIN`              | `https://zcode.z.ai`（与生产相同）            | 测试环境也走生产站                                   |
| `DEFAULT_BIGMODEL_API_ORIGIN`           | `https://bigmodel.cn`                         | 智谱模型 API、CLI 登录换 token                       |
| `DEFAULT_ZAI_OAUTH_ORIGIN`              | `https://chat.z.ai`                           | 仅被 vite 构建期注入，源码里没有读取方               |
| `DEFAULT_ZAI_BUSINESS_BASE_URL`         | `https://api.z.ai`                            | Z.ai 业务 API、Coding Plan                           |
| CDN `cdn-zcode.z.ai` / `cdn.codegeex.cn` | 硬编码在若干文件                              | 官方插件市场、插件图标、远程资产                     |

另有三个不经过上述常量的硬编码域名：`z.ai`（订阅管理页）、`open.bigmodel.cn`（Anthropic 兼容网关）、飞书/Discord（社区，不属于 Z.ai）。

## 按场景分类

### A 账号登录 / OAuth

| 位置                                                                                    | 地址                                                                       | 触发                        | 2.0 状态                         |
| --------------------------------------------------------------------------------------- | -------------------------------------------------------------------------- | --------------------------- | -------------------------------- |
| `apps/nex-cli/packages/adapters/src/auth/cli-oauth.ts:4`                                | `https://zcode.z.ai/api/v1` → `POST /oauth/cli/init`、`GET /oauth/cli/poll` | 用户在 CLI/TUI 执行 `login` | **生效**，是当前唯一的 ZCode 登录入口 |
| `apps/nex-cli/packages/adapters/src/auth/coding-plan-api-key.ts:21,133`                 | `https://api.z.ai` → `POST /api/auth/z/login`                              | CLI 登录 Z.ai Coding Plan   | 生效                             |
| 同文件 `:155`                                                                           | `https://bigmodel.cn` 项目 token                                           | CLI 登录 BigModel           | 生效                             |
| `apps/nex-cli/packages/adapters/src/auth/bigmodel-oauth.ts:51-52`                       | `bigmodel.cn/login`、`/api/auth/tokenByAuthCode`                           | —                           | **死代码**（只有测试引用）       |
| `packages/desktop/src/main/desktopOAuthDeepLink.ts:317,378`                             | —                                                                          | `nex://oauth/...`           | 已禁用，`hasPendingOAuthCallback` 恒为 false |
| `packages/ui/src/Root.tsx:1076`、`root/useRootWorkspaceActions.ts:464`                  | —                                                                          | 登录/登出按钮               | 空函数，UI 上不可达              |

桌面 UI 不再有登录；CLI 里的 `login` 仍然可用。

### B 会话分享

| 位置                                                                              | 地址                                                                   | 触发                          | 2.0 状态                                              |
| --------------------------------------------------------------------------------- | ---------------------------------------------------------------------- | ----------------------------- | ----------------------------------------------------- |
| `packages/services/src/node.ts:2265-2271`                                         | `https://zcode.z.ai/api/v1` 作为分享客户端 base                        | 服务构造                      | 注册为生效服务                                        |
| `conversation-share/conversationShareHttpClient.ts:~229,277,291,301,317`         | `/shares/capabilities`、`/shares/preparations/:id/artifacts`、`/confirm`、`/shares/:code/preview`、`/shares/:code/continuation` | 发布分享、打开/导入分享 | 服务可达，但**分享按钮因 `user` 恒为空而被隐藏**       |
| `conversationShareService.ts:718-722`                                             | `https://zcode.z.ai/cn/share`（或 `NEX_CONVERSATION_SHARE_WEB_URL`）  | 发布成功后生成分享链接        | 同上                                                  |
| `packages/web/src/share/ConversationShareLandingPage.tsx:98,571,710`             | `https://zcode.z.ai`（下载按钮）                                       | 分享落地页点击下载            | 落地页仍可被 `/cn/share/<code>` 打开                  |
| `packages/web-share/src/main.tsx:49-57`、`vite.config.ts`                         | `VITE_NEX_BASE_URL` → `https://zcode.z.ai/api/v1`                      | 独立分享落地页包              | 构建产物仍存在                                        |
| `nex://share/import`（`desktopDeepLinkUrl.ts`、`desktopOAuthDeepLink.ts`、preload） | 深链，不含主机                                                         | 浏览器里点"在 Nex 中继续"     | 生效                                                  |
| `ui/src/i18n/locales/*`：`manualClaimPlan…twitterText`、highspeed 分享文案       | 文案里含 `https://zcode.z.ai`                                          | 点分享到社交媒体（只复制文本，不请求） | Highspeed 卡片仍在渲染；manualClaim 疑似死（未验证）  |

### C 自动更新 / 版本

| 位置                                                                  | 地址                                                                            | 触发                  |
| --------------------------------------------------------------------- | ------------------------------------------------------------------------------- | --------------------- |
| `desktop/src/main/manifestUpdateProvider.ts:22,77-86,185-197,250`     | `https://zcode.z.ai/api/v1/releases/electron/manifest`                          | 启动 / 菜单检查更新   |
| `desktop/src/main/autoUpdater.ts:837-866`                             | 同上 origin；打包版会把其它 origin 重映射回默认                                 | 同上                  |
| `desktop/src/main/forceUpdateGuard.ts:45-53,184-190`                  | `https://zcode.z.ai/api/v1/client/configs`；手动更新打开 `/cn` 或 `/en` 页面    | **打包版生产环境，启动时、主窗口之前** |
| `desktop/src/main/remoteCdn.ts:5-9`                                   | `https://cdn.codegeex.cn/nex/electron/releases`（国内）、`https://cdn-zcode.z.ai/nex/electron/releases`（海外） | 远程 SSH 工作区下载运行时资产 |
| `desktop/src/main/desktopRuntimeEnv.ts:227-236,298-326`               | `NEX_REMOTE_ASSET_CDN_BASE_URL`（`.env.example` 示例指向 cdn-zcode.z.ai）       | 构造 Host 环境        |
| `desktop/electron-builder.config.js:978-987`                          | 发布提供方 `github`（`nex-harness/Nex`）                                        | 打包发布              |

注意：`electron-builder` 的发布源已经是 GitHub，但应用内的更新清单仍然指向 `zcode.z.ai`，两者并不一致，这是更新能力的最大风险点。

### D 远程控制中继 / 设备配对

| 位置                                                                             | 地址                                                                           | 触发                      |
| -------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ | ------------------------- |
| `desktop/src/main/index.ts:776-836`                                              | `wss://zcode.z.ai/api/v1/remote/ws2`、`https://zcode.z.ai/remote/v3\|v4`        | 用户开启 Web 远控 / 扫码  |
| `shared/src/web-remote-relay/contract.ts:119-124`                                | `/api/v1/remote/device`、`/device/delete`、`/token`、WS `/ws2`                  | 配对、取 token            |
| `desktop/src/main/web-remote-relay/webRemoteControlV2Http.ts:21,57`              | 同上                                                                           | 配对开始、通知            |
| `desktop/src/main/webRemoteControlManager.ts:423,1724-1781`                      | `/api/v1/remote/notification/status`、`relayWsUrl`、`mobileRemoteControlUrl`    | 远控状态、二维码          |
| `packages/web/src/main.tsx:774`                                                  | `resolveWebRemoteControlRelayWsUrl`                                            | 浏览器侧远控页            |
| `packages/android/app/build.gradle.kts:24,30`、`RemoteControlUrlPolicy.kt:53,62-66`、`AndroidManifest.xml:57-58` | `https://zcode.z.ai/remote`；白名单只放行 `zcode.z.ai` | Android 远控壳            |

中继配对需要一个 JWT：`desktop/src/main/index.ts:839-853` 读取 `nexjwttoken` 等凭据。2.0 没有账号，所以这条通路对新用户实际取不到凭据（未验证实际运行表现）。

### E 插件市场 / CDN / 内置 Provider 配置

| 位置                                                                                  | 地址                                                                                  | 触发                              |
| ------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- | --------------------------------- |
| `shared/src/plugin-marketplaces.ts:47`                                                | `https://cdn-zcode.z.ai/nex/official-plugin/marketplace.json`                          | 启动种子 / 刷新插件商店           |
| `apps/nex-cli/packages/adapters/src/plugins/marketplace.ts:53-54,611-649`             | `https://cdn-zcode.z.ai/nex/official-plugin/assets/`、`/icon-sources.json`             | 刷新市场时迁移图标，10s 超时      |
| `apps/nex-cli/packages/bootstrap/src/app/official-plugin-definitions.ts:62-63`        | `https://z.ai`（作者）、`…/official-plugin/assets`                                     | 构建/种子                         |
| `ui/src/v4/featureSuggestedPrompts.ts:11`、`officeSuggestedPrompts.ts`（16 处）       | `https://cdn-zcode.z.ai/nex/official-plugin/assets/<name>/icon.png`                    | 渲染推荐提示的图标                |
| `provider-node/src/nex-builtin-download.ts:55-62`                                     | `GET <origin>/api/v1/client/configs` → `builtin_provider_config_json`                  | Provider 运行时启动，之后每 60 秒检查 |
| `shared/src/gen-ui/contract.ts:20-31`、`services/src/gen-ui/adapters/service.ts:65`   | CSP 放行域 `zcode.z.ai`                                                                | gen-ui 页面渲染                   |

### F 模型 Provider / 订阅 / 配置

| 位置                                                                               | 地址                                                                                                  | 触发                                   |
| ---------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- | -------------------------------------- |
| `apps/nex-cli/.../model/official-coding-plan-gateway.ts:8-11`                      | `open.bigmodel.cn/api/anthropic/...`、`api.z.ai/api/anthropic/...` 被改写到 `<origin>/api/v1/ultra*/...` | 用 Coding Plan Provider 发模型请求     |
| `services/src/providers/api/apiEndpoints.ts:3-6`                                   | `<origin>/api/v1/client/scenes`                                                                       | 自动化模板、推荐提示                   |
| `services/src/coding-plan-subscription/codingPlanSubscriptionService.ts:16,47-54`  | `<origin>/api/v1/client/configs`                                                                      | 启动、创建会话                         |
| `services/src/client-config/clientConfigService.ts:69-74`                          | `<origin>/api/v1/client/configs?app_version&platform`                                                 | 插件商店排序、推荐、漏洞上报链接       |
| `services/src/feedback/feedbackService.ts:33-38` + `feedbackHttpClient.ts`         | `<origin>/api/v1/feedback/ticket`、`/feedback/attachment/upload-credential`                           | 用户提交/查看反馈（带 `X-Device-Mid`） |
| `services/src/marketing-touch/marketingTouchService.ts:52-77`                      | `GET /api/v1/marketing/touch`、`POST /api/v1/marketing/touch/action`                                  | 营销弹窗查询/确认                      |
| `shared/src/rewardsEmbedded.ts:24,54`                                              | `https://zcode.z.ai`（测试与生产分支相同）                                                            | 打开 Rewards 页面                      |
| `shared/src/model-provider-family.ts:35,40,45`、`nexEndpoint.ts:271-279`           | `https://z.ai/manage-apikey/subscription`、`bigmodel.cn/coding-plan/team/plans`                       | 点击订阅管理链接                       |
| `services/src/model-provider/legacyNexConfigProviderReader.ts:64-91`               | `https://open.bigmodel.cn/api/anthropic`                                                              | 1.x 配置迁移（保留）                   |
| `desktop/src/main/desktopMainIpcRemote.ts:100`、`desktopWindowChrome.ts:57`        | `api.z.ai`（路径 `/api/pay/paypal/`）                                                                 | 在内嵌 webview 里完成 Coding Plan 付款 |
| `apps/nex-cli/packages/nex-site-plugin/src/server/main.ts:27`                      | `<origin>/api/v1/sites/*`                                                                             | 站点插件面板操作                       |

已知瑕疵：`CODING_PLAN_ZAI_PAYPAL_RELAY_HOSTS` 集合里 `"api.z.ai"` 写了两次，疑似漏掉另一个域名（未确认原意）。`rewardsEmbedded.ts` 的测试分支与生产分支相同。

### G 帮助 / 文档 / 社区 / 反馈

| 位置                                                                  | 地址                                                                              | 触发                         |
| --------------------------------------------------------------------- | --------------------------------------------------------------------------------- | ---------------------------- |
| `desktop/src/main/desktopCommandHandlers.ts:454-474`                  | `https://zcode.z.ai/cn\|en/changelog`                                             | 帮助 → 更新日志              |
| 同文件 `:272-288,588-613` + `desktopHelpConfig.ts:16-27`              | 反馈、社区链接来自 `/api/v1/client/configs`                                       | 帮助 → 反馈/社区             |
| `config/default.json:2,5-6`                                           | 智谱飞书表单、飞书 applink、Discord（兜底配置）                                   | 本地兜底                     |
| `ui/src/lib/productDocs.ts:2`                                         | `https://zcode.z.ai/docs`                                                         | 帮助菜单打开文档             |
| `web/src/communityUrl.ts:25-29`                                       | `VITE_NEX_BASE_URL` → `/api/v1/client/configs`                                    | Web 端帮助菜单               |
| `ui/src/hooks/useVulnerabilityReportUrl.ts:13-15`                     | 来自 client-config 的 `zsrcUrl`                                                   | 帮助 → 安全问题上报          |

### H 遥测

未发现向 Z.ai 主机的上报。`services/src/node.ts:2481-2539` 里三个遥测加载函数没有调用方（死代码）；崩溃捕获只写本地文件。

### I 构建 / 打包 / CI

| 位置                                                         | 地址                                           | 说明                         |
| ------------------------------------------------------------ | ---------------------------------------------- | ---------------------------- |
| `desktop/electron-builder.config.js:579,923`                 | `homepage https://zcode.z.ai`、`dev@zcode.z.ai` | 应用元数据                   |
| `scripts/cdn-preload.sh:14`                                  | 默认 `CDN_DOMAIN=cdn-zcode.z.ai`               | CDN 预热                     |
| `desktop/tsup.config.ts`、`desktop/vite.config.ts`、`web*/vite.config.ts` | 构建期把 endpoint/CDN/OAuth 常量写进产物 | 构建                         |
| `.github/workflows/*`                                        | 没有任何 z.ai / zcode 字符串                   | CI 本身不依赖                |

### J 仅开发/测试

`desktop/wdio.conf.ts`（`open.bigmodel.cn`、OAuth 模拟）、`scripts/dev-web-share-env.mjs`、`scripts/multimodal-tool-result-p0.mjs`、`apps/nex-cli/test-model.mjs`、`.env.example` / `.env.development` / `.env.production`（`NEX_BASE_URL`、`NEX_CDN_BASE_URL`、`BIGMODEL_API_BASE_URL`、`ZAI_*`、`NEX_CONVERSATION_SHARE_WEB_URL`、`NEX_REMOTE_ASSET_CDN_BASE_URL`）。`NEX_CDN_BASE_URL` 没有代码读取方。

## 已确认的死代码（不再有调用方）

1. `bigmodel-oauth.ts` 的 `createBigmodelOAuthClient`（只被测试引用）。
2. `nexEndpoint.ts`：`DEFAULT_WEB_REMOTE_CONTROL_RELAY_WS_URL`、`buildZaiOAuthUrl`、`buildRuntimeZaiOAuthUrl`、`buildRuntimeZaiBusinessUrl`、`resolveRuntimeProductEndpointConfig`、`buildBigModelCodingPlanPersonalManageUrl`、`nexPlan*` 字段。
3. `services/src/providers/api/apiEndpoints.ts:8` 的 `ZAI_API_HOST`。
4. `ui/src/root/oauthCachedSessionRestore.ts`、`useProviderAvailabilityLoginEntryGuard.ts`。
5. `services/src/node.ts:2481-2539` 的三个遥测加载函数。
6. `web/vite.config.ts`、`web-share/vite.config.ts` 注入的 `VITE_ZAI_OAUTH_*`：源码里没有读取方。

## 一次性的关键事实

- 分享：服务和 RPC 通道都已注册，但头部"分享"按钮要求 `user` 非空，而 2.0 里没有任何代码给 `user` 赋值，所以桌面用户看不到入口；打开分享链接（深链/网页）仍然会请求 `zcode.z.ai`。
- 登录：桌面端无登录；CLI `login`/`logout` 仍然完整可用，并且会访问 `zcode.z.ai`、`api.z.ai`、`bigmodel.cn`。
- 更新：打包版启动时会先请求 `zcode.z.ai/api/v1/client/configs` 做强制更新检查（`forceUpdateGuard.ts`，超时 10 秒，响应上限 1 MiB）。请求失败、超时或返回非 2xx 时只记一条 warn 并**跳过校验，应用照常启动**，所以该域名不可用不会阻止启动，只会让启动最多多等 10 秒；反过来，如果该域名仍然在线并返回要求强更的配置，会阻止创建主窗口。
- 不是 ZCode 的外部依赖：飞书、Discord、GitHub 发布、`cdn.codegeex.cn`（智谱 CDN，但不是 z.ai 域名）。
