/**
 * pets-desktop 模块清单：桌面宠物悬浮窗的 main 侧控制器与纯布局计算。
 * preload 与独立 renderer（`src/preload/petWindow.ts`、`src/renderer/pets/`）同属本模块，
 * 但 renderer/preload 各自是独立编译目标，不参与 main 内部依赖。
 * 窗口层级与 Dock 边界见 docs/specs/desktop-pets.md。
 */
export const petsDesktopModule = {
  id: "pets-desktop",
  requires: ["shared", "ui", "pets-ui"],
  provides: ["pets-desktop"],
  publicEntrypoints: ["petWindow.ts"],
} as const;
