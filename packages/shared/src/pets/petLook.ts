import { PET_FRAME_COLUMNS, PET_LOOK_FIRST_ROW, PET_LOOK_SECTOR_COUNT } from "./petFormat.js";

/**
 * 看向光标：以宠物中心为原点，angle = atan2(dx, -dy)（正上方为 0°，顺时针），
 * 16 个 22.5° 扇区。距中心不足 1px 时返回 null（回落默认动画）。
 */
export function resolvePetLookSector(dx: number, dy: number): number | null {
  if (Math.hypot(dx, dy) < 1) return null;
  const degrees = (Math.atan2(dx, -dy) * 180) / Math.PI;
  const sector = Math.round(degrees / (360 / PET_LOOK_SECTOR_COUNT));
  return ((sector % PET_LOOK_SECTOR_COUNT) + PET_LOOK_SECTOR_COUNT) % PET_LOOK_SECTOR_COUNT;
}

/** 扇区 → 精灵图单元格（9/10 行各 8 帧）。 */
export function petLookSectorCell(sector: number): { row: number; column: number } {
  return {
    row: PET_LOOK_FIRST_ROW + Math.floor(sector / PET_FRAME_COLUMNS),
    column: sector % PET_FRAME_COLUMNS,
  };
}
