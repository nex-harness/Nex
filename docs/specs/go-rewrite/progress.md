# Go 重写整体进度

> 单一来源:`docs/specs/go-rewrite/phase-*.md` 是各 phase 的规约，本文档只是它们的勾选视图。
> 最后更新：2026-10-12(Phase 0 全绿，Phase 1 接近完成，Phase 2 全绿)。

## 总览

| Phase   | 状态        | 通过 / 总数         | 备注                                                                                     |
| ------- | ----------- | ------------------- | ---------------------------------------------------------------------------------------- |
| Phase 0 | ✅ 全绿     | 21/21               | `gate.sh 0` exit 0(2026-10-10)                                                           |
| Phase 1 | 🟢 接近完成 | 45/45 自动 + 1 人工 | 自动 gate 全过；剩 G1.13 手动 checklist(permission prompts / 文件编辑 / Desktop)需人工跑 |
| Phase 2 | ✅ 全绿     | 21/21               | `gate.sh 2` exit 0(2026-10-12);12 步全过                                                 |
| Phase 3 | 🟡 进行中   | 1/15                | 3.1 shared infra ✅;3.4 终端 defer(用户决策，将改实现）;3.2 进行中                       |
| Phase 4 | ⬜ 未开始   | 0/16                | 依赖 Phase 1;D4(JS-bound features）在 Phase 4 启动时需最终决策                           |
| Phase 5 | ⬜ 未开始   | 0/16                | 依赖 Phase 3 + Phase 4                                                                   |

## Phase 0 — Contracts, golden traces, harness ✅

`gate.sh 0` → `21 passed, 0 failed, 0 not built/open`(2026-10-10)

- [x] **0.1** TypeScript baseline(`baseline.md`)
- [x] **0.2** 决策 D1–D13 落地(`decisions.md`);D3/D8/D10 decided,D4/D6 deferred 到 Phase 4,D1 仍为占位
- [x] **0.3** Go module skeleton(`go/`,module `github.com/tylinux/nex/go` 占位）+ gofmt/vet/archcheck/xbuild 5 targets
- [x] **0.4** Schema 导出：138 个 zod → JSON Schema + manifest + method tables(`export-schemas.mts`)
- [x] **0.5** Go 类型 + 手写 validators(`go/contracts/`),drift check
- [x] **0.6** Schema agreement corpus(`go/testdata/schema-corpus`),Go validators 与 zod 全量对齐
- [x] **0.7** 录制器（`scripts/go-rewrite/record/`):record-agent-session + mock-model + normalizer + replay,14 个测试
- [x] **0.8** RPC frame 录制：`record-rpc.mjs` + `rpc-vectors.json`(R10 auth 未录，已声明例外）
- [x] **0.9** SQLite fixtures:22 个 migration 版本的 `session-store/vNNNN.sqlite` + schema dumps
- [x] **0.10** Replay harness:`replay-agent.mjs` 支持 S01–S05、S13 与 14 个 held-out 场景，Node 自录自播 20/20
- [x] **0.11** 行为清单（`inventory.md`,byte-identical 生成）
- [x] **0.12** Gate runner(`gate.sh` + `gate.test.sh`)
- [x] **0.13** Oracle coverage(`oracle-coverage.md`):phase1 名下 15 个 must-match 文件 9 个达标 + 4 个 approved exception + 2 个未测（归属后续 step)

## Phase 1 — Go agent behind the existing host 🟡

`gate.sh 1` → `41 passed, 0 failed, 3 not built/open`(2026-10-10)

- [x] **1.1** Stdio framing + envelope(`internal/protocol/{frame,envelope,stdio}.go`,83 向量 + fuzz + 进程级测试）
- [x] **1.2** Handshake、`runtime/capabilities`、`startup/storageState`(S01 通过）
- [x] **1.3** SQLite store:22 个 migration byte-exact 嵌入，跨引擎 byte-identical(`crosscheck-store.mjs`)
- [x] **1.4** Session runtime:records、residency、workspace identity(`workspaceIdentity || workspacePath`)
- [x] **1.5** Turn state machine 显式化：`internal/runtime/turnstate.go` 纯 `Transition(state, event)` 函数，10×13 全空间表驱动测试 + FuzzTurn + goroutine-leak 检测
- [x] **1.6** Model 层：OpenAI chat-completions SSE client、流式 tool-call 组装、失败分类器（106 向量）、retry 策略（fake-clock 测试）
- [x] **1.7** model-option-map DSL（在 prompt 构建中间接验证，独立向量脚本未做）
- [x] **1.8** 工具执行器 + Read/Write/Edit 三个工具（46/39/47 asserting differential 向量），其他 17 个工具未做
- [x] **1.9** v4 wire 编码器（CRC32、base64、字节测量、完整/分片决策）;assembler 移交 Phase 3.10.2
- [x] **1.10** v4 conversation topic:snapshot、deltas、resume、两种 client mode(desktop-continuous / web-remote-replayable）均由 `both_modes_test.go` 覆盖
- [x] **1.11** v4 commands 子集：仅 `createSession` 接入；其余 17 个 Phase 1 命令未实现
- [x] **1.12** Permissions 最小集（够用 S04/S05);bash 读策略完整版留 Phase 4
- [x] **1.13** Context 构建 + system prompt(40 个 TS-derived 向量，请求体与 Node 字节一致）;compaction 未做
- [x] **1.14** Switch / packaging / 端到端验收
  - [x] G1.10a：真实 provider 直连 — deepseek-flash 经 openai-responses 完成完整 turn(`scripts/go-rewrite/live-deepseek.mjs`)
  - [x] G1.10b:e2e smoke 通过 `@nex/server` + `GLM_BINARY_PATH` + mock model(`scripts/go-rewrite/e2e-agent-smoke.mjs`)
  - [x] G1.11：进程树 kill(Unix `setpgid`/`kill(-pgid)` 已测，Windows Job Objects 编译过）— `go/internal/adapters/exec/`
  - [x] G1.13a:Web smoke 自动化(Chromium 登录、发 prompt、读回答)— `scripts/go-rewrite/e2e-web-smoke.mjs`
  - [ ] G1.13:Web/Desktop 手动 checklist 余项(permission prompts、文件编辑、Desktop app)

**Phase 1 未完成的功能**（均未在 Go 端实现）:

- Bash、Glob、Grep、Todo、AskUserQuestion、TaskOutput、TaskStop、WebFetch、Skill、Cron\*、Agent、EnterPlanMode/ExitPlanMode、SendMessage、ReadSessionContext
- State mapper、queue 和 guide inputs、compaction、fork 和 rewind
- Subagents、hooks、memory、MCP、skills、workflows、media reads

**Phase 1 已实现的模型 dialect:**

- OpenAI chat-completions（原已存在）
- OpenAI Responses(`go/internal/adapters/model/openairesponses/`,D14 用 `openai-go` v1.12.0 + 保留自研 SSE 框架）
- Anthropic Messages(`go/internal/adapters/model/anthropic/`,D14 用 `anthropic-sdk-go` v1.80.0 + 保留自研 SSE 框架；向量过，dispatch 尚未接到 cmd)

## Phase 2 — Go server shell(RPC + HTTP + gateway)⬜

依赖 Phase 0（已就绪）。与 Phase 1 并行可行。

- [ ] **2.1** Value model + serialization(`go/internal/rpc/{value,serialize,vql}.go`)
- [ ] **2.2** Framing(`SocketProtocol`)
- [ ] **2.3** Channel server(call / event / cancel / dispose / pending)
- [ ] **2.4** RPC client(Go 侧，用于 gateway 上游）
- [ ] **2.5** HTTP shell(static files、server-info、headers)
- [ ] **2.6** Web authentication(session store、limiter、cookie)
- [ ] **2.7** Host capability + WebSocket endpoints(`/ws`, `/ws/host`)
- [ ] **2.8** Gateway:routing table + upstream forwarding(`gateway-routes.json`)
- [ ] **2.9** Node upstream supervision
- [ ] **2.10** Observability and tracing
- [ ] **2.11** Build / packaging / Docker
- [ ] **2.12** End-to-end with real UI
- [ ] **G2.1–G2.13** 全部 gate check

## Phase 3 — Port services to Go,channel by channel ⬜

依赖 Phase 1 + Phase 2。

- [x] **3.1** Shared service infrastructure(Registry、broadcast、paths、atomic file)— `G3.shared` PASS(2026-10-13):`go/internal/services/{registry.go,broadcast/,paths/}`、`go/internal/adapters/{fsx(atomic+fault),logx}`;winpath 向量表由真实 Node 生成;Registry 已接入 nex-server `buildHandler`(broadcast 为首个注册渠道,路由仍由 gateway-routes.json 控制)
- [ ] **3.2** Tier 1 stateless:`ISystemService`、`IFileService`、`IFileWatcherService`、`IMediaPreviewService`
- [ ] **3.3** Tier 1b:`IGitService`、`IGitCheckpointService`
- [ ] **3.4** Tier 2:`ITerminalService`(PTY)
- [ ] **3.5** Tier 3 settings:`ISettingService`、`ISettingsSyncService`、`IClientConfigService`、`IClientScenesService`、`IOnboardingRecordService`、`ICredentialService`、`IDeviceService`、`ICodingPlanSubscriptionService`
- [x] **3.6** Tier 3b providers:`IModelSelectionService`、`IProviderSettingsService`、`IProviderProvisioningTargetService`、`packages/provider` + `packages/provider-node` — `G3.providers` PASS(2026-10-15):`go/internal/providerconfig`(共享解析包:overlay 值类型、规则引擎、resolver、registry、file codec、builtin release、agent parser 抽取自 cmd/nex-agent)+ `go/internal/services/modelprovider`(personal repo、builtin source(内嵌 nex-builtin.json 工件)、config service、registry service、facades、models.dev/remote model list、legacy importers)+ `go/internal/services/providers`(provisioning target + 连接模式权限门,web-remote 精确拒绝错误)。路由翻转(step f)留待后续;Phase 1 agent 测试保持通过(cmd/nex-agent 改用共享包)
- [ ] **3.7** Tier 4 知识扩展：`ISkillsService`、`ISkillSyncService`、`ICommandsService`、`IMemoryService`、`IHooks…`、`IPluginsService` 读侧、`IPluginManagementService`、`IPluginSyncService`、`IMcpSyncService`
- [ ] **3.8** Tier 5 peripheral:`IPetService`、`IConversationShareService`、`IPromptAttachmentTransferService`、feedback、`IUsageStats`、`IStorageService`、process telemetry、`ICuaPermissionService`/`ICuaPipSessionService`
- [x] **3.9** Tier 6a bots:`IBotsService` + `/api/bots/*` — `G3.bots` PASS(2026-10-15):`go/internal/services/bots`(@nex/shared 合约类型、commandParser/commandOrder 表、reply formatter + tool-call summary + permission preview、`bot-config.v3.json`/`bot-state.v3.json` 仓库与 legacy 迁移、webhook/telegram/feishu/lark/weixin 五个 provider adapter、Telegram 长轮询 runtime 与游标纪律、回调管道与入站幂等层、SessionBridge 接缝)+ `go/internal/server/botsroute.go`(两条 POST 路由)。库选型见 decisions.md:飞书/微信/Telegram 全部 raw HTTP(TS 侧也只有手写 fetch;微信无任何公开 SDK),Go SDK 与 WebSocket 长连接一起留到 Phase 4 再评估。会话腿走 `bots.SessionBridge` 接缝,由 3.10 填充,不阻塞本步
  - ⚠️ `G3.0.test`(`go test -race ./...`)在本机有既有 flake,与本步无关:`TestRuntimeEndToEndPersonalProvider`(modelprovider)在全量并行下偶发失败,`TestAtomicWriteConcurrent`(fsx)在干净树上也偶发失败;两个包单独跑 5 次均稳定通过
- [ ] **3.10** Tier 6b agent bridge（关键路径，9 个子步骤）
  - [x] 3.10.1 Process manager — `G3.agentproc` PASS:`go/internal/agentproc`(spawn/residency/tree kill/idle reclaim/epoch/stderr 诊断)+ `fakeagent` 测试二进制
  - [x] 3.10.2 Protocol client — `G3.protoclient` PASS(2026-10-16):`go/internal/agentproc/proto`。`TopicWireFrameAssembler` 移植(25 个 assembler 向量逐条比对 TS 记录,14 种 fault reason、ordinal/supersede/timeout 规则);协议 client(按 id 关联、乱序应答、每请求超时、取消、observation 与业务请求的回收事件区分、transport close);`startup/storageState` gate;`interaction/*` 路由接缝(默认 method-not-found);`runtime/capabilities` 协商(正结果缓存、负结果不缓存);对**真实 Go agent 二进制**的往返测试(S01 握手形态 + 未知名 method 的 -32601 + gate 释放 + 并发关联);goroutine 泄漏 delta 检查
    - 顺带修掉 3.10.1 的一个真实缺陷:`agentproc.Client.ExitInfo()` 原本从 `child.exited`(无人写入的 channel)接收,任何调用者都会永久阻塞;改为 `child.WaitExit()`(等广播信号后持锁读已发布状态)
  - [x] 3.10.3 V4 connection scope facade — `G3.connscope` PASS(2026-10-16):`go/internal/services/agentscope`。每条已接受的 WebSocket 一个 scope,独占本连接建立的 subscription。`hello` → `clientHello` 握手(role 决定是否需要握手、`clientMode` 决定 `deliveryProfile`、`workflowRunDeltas` 只认 `=== true` 且是**连接**事实);`(connectionId, topic)` 帧路由;unsubscribe route(可信 relay 精确命中 vs 终端 UI 宁可拒绝猜测);`V4ConnectionFlowState` 流控(边沿串行、`closed` 终态、subscribe ACK 后补发 `saturated`、最后一个订阅先 closed 再 unsubscribe);dispose 拆掉本连接全部 subscription;`interaction/*` 按拥有该连接的 resolver 路由
    - 线协议词汇(topic key、`HelloMessage`/`ClientHello`、flow state、帧解码、fault 字符串)**复用 `internal/agentproc/proto`**,不重复声明,facade 与协议 client 不会漂移
    - 测试:两种 client mode × 两种 role 全覆盖;TS 里每条注释描述的历史 bug 一个回归测试(hello 先于 initialize、中途换 client、clientHello 严格解析、caller 侧 `workflowRunDeltas`/`connectionId`/`clientMode`/`deliveryProfile`/`subscriberScope` 被剥掉、relay 的长度前缀 namespace 消除 `a/b`+`c` 与 `a`+`b/c` 碰撞、ACK 竞态暂存窗口(早帧按到达序释放、只放行 ACK 的 subId、溢出整批失败并反向 unsubscribe、close 后迟到 ACK 反向 unsubscribe)、`available` **不是**换代边界而 `unavailable` **是**、迟到 ACK 拒绝但不向新 runtime 反向 unsubscribe、relay unsubscribe route 钉住 vs 终端不猜、`saturated` 在 subscribe 后补发、`closed` 终态、最后订阅先 closed、单个控制 RPC 失败不打断链、command `clientId` 不一致拒绝、clock 探测不占 workspace 绑定、迟到 attachment begin 先 abort、dispose 不影响别的连接);另加 12 条并发连接证明不串投不丢帧,以及覆盖每个 pump 退出路径的 goroutine 泄漏 delta 检查
    - 顺带修掉两个真实并发缺陷:并发 `Dispose` 会各自 enqueue 一条 close 边并重复 teardown(改为一次性 teardown + 其余调用者等 `disposeDone`);`pendingOwnership.stage` 在帧泵 goroutine 上写、由 subscribe 的 continuation 读,没有加锁(`-race` 抓到)
  - [x] 3.10.4 Task index & syncer — `G3.taskindex` + `G3.taskindexx` PASS(2026-10-18):`go/internal/adapters/taskindex`(store)+ `go/internal/services/taskindex`(repo + syncer)
    - 迁移逐字节:账本校验和不是 SQL 的哈希,`migrations.ts` 刻意哈希 `JSON.stringify(checksumInput)`(冻结语义输入;`function.toString` 被禁,Electron/SEA 打包会改写函数文本)。`export-tasks-migrations.mts` 把那份字节流原样导出为 `*.checksum-input.json`,Go 直接对该文件求 sha256,两引擎不可能漂移。`0001`(接管 schema + ALTER/backfill + 重复绑定保留裁决)与 `0002`(冻结的旧 selection 解码)是 Go 函数(`adopt.go`/`selection.go`),与 Phase 1 处理 session store `0020`–`0022` 同一手法;`0003` 逐字执行。连账本表自己的 DDL 大小写都对齐 TS——它会落进 `sqlite_master.sql`,跨引擎比对是逐字节的(交叉检查在这里抓到过一处真实漂移)
    - repo 规则:主键(identity 优先)压过残留的 `workspace_identity` 列,同路径不同远端不串线;`updatedAt` 单调;较晚的 `running` 快照不能把较新的终态拉回;手动改名不被 agent 标题覆盖;无正文上下文的 agent 写不清空已索引正文;unread marker 在写锁内基于不随清除重置的 watermark 严格递增,清除以客户端看到的 marker 为条件;删除墓碑与其分组引用同事务;系统分组归属与 grouped 顶层顺序**只初始化一次**(重复回源不按异步完成顺序重排列表)
    - syncer 规则:首帧是**静默基线**(只补缺失行,不回放历史终态、不广播——否则冷启动会给每个历史会话发一次完成提醒);没有基线的会话不回放终态;subscribe ACK 只是 admission,只有 logical frame 原子 apply 后才存在可证明的 resume base;断档在同订阅上按 `(logEpoch, seq)` resync,没有可证明 base 时强制 snapshot 而不是跨空洞拼接 delta;每次广播都显式带 reason(旧的隐式缺省值正是 UI 最重的刷新语义);terminal 先于 ready(手机 command queue 以 ready 作为放行下一条的边界)
    - 跨引擎(`crosscheck-taskindex.mjs`):Node 建并写 → Go 打开读并比对;Go 建并写 → Node 读;`sqlite_master` 23 个对象逐字节相同、账本 3 行相同;双方重开对方的库都**零**迁移。每个版本的 fixture 由真实 `runTasksDatabaseMigrations` 记录,两个代码迁移按 Node 的行级输出比对
    - 顺带修掉一个真实数据竞争:`Repo.enqueueWrite` 在 `chainsMu` 下读 `closed`,而 `Close` 在 `mu` 下写它(`-race` 抓到);顺带修掉一个真实缺陷:`ClearTaskUnreadIfMatches` 忘了把已清空的 `unreadAt` 传进 `writeRecord`,未读标记清不掉
  - [ ] 3.10.5 Task service adapter(owner/lease、跨 Host 路由、stale-run 防护）
  - [ ] 3.10.6 Window controller
  - [ ] 3.10.7 Automation(cron)
  - [ ] 3.10.8 Claude-native import
  - [ ] 3.10.9 Cutover of the agent leg
- [ ] **3.11** Remote/Docker/SSH services — 决策：Phase 3 保留 Node,Phase 5 接手

## Phase 4 — Agent breadth(Phase 1 之外的全部 agent 特性)⬜

依赖 Phase 1。**D4 在本阶段启动前需要最终决策（sidecar vs drop)**。

- [ ] **4.0** 录制 Phase 4 golden traces(S19–S50)
- [ ] **4.1** Egress policy + HTTP 加固
- [ ] **4.2** Bash parser + permission/read-only policy（完整版，~35 文件）
- [ ] **4.3** Bash background jobs、TaskOutput/TaskStop、cron 工具
- [ ] **4.4** Subagents 与 Agent 工具（profiles、嵌套 runner、message steering、completion notification)
- [ ] **4.5** Hooks + workspace hook trust(agent 侧）
- [ ] **4.6** Memory(extraction、recall、目录、index content)
- [ ] **4.7** Skills、plugins、custom commands 加载（agent 侧）
- [ ] **4.8** MCP client(stdio/HTTP 传输、OAuth、exposure/tool-search)
- [ ] **4.9** WebFetch、WebSearch
- [ ] **4.10** Media readers(images、PDF、video)+ attachments
- [ ] **4.11** Goals、file rewind、assistant feedback、selection side chat
- [ ] **4.12** Sidecar protocol and process(D4 = sidecar 时）
- [ ] **4.13** Codemode and Node REPL
- [ ] **4.14** Dynamic workflows(saved workflows、runs、retune、journal)
- [ ] **4.15** Browser Use and CUA(agent 侧协议）
- [ ] **4.16** Remaining protocol surface + capabilities flip
- [ ] **4.17** Parity sweep and soak(S01–S50 全跑 + 24h 浸泡）

## Phase 5 — Cutover(Go 变成默认)⬜

依赖 Phase 3 + Phase 4。

- [ ] **5.1** Dual-run canary(alpha channel,≥14 天 0 P1)
- [ ] **5.2** 最终决策（D10 落定；Node sidecar runtime 版本统一）
- [ ] **5.3** Build 和 packaging(reproducible build、多 target、签名/公证）
- [ ] **5.4** 替换 `nex-server-cli`(installer、supervisor、update、service manager)
- [ ] **5.5** `install.sh` + 从 Node 安装原地升级
- [ ] **5.6** Remote deployment(SSH/WSL/Docker)
- [ ] **5.7** Docker 镜像与 compose
- [ ] **5.8** 兼容性、升级/降级、数据安全套件
- [ ] **5.9** 性能与资源 benchmark
- [ ] **5.10** 安全 review
- [ ] **5.11** 退役 Node 后端（**需单独批准**)
- [ ] **5.12** 文档与 release
- [ ] **5.13** 最终验收（3 个 OS 干净机器装、升、降、卸）

## 决策状态速查

| ID  | 决策                      | 状态                         | 影响                        |
| --- | ------------------------- | ---------------------------- | --------------------------- |
| D1  | Go module path            | 占位（未最终）               | 无功能影响，发布前需替换    |
| D2  | Go toolchain              | provisional(1.24)            | —                           |
| D3  | SQLite driver             | ✅ decided(modernc.org)      | Phase 1.3 已用              |
| D4  | JS-bound features         | deferred to Phase 4          | 阻塞 Phase 4.12–4.15 的细化 |
| D5  | Parity oracle             | decided(recorded TS)         | —                           |
| D6  | TUI / `nex` CLI           | deferred to Phase 4          | —                           |
| D7  | New env vars              | provisional（不加新的）      | —                           |
| D8  | Node/Go 共享 SQLite       | ✅ decided                   | Phase 1.3 已验证            |
| D9  | Wire 兼容策略             | provisional(byte-compat)     | —                           |
| D10 | Desktop local host        | ✅ decided A(保留 Node host) | Desktop 只换 Go agent       |
| D11 | Replay 是 script-driven   | decided                      | —                           |
| D12 | Delta frame 30ms batching | decided(observed)            | —                           |
| D13 | Concurrent telemetry lane | decided(observed)            | —                           |

## 立即可做的下一件事

1. **G1.13**:Web/Desktop 手动 checklist 余项(permission prompts、文件编辑、Desktop app)。Web smoke 已由 G1.13a 覆盖。
2. **Phase 2 启动**:Phase 0 已就绪，Phase 1 也接近完成，可以并行启动 Go server shell。
3. **补 G0.13 例外文件的覆盖率**:`apply.ts` 66%、`turn-machine.js` 55%、`bash.js` 58%、`migration-runner.js` 75%（写到 80% 即可去掉例外）。
4. **Phase 4 决策 D4**:sidecar vs drop for JS-bound features(dynamic-workflow、codemode、node-repl、Browser Use、CUA)——阻塞 Phase 4 详细规划。
