# Server Web 登录与直连托管（2.0）

状态：实现中。范围：`packages/nex-server-cli`（Core HTTP ingress、`serve` / `web-token` 命令）与
`packages/web`（登录页与启动门禁）。1.x 的 `packages/server/webAuth`（见 1.2.3
`docs/specs/web-token-login.md`）是行为参考；2.0 的 Core 是新实现，不继承旧进程语义。

## 问题

2.0 不需要桌面端也能运行：`nex serve`（前台）或 `nex serve --daemon` 即可拉起 Supervisor 与 Core。
但这个 Server 目前无法被浏览器直接使用：

1. Core 的鉴权只有每次启动轮换的 **ingress token**（`run/ingress-token`，Bearer 或 `?token=`）。
   浏览器不持有它，页面能不能打开取决于有没有人在前面反代并注入 token（Docker 里的 nginx）。
2. Core 不托管 Web UI。
3. Core 对非回环监听直接 fail-closed，所以无法被局域网/公网访问。

## 产品规则

1. 默认行为不变：不配置 Web 根目录时，Core 只提供 API/WS，行为与现在一致。
2. 提供 Web 根目录（`--web-root <dir>` 或 `NEX_SERVER_WEB_ROOT`）时，Core
   直接托管该目录的静态资源，非文件路径回落 `index.html`（SPA）。`/api/*`、`/ws*` 永不回落。
3. 托管 Web UI 时必须有登录凭据 **web access token**：
   - 来源优先级：`NEX_SERVER_WEB_TOKEN` 环境变量（至少 12 个字符，否则 Core 启动失败）>
     `<serverRoot>/web-access-token` 文件。
   - 两者都没有时 Core 首次启动生成 32 字节随机 token 写入该文件（0600，原子写）。
   - `nex web-token` 打印当前 token（文件缺失则创建）；`nex web-token --rotate` 重新生成，
     需要 `nex restart` 生效，旧会话随之失效。
   - 与 ingress token 是两个凭据：ingress token 每次启动轮换且只给桌面/SSH/反代这类程序化入口；
     web access token 是用户记得住、可长期使用的登录凭据。不得互相替代。
4. 浏览器没有有效会话时，`/api/*`、`/ws*` 返回 401，Web 跳 `/login?next=<原路径>`；
   `/login` 只有一个输入框（access token），成功后整页跳转到 `next`（默认 `/`）。
5. 应用运行中会话失效（过期、token 轮换、会话文件被删）回到 `/login`；普通网络抖动不视为登出。
6. 已通过 ingress token（Bearer / `?token=`）的程序化请求不需要会话，行为与现在完全一致，
   因此 Docker 的 nginx 注入模式、桌面远控、SSH 隧道均不受影响。
7. `next` 只接受同源相对路径（`/…`，不能 `//…`、不含 scheme、反斜杠、控制字符、不能是 `/login`）。
8. 监听地址：`--host` / `NEX_SERVER_LISTEN_HOST`、`--port` / `NEX_SERVER_LISTEN_PORT`（刻意不用
   `NEX_SERVER_PORT`：Docker 镜像用它表示 nginx 对外端口，Core 同名读取会与 nginx 抢端口）。默认仍是 `127.0.0.1` +
   动态端口。**非回环监听会自动启用 Web 登录**（即使没有 `--web-root`，也会生成/读取 web access
   token），HTTP 层对「非回环却没有任何登录方式」的组合直接拒绝监听，不降级为无鉴权。
9. 运行参数持久化：`serve` 带 `--host/--port/--web-root` 时写入 `<serverRoot>/serve-config.json`
   （0600），Core 启动时读取，环境变量覆盖文件。这样 OS service、`restart`、`update` 后的
   Core 重启都沿用同一组参数，不依赖注册时的 shell 环境。Server 已在运行时带这些参数的
   `serve` 会报错并提示 `nex restart`，不静默忽略。
10. Core 不终止 TLS。非回环对外暴露时 Cookie 带 `Secure` 的条件是请求经 HTTPS（或可信反代的
   `X-Forwarded-Proto: https`）；明文 HTTP 上 token 会被网络路径看到，文档与 CLI 输出必须提示
   「对公网暴露请放在 TLS 反代之后」。登录限流按连接对端地址计，不信任 `X-Forwarded-For`。

## 工作区身份（直连 Server 的 Web）

浏览器直接使用 Server 时，只有一个 Server，工作区不需要用 `remote:server:<id>:<path>` 来区分归属。
Web 在 `/api/server-info` 的 `workspaces[0]` 上建的初始工作区 tab 因此**不携带** `workspaceIdentity`，
与侧栏任务行（只有 `workspacePath`）保持同一个身份。

- 会话状态的 key 是 `workspaceIdentity?.trim() || workspacePath`。同一个目录在 tab 上带 identity、在任务行上不带，
  就会得到两个桶：点击会话把 `activeTaskId` 写进无 identity 的桶，界面却按 tab 的 identity 读另一个空桶，
  结果停在新任务首页，并且订阅走带 identity 的路径，被服务端按 `existing-only` 拒绝
  （`Nex Agent runtime is not running.`）。
