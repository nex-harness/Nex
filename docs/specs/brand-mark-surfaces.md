# Nex N 图标在应用内的展示（2.0）

2.0 基线来自 ZCode 3.17.2，改名时只替换了部分素材：1024 桌面图标与 `.icns`/`.ico`、托盘模板图已是 N，
其余界面与安装素材仍是 ZCode 的 Z。本文规定统一替换为 1.2.x 已落地的 Nex N，不新增设计。

## 产品规则与所有者

- 全部应用与安装素材使用同一套 folded-ribbon N 三笔路径，黑白灰，不使用 Z、ZCODE 字样或绿色。
- 三笔路径沿用 1024 坐标系，字形裁剪框 `258 255 510 516`；路径与 1.2.3 的 `NexStartupLogo` 逐字符一致，
  各处复制的路径不得各自改形。
- `packages/ui` 负责共享 React 空态、启动徽标与 About；Web 与 Desktop 各自负责 JS 加载前的 HTML splash；
  Desktop main 负责原生 About HTML。静态素材由所属包打包，不为品牌资源引入运行时跨包加载。
- 素材的源头是 `public/logo/icon.svg`（1024×1024，黑色渐变圆角底板，白色 N，透明外边距不少于 100px）。
  Desktop build 的应用/Windows/安装 PNG、ICO、ICNS、Linux 多尺寸 PNG 与 `public/logo/icons` 副本从该源导出，
  保持文件名、尺寸与运行时路径不变。

## 展示接口与不变量

| 场景                                      | 规则                                                                                              |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `NexAboutLogo`（Web 登录页、About）       | 白色 currentColor N，`viewBox="258 255 510 516"`；调用方控制宽度，高度自适应。                    |
| Web / Desktop HTML splash                 | 同形白色 N；保留三笔 opacity 轮闪、96px 底板、56px 图标宽度，以及启动移除逻辑。                   |
| React `NexStartupLogo`                    | 同形白色 N；保留 busy/label 语义、可选呼吸动效与当前尺寸。                                        |
| Desktop 原生 About（`aboutWindow.ts`）    | 同形白色 N，宽 30px、高度自适应，放入现有 52px 深色容器。                                         |
| 云端内容演示页 `weekend-plan-hero.html`   | 品牌角标换成同形 N（只用于 mock/E2E 夹具，不影响生产界面）。                                      |
| Web `index.html` 内嵌 favicon             | 32px N PNG，与 `packages/web/public/favicon.ico` 为同一图形。                                     |
| DMG 背景（1×540×380/72dpi、2×1080×760/144dpi） | 保留布局与拖入区域；删除 "ZCODE" 字样，标题图标改为黑底白色 N；装饰为灰白。                  |
| `public/icon_512@2x.png`                  | 1024×1024 N，更新弹窗不再展示 Z。                                                                |

## 边界与失败语义

- 只改静态视觉，不改主题状态所有者、启动事件、窗口/托盘/更新流程的行为。
- 不删除功能：`HighspeedShareNoticeCard` 的 `zai-logo.png` 属于 Z.ai 高速分享推广内容，不是 Nex 标识，
  是否整体移除另行决定，本次不改。
- 缺失或损坏的素材由构建脚本与资源校验报错，不引入回退到 Z 的资源。

## 验收

1. 仓库源码与素材里不再出现 Z 三笔路径特征（`217.73` / `0.130127` / `0.130152`）。
2. 所有 PNG/ICO/ICNS 可解码，尺寸符合各自格式；1024 图标外边距完全透明；各副本与源一致。
3. 浏览器实际渲染：登录页与启动 splash 显示 N；标签页 favicon 为 N。
4. DMG 背景无 "ZCODE" 字样，Applications 拖入位置不被装饰占用。
5. `pnpm typecheck`、`pnpm lint` 通过；报告无法执行的运行时检查（桌面 About/安装包）。
