# 远程 Server 发布资产：GitHub Release 与本地产物部署

Status: accepted
Owner: Desktop Host（`packages/desktop/src/host/sourceServerReleaseResolver.ts`）、Server CLI 打包（`packages/nex-server-cli/src/packaging/`）、CI（`.github/workflows/release-server.yaml`）
Related: `docs/specs/desktop-updates-github.md`、`docs/specs/zcode-endpoints-inventory.md`（CDN 一行）、`docs/desktop-bundle-size-architecture.md`

## 背景

桌面「添加远程 Server」会把 nex-server 的运行时压缩包装到远端机器。客户端的下载来源写死为 ZCode 的 CDN：
`https://cdn-zcode.z.ai/nex/electron/releases/<版本>`（海外）与 `https://cdn.codegeex.cn/...`（国内）。
2.0 的构建不发布到那里（该路径返回 404），所以这条链路在发布版里不可用。

CI 现状与客户端的期望也对不上：

| | 客户端要的 | CI 发布的 |
| --- | --- | --- |
| 位置 | CDN `/<版本>/` | GitHub Release 资产 |
| 归档名 | `nex-server-<平台>.tar.gz` | `nex-server-<平台>-<标签>.tar.gz` |
| 索引 | `source-server-release.json`（含 sha256、大小） | 没有 |
| 平台 | `linux-x64`、`linux-arm64`、`darwin-x64`、`darwin-arm64` | `darwin-arm64`、`linux-x64`、`linux-arm64` |

## 产品规则

1. **发布来源只有 GitHub Release，与 App 同一个 tag。** 版本 `X.Y.Z` 的远程 Server 资产在
   `https://github.com/nex-harness/Nex/releases/download/vX.Y.Z/` 下，桌面端不再默认访问 ZCode CDN。
   CDN 构建期覆盖（`CDN_DOMAIN`、`OSS_PATH_PREFIX`）和 `NEX_REMOTE_ASSET_CDN_BASE_URL` 仍然可以覆盖这个默认值。
2. **支持的远端平台：`linux-x64`、`linux-arm64`、`darwin-arm64`。** 不再提供 `darwin-x64`；探测到
   `darwin-x64` 的远端按「不支持的平台」处理，沿用既有的专用错误码和说明。
3. **每个 Release 资产里有一份 `source-server-release.json` 和每个平台一个归档，名字不带标签后缀：**
   `nex-server-<平台>.tar.gz`。索引文件的 `archives[].urls` 写相对文件名，客户端用发布根拼成完整地址。
   `release-server.yaml` 产出的带标签归档（`nex-server-<平台>-<标签>.tar.gz`，给手动安装的用户）保持不变，两套文件并存。
   **两套归档的目录布局不同，不能互相改名复用：** 远端用 `tar -xzf … --strip-components=1` 解包，所以远程归档必须有**一层顶级目录**
   （`nex-server-<平台>/bin|runtime|manifest.json`，即 `stage` 的 `dist-release/nex-server-<平台>.tar.gz`）；
   手动安装包是 `tar -C bundle .` 的无顶级目录布局，还带 `web/`。
4. **客户端校验：** 下载后用索引里的 `sha256` 与 `sizeBytes` 校验，不一致就失败，不使用该文件（既有行为，不改）。
   索引里的 `version` 必须等于桌面 App 的版本（既有行为）。
5. **本地构建产物可以部署到远程（测试用途）。** 打包后的 App 与开发态一样，可以通过环境变量
   `NEX_REMOTE_ASSET_LOCAL_RELEASE_DIR=<根目录>` 指向一个本地发布根。根目录与开发态的 `mock-cdn` 同构：
   `<根目录>/releases/<版本>/` 下有 `source-server-release.json` 和 `nex-server-<平台>.tar.gz`。
   - 命中时只消费这个目录，并沿用开发态的 `development-content-addressed` 策略：descriptor 与归档必须通过
     sha256/大小校验；目录不存在、缺索引、缺目标平台、校验失败都**明确失败**，不回退到 GitHub 或缓存，
     避免把「本地产物没生效」伪装成连接成功。
   - 未设置时行为不变（开发态读 `mock-cdn`，打包后读发布根）。
   - 本地目录由 `pnpm prepare:remote-assets` 生成，默认输出到 `packages/desktop/mock-cdn/releases/<版本>/`；
     `pnpm prepare:remote-assets -- --out <根目录>` 改为输出到 `<根目录>/releases/<版本>/`。

## 状态所有者

| 状态 | 所有者 | 读取方 |
| --- | --- | --- |
| 发布根默认值 | `packages/desktop/src/main/remoteCdn.ts` | `desktopRuntimeEnv.ts` → Host 的 `sourceServerReleaseResolver.ts` |
| 目标平台清单 | `packages/shared/src/source-server/release.ts`（`SOURCE_SERVER_RELEASE_TARGETS`） | 客户端平台探测、descriptor schema、打包脚本 |
| 索引与归档的生成 | `packages/nex-server-cli/src/packaging/remoteAssetRelease.ts` | 本地 `prepare:remote-assets`、CI |
| 本地目录覆盖 | `desktopRuntimeEnv.ts` 读取环境变量，Host 解析器消费 | `sourceServerReleaseResolver.ts` |