- 只有排在 `workspaces[0]` 的项目会触发，换项目顺序后换另一个项目受影响。
- `/api/server-info.workspaces[].workspaceIdentity` 仍由 Server 返回，桌面把该 Server 当作远端设备连接时继续使用它；
  Web 直连不使用它。

## 状态所有者

| 状态                        | 所有者                                     | 读取方                     |
| --------------------------- | ------------------------------------------ | -------------------------- |
| web access token            | env 或 `web-access-token` 文件（Core 读取）| Core `webAuth`、`web-token`|
| 登录会话                    | Core `webAuth` → `<serverRoot>/web-sessions.json` | Core 中间件、auth 路由 |
| ingress token               | Core 启动时生成（不变）                    | Core 中间件、Supervisor    |
| 「浏览器是否已登录」        | `GET /api/auth/session` 的响应             | Web 启动门禁、登录页       |

浏览器从不保存 token：Cookie 只含随机 session id。

## 会话存储

- 文件 `<serverRoot>/web-sessions.json`，0600，临时文件 + rename 原子写，全部异步 IO。
- 形状 `{ version: 1, tokenFingerprint, sessions: [{ idHash, createdAt, expiresAt }] }`。
  `idHash = sha256(sessionId)`，session id 不落盘；`tokenFingerprint = sha256("nex-web-session:"+token)`，
  token 变化后加载时丢弃全部旧会话。
- session id 32 字节随机 base64url；有效期自创建起 30 天（绝对，不滑动续期）；最多保留 100 个。
- 缺失/不可读/损坏一律当作「没有会话」（fail closed），不会因此关闭鉴权。

## HTTP 接口

| 方法 | 路径                | 行为                                                                              |
| ---- | ------------------- | --------------------------------------------------------------------------------- |
| GET  | `/api/auth/session` | `{ authRequired, authenticated }`，始终 200，不走鉴权中间件。                     |
| POST | `/api/auth/login`   | body `{ token }`；成功 200 + `Set-Cookie`，不匹配 401，限流 429，坏请求 400。      |
| POST | `/api/auth/logout`  | 删除当前会话并清 Cookie，始终 200。                                               |

- `authRequired` 在 Web 登录启用时为 true；`authenticated` 为「携带有效会话 **或** 有效 ingress token」。
- Cookie：`nex_session=<id>; Path=/; HttpOnly; SameSite=Lax; Max-Age=2592000`（HTTPS 时加 `Secure`）。
- 登录限流：同一对端地址连续失败 5 次锁 60 秒（429 + `Retry-After`）。
- 受保护路径：`/ws`、`/ws/*`、`/api/*`（`/api/auth/*` 除外）。`/ws/host` 仍额外要求 host capability。

## 事件顺序

```text
浏览器 ──GET /──────────────────────▶ Core（静态 index.html，不鉴权）
浏览器 ──GET /api/auth/session─────▶ Core  {authRequired:true, authenticated:false}
Web   ──location.replace('/login?next=/…')
浏览器 ──POST /api/auth/login {token}▶ Core  比对 web access token → 创建会话 → 落盘 → Set-Cookie
Web   ──location.replace(next)       （整页跳转，丢弃登录前失败的 WS 状态）
浏览器 ──GET /api/server-info + WS /ws（带 Cookie）▶ Core 中间件：会话有效 → 放行
```

## 失败语义

- 会话落盘失败：登录返回 500，不签发 Cookie（避免「刚登录重启就失效」）。
- web access token 不合规（环境变量过短、文件不可读写）：Core 启动失败，Supervisor 按既有 Core 崩溃预算处理并报告原因。
- 静态根目录不存在或缺 `index.html`：Core 启动失败，不静默退化成 API-only。
- 登出后旧 Cookie 立即失效。

## 验收

1. 隔离数据目录下 `nex serve --web-root <dist> --port <p>`：`GET /` 返回页面；
   无 Cookie 访问 `/api/server-info` 只返回受限信息，`/ws` 401。
2. 错误 token 登录 401，连续 5 次后 429；正确 token 登录后 `/api/auth/session` 为已登录，
   `/ws` 可握手。
3. 重启 Server 后会话仍有效；`nex web-token --rotate` + `nex restart` 后旧会话失效。
4. 带 Bearer ingress token 的请求不受影响（nginx/桌面路径回归）。
5. `--host 0.0.0.0` 自动启用登录：未登录的 `/api/*`、`/ws` 为 401；`NEX_SERVER_WEB_TOKEN`
   短于 12 个字符时 Core 启动失败。
6. 浏览器实测：未登录访问 `/` 跳登录页，登录后进入应用，登出/会话丢失后回登录页。
7. 直连 Server 的 Web（至少两个项目）：每个项目下的历史会话都能打开并渲染正文；把任意项目调到第一位后，
   原来排第一的项目仍可打开，侧栏第一个项目的会话也不再停在新任务首页。会话状态只出现在无 identity 的桶里
   （`/api/server-info` 返回的 `workspaceIdentity` 不进入 Web 的 tab 与会话状态）。
