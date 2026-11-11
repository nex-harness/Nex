/**
 * 宠物悬浮窗 renderer：订阅 main 推送的状态并渲染，上报点击、右键与拖拽指针事件。
 *
 * 拖拽状态机归 main 所有（pets/petWindow.ts）；这里只做手势识别：
 * pointer capture + 4px 死区区分点击与拖拽，上报屏幕坐标与松手速度，
 * 并在拖拽期间本地覆盖 running-left/right 动画、悬停时 jumping / 看向光标。
 * 精灵下方是悬停才显示的控制条（新对话 / 语音两个图标按钮），本期按钮禁用占位。
 */
import { createElement, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  PET_CONTROLS_HEIGHT_PX,
  PET_DRAG_DEAD_ZONE_PX,
  PET_FRAME_HEIGHT,
  PET_FRAME_WIDTH,
  PET_VELOCITY_WINDOW_MS,
  computePetReleaseVelocity,
  resolvePetLookSector,
  type PetPointerSample,
  type PetWindowAction,
  type PetWindowState,
} from "@nex/shared";
import {
  PetSprite,
  appendPetPointerSample,
  resolvePetPresentation,
  updatePetDragDirection,
  type PetDragDirection,
} from "@nex/ui/pets";

declare global {
  interface Window {
    nexPet?: {
      onState?(handler: (state: PetWindowState | null) => void): () => void;
      sendAction?(action: PetWindowAction): void;
    };
  }
}

const DRAG_SEND_INTERVAL_MS = 16;

interface GestureSession {
  pointerId: number;
  startX: number;
  startY: number;
  dragging: boolean;
  samples: PetPointerSample[];
  direction: { direction: PetDragDirection | null; anchorX: number };
  lastSentAt: number;
}

function sendAction(action: PetWindowAction) {
  window.nexPet?.sendAction?.(action);
}

const ICON_PROPS = {
  width: 16,
  height: 16,
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 2,
  strokeLinecap: "round",
  strokeLinejoin: "round",
  "aria-hidden": true,
} as const;

/** 新对话：笔 + 方框（与 Codex 控制条一致的「编辑」图标）。 */
const NewChatIcon = () =>
  createElement(
    "svg",
    ICON_PROPS,
    createElement("path", { d: "M12 20h9" }),
    createElement("path", { d: "M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" }),
  );

/** 语音：声波条。 */
const VoiceIcon = () =>
  createElement(
    "svg",
    ICON_PROPS,
    createElement("path", { d: "M4 10v4" }),
    createElement("path", { d: "M8 6v12" }),
    createElement("path", { d: "M12 3v18" }),
    createElement("path", { d: "M16 8v8" }),
    createElement("path", { d: "M20 11v2" }),
  );

/**
 * 控制条里的图标按钮：本期未实现，禁用并在 tooltip 里说明。
 * 只放图标（文字按钮在 112px 窗口里会被裁掉）；文案走 aria-label / title。
 */
function ControlButton(props: { label: string; hint: string; icon: typeof NewChatIcon }) {
  return createElement(
    "button",
    {
      type: "button",
      disabled: true,
      title: `${props.label} · ${props.hint}`,
      "aria-label": props.label,
      // 禁用按钮不吞指针事件，避免挡住外层的拖拽/右键手势。
      style: {
        pointerEvents: "none",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        width: 36,
        height: 28,
        padding: 0,
        border: "none",
        background: "transparent",
        color: "rgba(255,255,255,0.45)",
        cursor: "not-allowed",
      },
    },
    createElement(props.icon),
  );
}