没有第二份状态：索引文件是归档内容身份的唯一凭据，客户端与 CI 共用 `sourceServerReleaseDescriptorSchema`。

## 事件顺序

```text
CI（tag vX.Y.Z）：3 个平台各自 stage -> 收集 nex-server-<平台>.tar.gz
                  -> collectSourceServerRemoteReleaseArchives 生成 source-server-release.json
                  -> 与 App 的 dmg/zip 等一起发布到同一个 Release

桌面添加远程：探测远端平台 -> 解析发布根（默认 GitHub Release；设置了本地目录则只用本地目录）
              -> 取 source-server-release.json -> 校验 version -> 取对应平台归档 -> 校验 sha256/大小
              -> 上传到远端（local-download-upload）或让远端自己下载（remote-download）-> 解包并启动
```

## 失败语义

- 发布根不可达或没有索引：报「无法解析 Source Server 发布索引」，沿用 `remote-download-failed` 的公开码，不静默回退到 ZCode CDN。
- 索引缺目标平台：报缺少该平台（包括 `darwin-x64`）。
- 归档 sha256 或大小不符：丢弃临时文件并报校验失败。
- 设置了本地目录但不可用：明确失败并指出目录，不回退。

## 验收

1. 单元之外的真实验证：`pnpm prepare:remote-assets` 生成本地发布目录（`darwin-arm64`、`linux-x64`、`linux-arm64` 三个归档与索引），
   索引里没有 `darwin-x64`；用 `NEX_REMOTE_ASSET_LOCAL_RELEASE_DIR` 指向它的根目录，桌面连接一个远端（本机 SSH 或 Linux 机器），
   部署成功并能连接。
2. 把目录里的归档改一个字节再连：必须报校验失败，不得连接成功。
3. 未设置本地目录的打包 App：默认请求地址是 `https://github.com/nex-harness/Nex/releases/download/v<版本>/source-server-release.json`，
   而不是 `cdn-zcode.z.ai`。
4. CI（`release-server.yaml`）在 `v*` tag 上产出 `source-server-release.json` 和 3 个不带标签的归档，并随同一个 Release 发布；
   `darwin-x64` 不出现。
5. `pnpm typecheck`、`pnpm lint`、`pnpm architecture:check --changed` 通过。

## 不在范围

- 官方插件市场的 CDN（`cdn-zcode.z.ai/nex/official-plugin/...`）：保持不变。
- Windows 远端：不提供。
- 签名与公证：这里只校验 sha256，不引入新的签名链路。

## 验证记录

### 2026-11-16：本地构建产物部署到真实 Linux x86 远端（`remotebuilder`，Debian 13，glibc 2.41）

用 `stage:remote-assets --out /tmp/nex-remote-release` 生成的 `linux-x64` 归档（90 MB），按桌面安装器的同一条命令序列，
在**隔离的 HOME**（`/tmp/nex-rt/home`，不碰该机器已有的 `~/.nex` 与正在运行的 nex-next）下手动走完：

| 步骤 | 结果 |
| --- | --- |
| 上传 | 本机、远端、索引三处 sha256 完全一致（`5e5027a3…`） |
| 解包 | `tar -xzf … --strip-components=1`：得到 `bin/`、`runtime/`、`manifest.json`；`manifest` 为 `nex-server` / `linux-x64` / `2.0.0` |
| 运行时 | 随包 Node `v22.16.0` 在该机器 glibc 上直接启动 |
| 安装 | `bin/nex update --from-archive … --json --progress` 依次输出 `verify-archive`、`extract`、`verify`，结果 `{"applied":true,"installed":true,"version":"2.0.0"}` |
| 启动 | `nex serve --daemon --port 3099` 就绪；`status` 为 `ready`，版本 2.0.0 |
| 经 SSH 隧道 | 未认证：`/api/server-info` 只返回受限信息，`/ws` 返回 401；带 ingress token：返回完整 server-info，`/ws` 升级返回 101 |

验证后已停止该 server 并删除临时目录；该机器原有的 `~/.nex`、nex-next 进程未被改动。

**没有覆盖：** 没有通过桌面 App 的界面走「添加远程 Server」这一整条流程（本机没有图形化驱动该窗口），上表是用与之相同的命令序列手动执行，
差别只在上传由 `scp` 代替 SFTP、`HOME` 被隔离。`linux-arm64` 与 `darwin-arm64` 的归档只做了索引与校验，没有部署到真实机器。
GitHub Release 上的下载路径（`remote-download` 模式）和 CI 新增步骤需要首次打 tag 才能验证。
