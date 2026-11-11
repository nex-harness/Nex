/**
 * 宠物浮动挂件（Web/Desktop 主窗口内）：fixed 定位、可拖拽换位、点击聚焦。
 * Desktop 的独立悬浮窗走另一条链路（IPlatformService.syncPetState），
 * 本组件是 Web 端呈现 + Desktop 悬浮窗不可用时的降级呈现。
 */
import { useCallback, useEffect, useRef, useState } from "react";
import {
  PET_FRAME_HEIGHT,
  PET_FRAME_WIDTH,
  resolvePetLookSector,
  type PetAnchor,
  type PetManifest,
  type PetSemanticState,
} from "@nex/shared";

import {
  resolvePetPresentation,
  updatePetDragDirection,
  type PetDragDirection,
} from "./petInteraction.js";
import { PetSprite } from "./PetSprite.js";

interface PetFloatingWidgetProps {
  spriteUrl: string;
  manifest: PetManifest | null;
  state: PetSemanticState;
  spriteRows: number;
  sizePx: number;
  anchor: PetAnchor;
  onAnchorChange: (anchor: PetAnchor) => void;
  onClick?: () => void;
}

const ANCHOR_CLASSNAMES: Record<PetAnchor, string> = {
  "bottom-right": "right-4 bottom-4",
  "bottom-left": "left-4 bottom-4",
  "top-right": "right-4 top-16",
  "top-left": "left-4 top-16",
};

function nearestAnchor(
  x: number,
  y: number,
  viewportWidth: number,
  viewportHeight: number,
): PetAnchor {
  const isLeft = x < viewportWidth / 2;
  const isTop = y < viewportHeight / 2;
  if (isTop) return isLeft ? "top-left" : "top-right";
  return isLeft ? "bottom-left" : "bottom-right";
}

export function PetFloatingWidget({
  spriteUrl,
  manifest,
  state,
  spriteRows,
  sizePx,
  anchor,
  onAnchorChange,
  onClick,
}: PetFloatingWidgetProps) {
  const [dragging, setDragging] = useState(false);
  const [dragOffset, setDragOffset] = useState<{ x: number; y: number } | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const dragStartRef = useRef<{ pointerX: number; pointerY: number } | null>(null);
  const movedRef = useRef(false);
  const [hovering, setHovering] = useState(false);
  const [lookSector, setLookSector] = useState<number | null>(null);
  const [dragDirection, setDragDirection] = useState<PetDragDirection | null>(null);
  const directionRef = useRef<{ direction: PetDragDirection | null; anchorX: number }>({
    direction: null,
    anchorX: 0,
  });

  const handlePointerDown = useCallback((event: React.PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    movedRef.current = false;
    directionRef.current = { direction: null, anchorX: event.clientX };
    dragStartRef.current = { pointerX: event.clientX, pointerY: event.clientY };
    const rect = rootRef.current?.getBoundingClientRect();
    if (rect) {
      setDragOffset({ x: event.clientX - rect.left, y: event.clientY - rect.top });
      setDragging(true);
      event.currentTarget.setPointerCapture(event.pointerId);
    }
  }, []);

  const handlePointerMove = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      const rect = event.currentTarget.getBoundingClientRect();
      setLookSector(
        resolvePetLookSector(
          event.clientX - (rect.left + rect.width / 2),
          event.clientY - (rect.top + rect.height / 2),
        ),
      );
      if (!dragging || !dragStartRef.current || !dragOffset) return;
      const dx = event.clientX - dragStartRef.current.pointerX;
      const dy = event.clientY - dragStartRef.current.pointerY;
      if (Math.abs(dx) + Math.abs(dy) > 4) {
        movedRef.current = true;
        const next = updatePetDragDirection(directionRef.current, event.clientX);
        if (next.direction !== directionRef.current.direction) setDragDirection(next.direction);
        directionRef.current = next;
      }
      const element = rootRef.current;
      if (element) {
        element.style.left = `${event.clientX - dragOffset.x}px`;
        element.style.top = `${event.clientY - dragOffset.y}px`;
        element.style.right = "auto";
        element.style.bottom = "auto";
      }
    },
    [dragging, dragOffset],
  );

  const handlePointerUp = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      if (!dragging) return;
      setDragging(false);
      setDragOffset(null);
      setDragDirection(null);
      event.currentTarget.releasePointerCapture(event.pointerId);
      if (movedRef.current) {
        const next = nearestAnchor(
          event.clientX,
          event.clientY,
          window.innerWidth,
          window.innerHeight,
        );
        onAnchorChange(next);
      } else {
        onClick?.();
      }
    },
    [dragging, onAnchorChange, onClick],
  );

  // 锚点变化时清除拖拽内联样式，回到 class 定位。
  useEffect(() => {
    const element = rootRef.current;
    if (!element) return;
    element.style.left = "";
    element.style.top = "";
    element.style.right = "";
    element.style.bottom = "";
  }, [anchor]);

  const presentation = resolvePetPresentation({
    semantic: state,
    dragDirection,
    hovering,
    spriteRows,
    lookSector,
  });

  return (
    <div
      ref={rootRef}
      className={`fixed z-[9998] cursor-pointer select-none transition-opacity ${ANCHOR_CLASSNAMES[anchor]} ${dragging ? "opacity-80" : ""}`}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={handlePointerUp}
      onPointerEnter={() => setHovering(true)}
      onPointerLeave={() => {
        if (dragging) return;
        setHovering(false);
        setLookSector(null);
      }}
      role="button"
      aria-label="pet"
      style={{ touchAction: "none" }}
    >
      <PetSprite
        spriteUrl={spriteUrl}
        manifest={manifest}
        spriteRows={spriteRows}
        animationName={presentation.animationName}
        lookSector={presentation.lookSector}
        heightPx={Math.round((sizePx * PET_FRAME_HEIGHT) / PET_FRAME_WIDTH)}
      />
    </div>
  );
}
