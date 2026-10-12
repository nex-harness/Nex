# Go 重写整体进度

> 单一来源:`docs/specs/go-rewrite/phase-*.md` 是各 phase 的规约，本文档只是它们的勾选视图。
> 最后更新：2026-10-12(Phase 0 全绿，Phase 1 接近完成，Phase 2 全绿)。

## 总览

| Phase   | 状态        | 通过 / 总数         | 备注                                                                                     |
| ------- | ----------- | ------------------- | ---------------------------------------------------------------------------------------- |
| Phase 0 | ✅ 全绿     | 21/21               | `gate.sh 0` exit 0(2026-10-10)                                                           |
| Phase 1 | 🟢 接近完成 | 45/45 自动 + 1 人工 | 自动 gate 全过；剩 G1.13 手动 checklist(permission prompts / 文件编辑 / Desktop)需人工跑 |
| Phase 2 | ✅ 全绿     | 21/21               | `gate.sh 2` exit 0(2026-10-12);12 步全过                                                 |
| Phase 3 | ⬜ 未开始   | 0/15                | 依赖 Phase 1 + Phase 2                                                                   |
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

- [ ] **3.1** Shared service infrastructure(Registry、broadcast、paths、atomic file)
- [ ] **3.2** Tier 1 stateless:`ISystemService`、`IFileService`、`IFileWatcherService`、`IMediaPreviewService`
- [ ] **3.3** Tier 1b:`IGitService`、`IGitCheckpointService`
- [ ] **3.4** Tier 2:`ITerminalService`(PTY)
- [ ] **3.5** Tier 3 settings:`ISettingService`、`ISettingsSyncService`、`IClientConfigService`、`IClientScenesService`、`IOnboardingRecordService`、`ICredentialService`、`IDeviceService`、`ICodingPlanSubscriptionService`
- [ ] **3.6** Tier 3b providers:`IModelSelectionService`、`IProviderSettingsService`、`IProviderProvisioningTargetService`、`packages/provider` + `packages/provider-node`
- [ ] **3.7** Tier 4 知识扩展：`ISkillsService`、`ISkillSyncService`、`ICommandsService`、`IMemoryService`、`IHooks…`、`IPluginsService` 读侧、`IPluginManagementService`、`IPluginSyncService`、`IMcpSyncService`
- [ ] **3.8** Tier 5 peripheral:`IPetService`、`IConversationShareService`、`IPromptAttachmentTransferService`、feedback、`IUsageStats`、`IStorageService`、process telemetry、`ICuaPermissionService`/`ICuaPipSessionService`
- [ ] **3.9** Tier 6a bots:`IBotsService` + `/api/bots/*`
- [ ] **3.10** Tier 6b agent bridge（关键路径，9 个子步骤）
  - [ ] 3.10.1 Process manager
  - [ ] 3.10.2 Protocol client（含 `TopicWireFrameAssembler` 移植，25 个 assembler 向量已生成）
  - [ ] 3.10.3 V4 connection scope facade
  - [ ] 3.10.4 Task index & syncer
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
