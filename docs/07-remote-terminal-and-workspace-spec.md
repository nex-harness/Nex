# Nex-v3 远程终端 (`libghostty`) 与多标签工作台规范 (Remote Terminal & Multi-Tab Workbench Spec)

- **状态**: Draft / Approved Core Direction
- **更新日期**: 2026-11-18
- **设计目标**: 在不破坏纯 Go 零 CGO 静态编译的前提下，基于 **`libghostty-vt` WebAssembly (`coder/ghostty-web`)** 与 Runner 原生跨平台 PTY 实现低延迟远程终端；并在 GUI 右侧边栏与底部停靠区构建统一的**多标签工作台容器（支持 `Files` 树状浏览、新 Tab 文件预览、`Review` Git Diff 审查、`Terminal` 与 `Side conversation`）**。

---

## 1. 工作台整体布局与多标签侧边栏架构 (Multi-Tab Workbench)

桌面端与 Web 端采用经典的 Coding Agent 分栏工作台骨架（移动端响应式收敛为抽屉式全屏 Tab 切换）：

```text
┌───────────────────────────────────────────┬────────────────────────────────────────┐
│  📁 Session Title          [± 更改 +128 -9]│ [Files ×] [main.go ×] [Review ×] [+]   │
│                                           ├────────────────────────────────────────┤
│  (主对话流：消息、思考折叠块、工具卡片)     │ 🔍 搜索文件...                          │
│                                           │ ▾ pkg/                                 │
│  ┌─────────────────────────────────────┐  │   ▾ agent/                             │
│  │ 向 Nex 提问，使用 @ 引用，/ 选择能力 │  │       loop.go                          │
│  │ [+] [Full access v]   [Model] [↑]   │  │       queue.go                         │
│  └─────────────────────────────────────┘  │   ▾ harness/                           │
├───────────────────────────────────────────┤       pty_unix.go                      │
│ [终端] [Nex ×] [+]                    [×] │       pty_windows.go                   │
│ $ go test ./...                           │ ▸ cmd/                                 │
│ ok   nex/pkg/agent   0.142s               │ ▸ docs/                                │
└───────────────────────────────────────────┴────────────────────────────────────────┘
```

### 1.1 右侧边栏多态 Tab 系统 (`WorkbenchTab`)

右侧侧边栏是一个通用的**多标签容器（Multi-Tab Dock）**。点击顶栏 `+` 按钮可随时新建并自由混排以下面板类型，点击文件树中的文件也会动态派生预览 Tab：

| Tab 类型 (`kind`) | 入口方式 | 核心职责 |
| :--- | :--- | :--- |
| **`files` (`Files`)** | `+` 菜单新建 / 默认常驻 | 以树状目录按层展示远端 Runner 当前工作区文件，支持模糊搜索文件名与 `.gitignore` 状态置灰。 |
| **`file_preview`** | 在 `Files` 树或聊天引用中点击文件 | **在独立新 Tab 中打开文件只读预览**（如 `[loop.go ×]`）：文本/源码支持语法高亮与行号点选引用、Markdown 支持渲染/源码切换、常见图片格式支持缩放预览。 |
| **`review` (`Review`)** | `+` 菜单新建 / 点击对话右上角 `[± 更改 +X -Y]` 胶囊 | 展示远端工作区实时 Git Diff 变更文件列表、增删行数统计，支持逐文件展开 Unified / Split 高亮对比。 |
| **`terminal` (`Terminal`)** | `+` 菜单新建（亦可停靠在左下角面板） | 基于 `libghostty-vt` WASM 的交互式远程伪终端实例，每个 Tab 对应远端 Runner 上的一个独立 PTY 会话。 |
| **`side_chat` (`Side conversation`)** | `+` 菜单新建 / 选中代码或文件发起旁路提问 | 共享当前工作区只读文件上下文的轻量支线问答标签页，避免临时查阅问题打断或污染左侧主任务上下文。 |

---

## 2. 基于 `libghostty` (WASM) 的远程终端架构

摒弃传统手写 JS 状态机的 `xterm.js`，采用 **Ghostty 官方 Zig 终端核心编译而成的 WebAssembly 模块**驱动前端渲染，后端配合纯 Go 零 CGO 跨平台伪终端。

### 2.1 前端渲染引擎：`libghostty-vt` WASM (`coder/ghostty-web`)

1. **为什么选 WASM 版 `libghostty-vt` 而非 CGO 原生库？**
   - **严守零 CGO 与三端大一统**：通过 `coder/ghostty-web` 加载 `~400KB` 零外部依赖的 `libghostty-vt.wasm`，无需链接 C/Zig 原生 GUI 库，同一套 React 终端 Tab 组件可同时在 **纯浏览器 Web、`mygo` Desktop (macOS/Windows/Linux)、`mygo` Mobile** 的任意分栏内流畅运行。
   - **真·原生级 VT 解析精度**：完整复用 Ghostty 的 VT100/xterm 解析器与字素簇（Grapheme Cluster）宽度算法，彻底根治中文双宽字符、Emoji、Nerd Fonts 图标错位问题，并支持 `XTPUSHSGR`/`XTPOPSGR` 等现代全屏 TUI 应用（`nvim`, `lazygit`, `htop`, `btop`）所需的控制序列。
