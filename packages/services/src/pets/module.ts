/**
 * pets 模块清单：桌面宠物目录（~/.nex/pets/）的扫描、校验与精灵图路径解析。
 * 依赖声明与 architecture-policy.yaml 保持一致；对外只暴露 contract.ts。
 */
export const petsModule = {
  id: "pets",
  requires: ["shared", "services"],
  provides: ["pets-service"],
  publicEntrypoints: ["contract.ts"],
} as const;
