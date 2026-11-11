import { z } from "zod";

const finiteNumber = z.number().finite();

/** pet-window → main 动作的运行时校验（IPC 入口不信任 renderer 载荷）。 */
export const petWindowActionSchema = z.discriminatedUnion("kind", [
  z.object({ kind: z.literal("focus-main-window") }),
  z.object({ kind: z.literal("show-context-menu") }),
  z.object({ kind: z.literal("drag-start"), pointerX: finiteNumber, pointerY: finiteNumber }),
  z.object({ kind: z.literal("drag-move"), pointerX: finiteNumber, pointerY: finiteNumber }),
  z.object({
    kind: z.literal("drag-end"),
    pointerX: finiteNumber,
    pointerY: finiteNumber,
    altKey: z.boolean(),
    velocity: z.object({ x: finiteNumber, y: finiteNumber }).optional(),
  }),
]);
