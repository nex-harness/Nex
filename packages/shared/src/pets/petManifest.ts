/**
 * Codex 兼容 pet.json manifest 的 zod schema 与校验。
 * 字段与校验规则来自 openai/codex `codex-rs/tui/src/pets/model.rs`，
 * 产品约束见 docs/specs/desktop-pets.md。
 */
import { z } from "zod";

import {
  PET_DEFAULT_SPRITESHEET_FILE_NAME,
  PET_FRAME_COLUMNS,
  PET_FRAME_HEIGHT,
  PET_FRAME_ROWS,
  PET_FRAME_WIDTH,
  PET_MAX_FPS,
  PET_MAX_FRAMES,
} from "./petFormat.js";

const petFrameGridSchema = z.object({
  width: z.number().int().positive(),
  height: z.number().int().positive(),
  columns: z.number().int().positive(),
  rows: z.number().int().positive(),
});

const petAnimationSpecSchema = z.object({
  /** 精灵索引（行优先，0..columns*rows-1）。 */
  frames: z.array(z.number().int().nonnegative()).min(1),
  fps: z.number().positive().max(PET_MAX_FPS).optional(),
  loop: z.boolean().optional(),
  fallback: z.string().optional(),
});

export const petManifestSchema = z.object({
  id: z.string().min(1).optional(),
  displayName: z.string().optional(),
  description: z.string().optional(),
  /** 仅允许目录内相对路径。 */
  spritesheetPath: z.string().min(1).optional(),
  frame: petFrameGridSchema.optional(),
  animations: z.record(z.string(), petAnimationSpecSchema).optional(),
});
export type PetManifest = z.infer<typeof petManifestSchema>;
export type PetFrameGrid = z.infer<typeof petFrameGridSchema>;
export type PetAnimationSpec = z.infer<typeof petAnimationSpecSchema>;

export const PET_DEFAULT_FRAME_GRID: PetFrameGrid = {
  width: PET_FRAME_WIDTH,
  height: PET_FRAME_HEIGHT,
  columns: PET_FRAME_COLUMNS,
  rows: PET_FRAME_ROWS,
};

export type PetManifestValidation =
  | { ok: true; manifest: PetManifest }
  | { ok: false; reason: string };

/**
 * 解析并校验 pet.json 文本。除 schema 外强制不变量：
 * spritesheetPath 必须是目录内相对路径；帧网格必须精确铺满整图（由调用方
 * 在拿到图片尺寸后另行核对，这里只校验网格自身合法性）；动画帧索引、
 * 总数、fallback 引用合法。
 */
export function parsePetManifest(text: string, dirName: string): PetManifestValidation {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { ok: false, reason: "pet.json 不是合法 JSON" };
  }
  const parsed = petManifestSchema.safeParse(raw);
  if (!parsed.success) {
    return {
      ok: false,
      reason: `pet.json 字段非法: ${parsed.error.issues[0]?.message ?? "unknown"}`,
    };
  }
  const manifest = parsed.data;

  const spritesheetPath = manifest.spritesheetPath ?? PET_DEFAULT_SPRITESHEET_FILE_NAME;
  if (!isDirectoryRelativePath(spritesheetPath)) {
    return { ok: false, reason: "spritesheetPath 必须是目录内相对路径（禁止绝对路径与 ..）" };
  }

  const grid = manifest.frame ?? PET_DEFAULT_FRAME_GRID;
  const totalFrames = grid.columns * grid.rows;
  if (totalFrames > PET_MAX_FRAMES) {
    return { ok: false, reason: `帧网格超过上限 ${PET_MAX_FRAMES}` };
  }

  if (manifest.animations) {
    for (const [name, spec] of Object.entries(manifest.animations)) {
      for (const frameIndex of spec.frames) {
        if (frameIndex >= totalFrames) {
          return { ok: false, reason: `动画 ${name} 引用了越界帧索引 ${frameIndex}` };
        }
      }
      if (spec.fallback && !(spec.fallback in manifest.animations)) {
        return { ok: false, reason: `动画 ${name} 的 fallback 指向不存在的动画 ${spec.fallback}` };
      }
    }
  }

  // id 缺省取目录名（与 codex model.rs 一致）。
  if (!manifest.id) {
    manifest.id = dirName;
  }
  return { ok: true, manifest };
}

/** 目录内相对路径：非绝对路径、无 .. 段、无盘符。 */
export function isDirectoryRelativePath(candidate: string): boolean {
  if (candidate.length === 0) return false;
  if (candidate.startsWith("/") || candidate.startsWith("\\")) return false;
  if (/^[A-Za-z]:[\\/]/.test(candidate)) return false;
  const segments = candidate.split(/[\\/]+/);
  return segments.every((segment) => segment !== ".." && segment.length > 0);
}

/**
 * 网格铺满校验：必须在拿到图片真实尺寸后调用（petService 在读图头后执行）。
 *
 * 官方 Codex 硬校验整图必须精确等于 1536×1872；这里放宽为「宽度匹配、
 * 高度为帧高整数倍且不小于声明行数」，以兼容社区魔改宠物的加行扩展
 * （如 1536×2288 的 11 行变体），同时仍保证声明的帧网格铺满对应区域。
 * 详见 docs/specs/desktop-pets.md。
 */
export function isGridTilingImage(
  grid: PetFrameGrid,
  imageWidth: number,
  imageHeight: number,
): boolean {
  if (grid.width * grid.columns !== imageWidth) return false;
  if (imageHeight % grid.height !== 0) return false;
  return imageHeight / grid.height >= grid.rows;
}
