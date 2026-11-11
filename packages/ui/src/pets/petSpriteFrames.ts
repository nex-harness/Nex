/**
 * 宠物精灵图帧切分（纯函数）：动画名 → 源矩形序列。
 * 行约定见 docs/specs/desktop-pets.md 与 @nex/shared 的 PET_DEFAULT_ANIMATIONS。
 */
import {
  PET_DEFAULT_ANIMATIONS,
  PET_DEFAULT_FRAME_GRID,
  PET_FRAME_ROWS_V2,
  PET_IDLE_DURATION_SCALE,
  PET_ONE_SHOT_REPEAT_COUNT,
  petLookSectorCell,
  type PetAnimationName,
  type PetFrameGrid,
  type PetManifest,
} from "@nex/shared";

export interface PetSpriteFrame {
  /** 源矩形（px，相对整图左上角）。 */
  sx: number;
  sy: number;
  sw: number;
  sh: number;
  /** 该帧展示时长（ms）。 */
  durationMs: number;
}

function gridOf(manifest: PetManifest | null): PetFrameGrid {
  return manifest?.frame ?? PET_DEFAULT_FRAME_GRID;
}

/** 行内连续帧（默认行约定）。 */
function framesFromRow(
  grid: PetFrameGrid,
  row: number,
  frameCount: number,
  durations: readonly number[],
): PetSpriteFrame[] {
  const frames: PetSpriteFrame[] = [];
  for (let column = 0; column < frameCount; column += 1) {
    frames.push({
      sx: column * grid.width,
      sy: row * grid.height,
      sw: grid.width,
      sh: grid.height,
      durationMs: durations[column] ?? 120,
    });
  }
  return frames;
}

/** manifest 自定义动画（帧索引行优先 + fps 折算）。 */
function framesFromSpec(
  grid: PetFrameGrid,
  spec: { frames: number[]; fps?: number },
): PetSpriteFrame[] {
  const durationMs = spec.fps ? 1000 / spec.fps : 125;
  return spec.frames.map((frameIndex) => {
    const row = Math.floor(frameIndex / grid.columns);
    const column = frameIndex % grid.columns;
    return {
      sx: column * grid.width,
      sy: row * grid.height,
      sw: grid.width,
      sh: grid.height,
      durationMs,
    };
  });
}

/**
 * 解析某只宠物某个动画的帧序列。
 * 优先 manifest.animations 覆盖；否则走默认行约定。
 * 动画名不存在时回退 idle。
 */
export function resolvePetAnimationFrames(
  manifest: PetManifest | null,
  animationName: string,
): PetSpriteFrame[] {
  const grid = gridOf(manifest);

  const custom = manifest?.animations?.[animationName];
  if (custom) {
    return framesFromSpec(grid, custom);
  }

  const defaultSpec = PET_DEFAULT_ANIMATIONS[animationName as PetAnimationName];
  if (defaultSpec) {
    // 默认行约定只在「网格与官方一致」时安全；自定义网格下帧行可能越界，
    // 越界时回退 idle（行 0 总是存在，因为网格校验要求铺满）。
    if (
      grid.width === PET_DEFAULT_FRAME_GRID.width &&
      grid.height === PET_DEFAULT_FRAME_GRID.height &&
      grid.columns === PET_DEFAULT_FRAME_GRID.columns &&
      grid.rows === PET_DEFAULT_FRAME_GRID.rows
    ) {
      return framesFromRow(
        grid,
        defaultSpec.row,
        defaultSpec.frameCount,
        defaultSpec.frameDurationsMs,
      );
    }
    if (animationName !== "idle") {
      return resolvePetAnimationFrames(manifest, "idle");
    }
    // 自定义网格 + idle：取行 0 的全部列。
    return framesFromRow(
      grid,
      0,
      grid.columns,
      Array.from({ length: grid.columns }, () => 200),
    );
  }

  // 未知动画名回退 idle。
  if (animationName !== "idle") {
    return resolvePetAnimationFrames(manifest, "idle");
  }
  return framesFromRow(grid, 0, Math.min(6, grid.columns), [280, 110, 110, 140, 140, 320]);
}

export interface PetPlaybackSequence {
  frames: PetSpriteFrame[];
  /** 播放到末尾后回绕的起点；idle 为 0，一次性动画指向末尾的 idle 段。 */
  loopStartIndex: number;
}

/**
 * 播放序列（对齐 Codex）：idle 逐帧时长 ×6 循环；其余状态播放 3 遍后落回 idle 循环。
 * manifest 自定义动画 `loop: false` 同样按一次性处理，`loop: true` 直接循环。
 */
export function buildPetPlaybackSequence(
  manifest: PetManifest | null,
  animationName: string,
): PetPlaybackSequence {
  const idleFrames = resolvePetAnimationFrames(manifest, "idle").map((frame) => ({
    ...frame,
    durationMs: frame.durationMs * PET_IDLE_DURATION_SCALE,
  }));
  if (animationName === "idle") {
    return { frames: idleFrames, loopStartIndex: 0 };
  }
  const base = resolvePetAnimationFrames(manifest, animationName);
  if (manifest?.animations?.[animationName]?.loop === true) {
    return { frames: base, loopStartIndex: 0 };
  }
  const repeated: PetSpriteFrame[] = [];
  for (let round = 0; round < PET_ONE_SHOT_REPEAT_COUNT; round += 1) repeated.push(...base);
  return { frames: [...repeated, ...idleFrames], loopStartIndex: repeated.length };
}

/** 「看向光标」单帧：仅 v2（≥11 行）且默认网格时可用，否则返回 null。 */
export function resolvePetLookFrame(
  manifest: PetManifest | null,
  spriteRows: number,
  sector: number,
): PetSpriteFrame | null {
  const grid = gridOf(manifest);
  if (
    spriteRows < PET_FRAME_ROWS_V2 ||
    grid.width !== PET_DEFAULT_FRAME_GRID.width ||
    grid.height !== PET_DEFAULT_FRAME_GRID.height ||
    grid.columns !== PET_DEFAULT_FRAME_GRID.columns
  ) {
    return null;
  }
  const { row, column } = petLookSectorCell(sector);
  return {
    sx: column * grid.width,
    sy: row * grid.height,
    sw: grid.width,
    sh: grid.height,
    durationMs: 1000,
  };
}