function PetWindowApp() {
  const [state, setState] = useState<PetWindowState | null>(null);
  const [dragDirection, setDragDirection] = useState<PetDragDirection | null>(null);
  const [hovering, setHovering] = useState(false);
  const [lookSector, setLookSector] = useState<number | null>(null);
  const sessionRef = useRef<GestureSession | null>(null);
  const spriteRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => window.nexPet?.onState?.((next) => setState(next)) ?? (() => {}), []);

  const presentation = useMemo(
    () =>
      resolvePetPresentation({
        semantic: state?.animation ?? "idle",
        dragDirection,
        hovering,
        spriteRows: state?.spriteRows ?? 9,
        lookSector,
      }),
    [state?.animation, state?.spriteRows, dragDirection, hovering, lookSector],
  );

  if (!state) return null;

  const spriteHeightPx = Math.round((state.sizePx * PET_FRAME_HEIGHT) / PET_FRAME_WIDTH);

  const updateLook = (event: React.PointerEvent<HTMLDivElement>) => {
    // 看向光标以精灵（而非整个窗口）中心为原点。
    const rect = spriteRef.current?.getBoundingClientRect();
    if (!rect) return;
    setLookSector(
      resolvePetLookSector(
        event.clientX - (rect.left + rect.width / 2),
        event.clientY - (rect.top + rect.height / 2),
      ),
    );
  };

  const handlePointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || event.ctrlKey) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    sessionRef.current = {
      pointerId: event.pointerId,
      startX: event.screenX,
      startY: event.screenY,
      dragging: false,
      samples: [{ x: event.screenX, y: event.screenY, timeMs: event.timeStamp }],
      direction: { direction: null, anchorX: event.screenX },
      lastSentAt: 0,
    };
  };

  const handlePointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const session = sessionRef.current;
    if (!session || session.pointerId !== event.pointerId) {
      updateLook(event);
      return;
    }
    session.samples = appendPetPointerSample(
      session.samples,
      { x: event.screenX, y: event.screenY, timeMs: event.timeStamp },
      PET_VELOCITY_WINDOW_MS,
    );
    if (!session.dragging) {
      const moved = Math.max(
        Math.abs(event.screenX - session.startX),
        Math.abs(event.screenY - session.startY),
      );
      if (moved < PET_DRAG_DEAD_ZONE_PX) return;
      session.dragging = true;
      sendAction({ kind: "drag-start", pointerX: session.startX, pointerY: session.startY });
    }
    const nextDirection = updatePetDragDirection(session.direction, event.screenX);
    if (nextDirection.direction !== session.direction.direction) {
      setDragDirection(nextDirection.direction);
    }
    session.direction = nextDirection;

    if (event.timeStamp - session.lastSentAt >= DRAG_SEND_INTERVAL_MS) {
      session.lastSentAt = event.timeStamp;
      sendAction({ kind: "drag-move", pointerX: event.screenX, pointerY: event.screenY });
    }
  };

  const finishGesture = (event: React.PointerEvent<HTMLDivElement>, cancelled: boolean) => {
    const session = sessionRef.current;
    if (!session || session.pointerId !== event.pointerId) return;
    sessionRef.current = null;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    setDragDirection(null);
    // 捕获期间指针可能已移出窗口，此时不会再收到 pointerleave，需在松手时补判。
    const rect = event.currentTarget.getBoundingClientRect();
    const inside =
      event.clientX >= rect.left &&
      event.clientX <= rect.right &&
      event.clientY >= rect.top &&
      event.clientY <= rect.bottom;
    if (!inside) {
      setHovering(false);
      setLookSector(null);
    }
    if (!session.dragging) {
      if (!cancelled) sendAction({ kind: "focus-main-window" });
      return;
    }
    const velocity = computePetReleaseVelocity([
      ...session.samples,
      { x: event.screenX, y: event.screenY, timeMs: event.timeStamp },
    ]);
    sendAction({
      kind: "drag-end",
      pointerX: event.screenX,
      pointerY: event.screenY,
      altKey: event.altKey,
      ...(velocity ? { velocity } : {}),
    });
  };

  return createElement(
    "div",
    {
      onPointerDown: handlePointerDown,
      onPointerMove: handlePointerMove,
      onPointerUp: (event: React.PointerEvent<HTMLDivElement>) => finishGesture(event, false),
      onPointerCancel: (event: React.PointerEvent<HTMLDivElement>) => finishGesture(event, true),
      onPointerEnter: (event: React.PointerEvent<HTMLDivElement>) => {
        setHovering(true);
        updateLook(event);
      },
      onPointerLeave: () => {
        if (sessionRef.current) return;
        setHovering(false);
        setLookSector(null);
      },
      // 右键由 main 弹原生菜单（隐藏 / 设置）；同时阻止 Chromium 默认菜单。
      onContextMenu: (event: React.MouseEvent) => {
        event.preventDefault();
        sendAction({ kind: "show-context-menu" });
      },
      "data-pet-state": state.animation,
      style: {
        width: "100%",
        height: "100%",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        touchAction: "none",
        cursor: dragDirection ? "grabbing" : "grab",
      },
    },
    createElement(
      "div",
      { ref: spriteRef, style: { height: spriteHeightPx, flex: "none" } },
      createElement(PetSprite, {
        spriteUrl: state.spriteUrl,
        manifest: state.manifest,
        spriteRows: state.spriteRows,
        animationName: presentation.animationName,
        lookSector: presentation.lookSector,
        heightPx: spriteHeightPx,
      }),
    ),
    createElement(
      "div",
      {
        "data-pet-controls": true,
        style: {
          height: PET_CONTROLS_HEIGHT_PX,
          flex: "none",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          // 常态透明，悬停（含拖拽中指针仍在窗口内）才淡入。
          opacity: hovering && !dragDirection ? 1 : 0,
          transition: "opacity 120ms ease",
        },
      },
      // 单个胶囊容纳两个图标按钮，中间一条分隔线（对齐 Codex 的控制条）。
      createElement(
        "div",
        {
          style: {
            display: "flex",
            alignItems: "center",
            borderRadius: 16,
            background: "rgba(40,40,44,0.78)",
            border: "1px solid rgba(255,255,255,0.14)",
            backdropFilter: "blur(12px)",
          },
        },
        createElement(ControlButton, {
          label: state.labels.newChat,
          hint: state.labels.comingSoon,
          icon: NewChatIcon,
        }),
        createElement("div", {
          "aria-hidden": true,
          style: { width: 1, height: 14, background: "rgba(255,255,255,0.16)" },
        }),
        createElement(ControlButton, {
          label: state.labels.voice,
          hint: state.labels.comingSoon,
          icon: VoiceIcon,
        }),
      ),
    ),
  );
}

const container = document.getElementById("root");
if (container) {
  createRoot(container).render(createElement(PetWindowApp));
}