2. **UI 配置联动**：终端 Tab 自动订阅 `user_preferences` 中的等宽字体族、字号与深浅色主题色板，拖拽右侧栏或底部面板边缘触发 `ResizeObserver` 时自动重新计算精确的 `(cols, rows)` 并同步至远端 PTY。

### 2.2 Runner 端零 CGO 跨平台伪终端 (`pkg/harness/pty`)

在 `nex-runner` 中通过 Go 编译标签（Build Tags）为三大桌面/服务器系统提供 100% 纯 Go 无 CGO 的真伪终端支持：

| Runner 操作系统 | 纯 Go 实现方案（零 CGO） | 默认 Shell 探测优先级 |
| :--- | :--- | :--- |
| **macOS / Linux** (`pty_unix.go`) | **`github.com/creack/pty`**（基于底层 `ioctl` 与 `/dev/ptmx`） | `$SHELL` -> `zsh` / `bash` -> `/bin/sh`（以 Login Shell `-l` 启动以加载用户 PATH 环境） |
| **Windows** (`pty_windows.go`) | **`golang.org/x/sys/windows` 原生 `ConPTY`**（调用 Win32 `CreatePseudoConsole` API） | 用户配置 Shell -> PowerShell 7 (`pwsh.exe`) -> Windows PowerShell -> `cmd.exe`（可选切换 Git Bash / WSL） |

### 2.3 WebSocket 二进制流快车道与断线画面回放

为了在远程网络下获得接近本地原生终端的敲击响应速度，终端控制流与字节数据流分离传输：

1. **控制信令（JSON Text Frame）**：
   - `pty.open`: `{ runner_id, workspace_cwd, cols, rows, shell? }` -> 返回 `{ pty_id }`
   - `pty.resize`: `{ pty_id, cols, rows }` -> 触发远端 `TIOCSWINSZ` / `ResizePseudoConsole`
   - `pty.close`: `{ pty_id }` -> 销毁对应 PTY 及其子进程树
   - `pty.list`: 查询当前工作区下仍在后台存活的活跃 PTY Tab 列表
2. **字节流快车道（WebSocket Binary Frame，零 Base64 膨胀）**：
   - 二进制帧格式：`[1 Byte OpCode (0x01=Stdin, 0x02=Stdout)][16 Bytes Raw UUID (pty_id)][Raw Terminal Bytes]`。
   - 前端收到 Binary Frame 后直接提取 `Uint8Array` 切片写入 `libghostty` WASM 线性内存，避免大日志刷屏时 JSON 序列化卡死主线程。
3. **内置类 `tmux` 断线重连画面恢复（Ring Buffer Replay）**：
   - PTY 会话生命周期归属于 `nex-runner`（而非绑定在前端页面窗口上；页面关闭不杀终端，除非用户显式点 Tab 上的 `×` 关闭或会话超时回收）。
   - Runner 为每个活跃 `pty_id` 在内存中维护一个 **`128KB` 环形字节缓冲区（Ring Buffer）**。当用户刷新浏览器或从另一台设备重新挂载该终端 Tab 时，Runner 先下发缓冲区快照还原当前终端屏幕内容与全屏 TUI 状态，随后无缝衔接到实时输出流。

---

## 3. 远程工作区文件树与新 Tab 预览规范 (`Files` & `File Preview`)

由于工作区物理文件位于远端 `nex-runner`（可能是内网 Linux 或远程 Windows），文件树与预览全部通过 `Client <-> Server <-> Runner` 的轻量 RPC 通道按需拉取。

### 3.1 树状目录按层懒加载与模糊检索

1. **按层懒加载 (`workspace.fs_list`)**：
   - 严禁一次性递归拉取整个工作区目录树（防止遇到大型 Monorepo 或构建目录瞬间阻塞）。
   - 初始仅加载工作区根目录第一层；用户点击目录前的 `▸` 展开时，按相对路径请求该层子项：
     ```json
     {
       "entries": [
         { "name": "pkg", "kind": "dir", "git_ignored": false, "git_status": "modified" },
         { "name": "node_modules", "kind": "dir", "git_ignored": true },
         { "name": "go.mod", "kind": "file", "size": 1420, "git_ignored": false, "git_status": "" }
       ],
       "truncated": false
     }
     ```
   - 排序规则：目录优先于文件，同级按字母自然序排列；命中 `.gitignore` 的目录（如 `node_modules`、`dist`、`.git`）默认以半透明置灰展示，仍允许手动点开排查。
