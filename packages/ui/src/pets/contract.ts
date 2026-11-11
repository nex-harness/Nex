/**
 * pets-ui 对外契约：宠物运行时在应用级挂载一次，设置页通过同一模块呈现目录与偏好。
 * 产品规则、状态所有者与失败语义见 docs/specs/desktop-pets.md。
 */
import type { ComponentType } from "react";

/** 挂载点：`Root` 应用级渲染一次；`isDesktop` 决定是否把状态推给桌面悬浮窗。 */
export interface PetRuntimeContract {
  isDesktop: boolean;
}

/** 设置页「宠物」分区：开关、显示方式、大小、宠物网格与刷新。 */
export interface PetsSettingsSectionContract {
  isDesktop: boolean;
}

/** 公开组件（React 组件同时是类型与值，契约只声明形状供示例与调用方引用）。 */
export type PetRuntimeComponent = ComponentType<PetRuntimeContract>;
export type PetsSettingsSectionComponent = ComponentType<PetsSettingsSectionContract>;
