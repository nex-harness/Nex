/**
 * 桌面宠物跨进程/跨层共享类型。详见 docs/specs/desktop-pets.md。
 */

/** 宠物语义状态（与 PetAnimationName 对应的状态机输出）。 */
export type PetSemanticState = "idle" | "running" | "waiting" | "review" | "failed";

/** 桌面悬浮窗吸附区：水平三等分 × 垂直二等分。 */
export type PetSnapZone =
  | "top-left"
  | "top-center"
  | "top-right"
  | "bottom-left"
  | "bottom-center"
  | "bottom-right";

/** 桌面悬浮窗落点：全局屏幕坐标 + 所属显示器 + 可选吸附区（自由放置时缺省）。 */
export interface PetWindowPlacement {
  x: number;
  y: number;
  displayId?: number;
  snapZone?: PetSnapZone;
}

/** 宠物挂件/悬浮窗锚点。 */
export type PetAnchor = "bottom-right" | "bottom-left" | "top-right" | "top-left";

/** setting.json 中的宠物偏好（唯一所有者：ISettingService）。 */
export interface PetSettings {
  enabled: boolean;
  /** 选中的宠物目录 id；null = 未选择。 */
  petId: string | null;
  /** 挂件锚点（Web/Desktop 主窗口内）；悬浮窗位置由 desktop main 另行持久化。 */
  anchor?: PetAnchor;
  /** 桌面悬浮窗位置（全局屏幕坐标）；由 main 进程拖拽落定后写回。 */
  windowPosition?: { x: number; y: number };
  /** 悬浮窗落点所在显示器 id；显示器变化时用于还原到同一块屏幕。 */
  windowDisplayId?: number;
  /** 悬浮窗吸附区；缺省表示自由放置（Alt 拖放）。 */
  windowSnapZone?: PetSnapZone;
  /** 宠物宽度（px，80–224，缺省 112）。 */
  size?: number;
  /** 悬浮窗可见性；缺省 always。 */
  visibility?: import("./petFormat.js").PetVisibility;
}

/** 设置页网格条目。 */
export interface PetSummary {
  id: string;
  displayName: string;
  description: string;
  /** 宠物目录绝对路径（仅服务端/host 使用；不直接暴露给 renderer 渲染）。 */
  dirPath: string;
  /** 精灵图文件名（目录内相对）。 */
  spritesheetFileName: string;
  /** 精灵图行数（图高 / 帧高）；≥ 11 表示带「看向光标」环（v2）。 */
  spriteRows: number;
  /** 校验通过的 manifest（含可选 frame/animations 覆盖）。 */
  manifest: import("./petManifest.js").PetManifest;
}

/** 单个宠物目录加载失败。 */
export interface PetLoadError {
  dirName: string;
  reason: string;
}

export interface PetListResult {
  pets: PetSummary[];
  errors: PetLoadError[];
}

export interface PetWindowLabels {
  newChat: string;
  voice: string;
  comingSoon: string;
}

/** renderer → main：悬浮窗状态（null 表示销毁/隐藏）。 */
export interface PetWindowState {
  petId: string;
  animation: PetSemanticState;
  /** 精灵图可访问 URL（desktop: nex-media:// 授权 URL）。 */
  spriteUrl: string;
  /** 校验通过的 manifest（自定义网格/动画覆盖）；pet window 渲染需要。 */
  manifest: import("./petManifest.js").PetManifest | null;
  /** 精灵图行数，用于判断是否启用「看向光标」。 */
  spriteRows: number;
  /** 宠物宽度（px）。 */
  sizePx: number;
  /** 悬浮窗可见性；on-demand 时由 main 注册全局快捷键控制显隐。 */
  visibility: import("./petFormat.js").PetVisibility;
  /** 悬停控制行的本地化文案（pet window 没有 i18n 上下文，由主窗口 renderer 提供）。 */
  labels: PetWindowLabels;
  /** 建窗/显示器变化时还原的落点；窗口存在期间以用户拖拽为准。 */
  placement?: PetWindowPlacement;
}

/** 拖拽松手速度（px/s，已含 ×3 抛掷倍率）。 */
export interface PetReleaseVelocity {
  x: number;
  y: number;
}

/** pet-window → main：用户动作（指针坐标均为屏幕坐标）。 */
export type PetWindowAction =
  | { kind: "focus-main-window" }
  /** 右键：请求 main 弹出原生菜单（隐藏 / 设置）。 */
  | { kind: "show-context-menu" }
  | { kind: "drag-start"; pointerX: number; pointerY: number }
  | { kind: "drag-move"; pointerX: number; pointerY: number }
  | {
      kind: "drag-end";
      pointerX: number;
      pointerY: number;
      altKey: boolean;
      velocity?: PetReleaseVelocity;
    };