2. **快速文件名检索 (`workspace.fs_search`)**：
   - `Files` 面板顶部的搜索框输入关键词时，调用 Runner 内置纯 Go 遍历器（自动跳过 `.git` 与 `.gitignore` 忽略项），毫秒级返回前 100 条模糊匹配相对路径供一键跳转打开。

### 3.2 点击文件在新 Tab 中打开预览 (`File Preview Tab`)

当用户在 `Files` 树中点击一个文件节点时：
- **Tab 复用与新建策略**：若该文件路径已存在打开的预览 Tab，则直接激活切换到该 Tab；否则在右侧边栏顶栏**新建一个以文件名命名的预览 Tab**（同名不同目录文件自动附加父目录前缀区分，如 `agent/loop.go` vs `harness/loop.go`）。
- **文件内容嗅探与安全分流 (`workspace.fs_read_preview`)**：
  Runner 读取文件前 512 字节魔数（Magic Bytes）与文件大小（单次默认预览上限 `1MB`），分三类返回给前端渲染：
  1. **文本与代码文件**（`.go`, `.ts`, `.tsx`, `.py`, `.rs`, `.json`, `.yaml`, `.toml`, `.sh`, `.sql`, `Dockerfile`, 无后缀纯文本等）：
     - 使用 **Shiki** 渲染带行号的只读语法高亮视图；
     - `.md` / `.mdx` 文件顶部提供 **「渲染视图 / 源码视图」** 一键切换开关；
     - **交互联动**：选中预览中的某几行代码，点击浮现的 `Add to chat` 按钮即可将 `@pkg/agent/loop.go:L42-L58` 插入左侧对话输入框。
  2. **支持的媒体/图像文件**（`.png`, `.jpg`, `.webp`, `.gif`, `.svg`）：
     - 在预览 Tab 内居中展示图片、分辨率与文件体积，支持滚轮缩放。
  3. **超大文本或不支持的二进制文件**（如 `.exe`, `.sqlite`, `.zip` 或 `> 1MB` 大日志）：
     - 对于超大纯文本，默认加载前 `256KB` 并在顶部提示 *“文件较大（18.4 MB），当前仅预览前 256 KB”*；
     - 对于二进制文件，展示文件元信息卡片（大小、SHA-256、修改时间）与 *“暂不支持预览二进制文件”* 提示，绝不将乱码字节塞入 DOM。

---

## 4. 远程修改审查规范 (`Review` Tab & Git Diff 胶囊)

将 Agent 自动改代码与用户人工 Code Review 紧密串联：

### 4.1 实时变更统计胶囊 (`[± 更改 +X -Y]`)
- `nex-runner` 对活跃会话工作区挂载轻量文件变动监听（防抖合并窗口 `500ms`），并在每次 `write` / `edit` / `bash` 工具执行完毕后自动刷新一次轻量 `git status --porcelain` + `git diff --numstat`。
- 若当前工作区为 Git 仓库且存在未提交变更，向前端推送 `workspace.git_summary` 事件（包含当前分支名、变更文件总数、`+additions`、`-deletions`），实时驱动对话区右上角的 **`[± 更改 +7641 -0]`** 胶囊。点击胶囊直接在右侧边栏打开或激活 **`Review` Tab**。

### 4.2 `Review` Tab 交互能力
1. **对比基准切换**：默认展示 **Working Tree vs HEAD**（全部未提交改动，含 Untracked 新增文件全文绿底预览）；支持下拉切换仅看 `Staged`、仅看 `Unstaged` 或查看最近一次 Commit。
2. **变更文件折叠树 + 高亮 Diff 视图**：
   - 顶部/左侧列出所有变更文件及其状态徽章（`M` 修改、`A` 新增、`D` 删除、`R` 重命名）与各自 `+X -Y`；
   - 支持 **Unified（单栏内联）** 与 **Split（左右双栏对比）** 视图切换；
   - **大锁文件防卡保护**：遇到 `package-lock.json`、`go.sum` 或单文件变更超过 2000 行的生成物时默认折叠 Diff 正文，点击「展开此文件差异」再按需向 Runner 请求 `workspace.git_file_diff`。
   - **行级评论转对话指令**：点击 Diff 某一行旁边的评论图标写下修改意见（如 *“这里并发没加锁”*），可直接携带文件名、行号与上下文片段发送给左侧主对话让 Agent 精准返工。

---

## 5. 工作区访问安全边界

所有 `workspace.fs_*`、`workspace.git_*` 与 `pty.*` 请求均在 `nex-runner` 本地经过路径安全校验：
- 对传入的 `rel_path` 强制执行 `filepath.Clean` 与符号链接解析，拦截 `../` 目录穿越攻击，确保 `Files` 树与 `Preview` 请求严格收敛在当前 Session 绑定的工作区根目录之内（除非用户在终端或 `Full access` 模式下显式切换工作区根目录）。
