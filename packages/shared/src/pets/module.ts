/**
 * pets-shared 模块清单：宠物格式契约（帧网格、manifest、物理、设置、窗口动作）。
 * 只放纯数据与纯函数，不依赖 Electron / Node IO；产品规则见 docs/specs/desktop-pets.md。
 */
export const petsSharedModule = {
  id: "pets-shared",
  requires: [],
  provides: ["pets-shared"],
  publicEntrypoints: ["index.ts", "contract.ts"],
} as const;
