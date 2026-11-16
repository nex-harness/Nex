# 桌面端自动更新：GitHub Releases

> 本文件原名「桌面端服务端 Manifest 自动更新」。2.0 起更新只走 GitHub Releases，
> 服务端 manifest 接口 `/api/v1/releases/electron/manifest` 与强制升级 gate 已删除。
> 产品规则、状态所有者与验收见 `docs/specs/desktop-updates-github.md`。

## 发布来源

- 发布仓库：`github.com/nex-harness/Nex`（Public）。`packages/desktop/electron-builder.config.js` 的
  `publish` 块与客户端 `autoUpdater.setFeedURL({ provider: "github", owner: "nex-harness", repo: "Nex" })`
  使用同一坐标。
- 客户端读取 `https://github.com/nex-harness/Nex/releases/download/<tag>/latest-mac.yml`（Windows 为
  `latest.yml`，Linux 为 `latest-linux.yml`）及同目录 `.blockmap`。GitHub provider 固定
  `isUseMultipleRangeRequest = false`（GitHub 资产由 S3 提供），与 electron-builder 的 publish 配置一致。
- 发布流水线 `.github/workflows/release.yaml` 打 `v*` tag 时由 electron-builder `--publish always`
  上传安装包、blockmap 与 channel yml。

## 更新网络出口

CA PEM 文件可以包含完整服务链或“根 CA + 服务端叶证书”。常规建链仅把具备 CA 签发资格的配置证书作为锚，附带的非 CA 服务证书不能抢先成为锚而破坏合法链；完整配置集仍用于显式自签名服务证书的逐字匹配。非 CA、非自签名叶证书不能单独作为 pin 绕过链校验。

用户显式配置的自签名服务证书可直接作为信任锚，不要求其具备 CA 签发用途。仅当收到的是单张证书、与配置的证书逐字匹配且自签名有效时采用此路径；仍须通过有效期、域名/IP、服务端用途、关键扩展和密钥/签名强度检查。不能用 PKIjs 的某个失败码作为此兼容路径的唯一条件，也不能据此接受未配置的证书或放宽多级 CA 链。

自定义 CA 的验证路径上，每张证书的 keyUsage 都必须是结构合法、仅使用已定义位的 BIT STRING；叶证书如声明用途须允许签名，CA 如声明用途须允许证书签发。不能只检查叶证书，也不能将解析成功视为用途合法。

名称约束按每一级 CA 分别施加：同一级 permittedSubtrees 内是候选集合，不同 CA 之间必须全部满足；任一级 excludedSubtrees 命中即拒绝。检查每张后代证书的所有 DNS/IP SAN，不能只检查用于本次连接的名字。自颁发中间证书沿用路径验证的例外，但最终叶证书不能跳过。此补充验证支持 ASCII DNS 名和 IPv4/IPv6 地址及掩码；带约束的通配符 SAN、其他名称类型的约束、非零 minimum 或显式 maximum 等未可靠支持的形式拒绝放行，不静默忽略。普通无名称约束的链保持原行为。

回归入口 `node packages/desktop/scripts/auto-update-ca-constraints-smoke.mjs` 同时检查策略回调与实际 updater HTTPS。`--app <完整包可执行文件>` 在隔离数据目录中运行真实完整包，使用同组证书验证，不替换包内策略实现。
该入口同时接入 `test:auto-update-source-security` 的 MR 自动 job，网络策略或回归脚本改动会触发；完整签名包验收仍单独记录，源码夹具不冒充完整包。

2026-11-02 验证：旧完整包 3.17.112 可实际放行畸形 CA keyUsage、扩大后的名称约束及越界的第二个 SAN。修复后启用自动更新的 production 完整签名包 3.17.113，通过新增 18 个约束场景与原 34 个 TLS 场景；真实下载完整 ZIP 时，原生关闭和“后台下载”均不取消任务，重开读到当前进度，完成后可再次打开，实际只下载一次且不执行安装。源码窗口生命周期 E2E 另行通过。此为隔离合成证书和本地更新源，不代表生产网络、Apple 公证或其他操作系统验收。

更新检查、安装包和 blockmap 请求使用 `electron-updater` 自己的 Chromium Session。
启动时，该 Session 必须与主界面一样读取设置页的 `httpProxy`、`httpProxyNoProxy`
和 `httpProxyCaCertPath`；代理留空时直连，不继承 shell 或系统代理。
内置浏览器仍保留代理留空时跟随系统的既有规则，忽略证书校验开关只作用于内置浏览器。
网络设置按既有启动配置生效，不另建更新器网络设置或自动重试状态。
应用策略的启动日志分别标明 `default-session`、`auto-updater`、`embedded-browser`，
用于区分出口；只记录模式和策略开关，不记录代理地址、账号或密码。

回归：通过隔离 HTTP 代理，用真实 `electron-updater` 请求无法直连的测试域名并下载文件，
确认代理收到请求、字节数与校验一致；检查 no-proxy 可以直连本地服务器，
断流后由用户重试可下载完成。该场景不承诺断点续传，也不代表原反馈已确认同一根因。
在仓库根目录执行 `pnpm install --frozen-lockfile` 后，运行
`node packages/desktop/scripts/auto-update-network-smoke.mjs`，证据保存在 `.e2e-artifacts`。
该入口直接使用的 `esbuild` 由 desktop 包声明为开发依赖，不能依赖其他包提升到根目录的副本。
该回归入口按正式主进程的 ESM 格式构建，并保持 `electron-updater` 为外部依赖，
同时覆盖 CommonJS 依赖的加载边界；不得用 CJS 打包将不兼容的命名导入转换掉。

## 通道

| 设置 `receivePreviewUpdates` | `autoUpdater.allowPrerelease` | 行为                                                                 |
| ---------------------------- | ----------------------------- | -------------------------------------------------------------------- |
| false                        | false                         | 只匹配非 prerelease Release；读取 `latest*.yml`                      |
| true                         | true                          | 匹配 prerelease tag（如 `v3.18.0-preview.1` → `preview-mac.yml`），缺失时回退 `latest*.yml` |

通道值仍由 main 推导为 `stable | preview` 并进入 `UpdateStatePayload.channel`，用于按通道记录跳过版本。

## Windows 更新信任边界

更新授权依赖 GitHub Release 上的 channel yml 哈希与安装包的 Authenticode 签名。
`packages/desktop/electron-builder.config.js` 保留包内 `publisherName`，只用于激活
`autoUpdater.verifyUpdateCodeSignature` 自定义校验，并通过 `verifyWindowsAuthenticodeSignature`
检查实际待安装文件；不按历史发布者 DN 限制新签名者，也不需要用户预装换证过渡版。
