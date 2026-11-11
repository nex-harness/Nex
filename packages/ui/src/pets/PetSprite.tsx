/**
 * 宠物精灵图 canvas 播放器。
 * - drawImage 源矩形切帧；播放序列语义见 buildPetPlaybackSequence。
 * - lookSector 非空时显示「看向光标」静态帧（仅 v2 精灵图生效）。
 * - prefers-reduced-motion 时只画首帧。
 * - 图片解码失败显示空白占位，不抛错。
 */
import { useEffect, useMemo, useRef, useState, type ComponentType } from "react";
import { PET_DISPLAY_HEIGHT_PX, type PetManifest } from "@nex/shared";

import { logger } from "@/logger.js";

import { buildPetPlaybackSequence, resolvePetLookFrame } from "./petSpriteFrames.js";

export interface PetSpriteProps {
  spriteUrl: string;
  manifest: PetManifest | null;
  animationName: string;
  /** 精灵图行数；缺省按 9 行（无看向光标）。 */
  spriteRows?: number;
  /** 看向光标扇区（0..15）；null/undefined 表示不覆盖。 */
  lookSector?: number | null;
  /** 展示高度（px）；宽度按帧宽高比自动推导。 */
  heightPx?: number;
  className?: string;
}

export type PetSpriteComponent = ComponentType<PetSpriteProps>;

/** 精灵播放器：按动画名逐帧绘制，lookSector 非空时固定显示「看向光标」帧。 */
export const PetSprite: PetSpriteComponent = (props: PetSpriteProps) => {
  const {
    spriteUrl,
    manifest,
    animationName,
    spriteRows = 9,
    lookSector = null,
    heightPx = PET_DISPLAY_HEIGHT_PX,
    className,
  } = props;
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [image, setImage] = useState<HTMLImageElement | null>(null);

  const lookFrame = useMemo(
    () => (lookSector === null ? null : resolvePetLookFrame(manifest, spriteRows, lookSector)),
    [manifest, spriteRows, lookSector],
  );
  const sequence = useMemo(
    () => buildPetPlaybackSequence(manifest, animationName),
    [manifest, animationName],
  );

  useEffect(() => {
    let cancelled = false;
    const next = new Image();
    next.decoding = "async";
    next.src = spriteUrl;
    next
      .decode()
      .then(() => {
        if (!cancelled) setImage(next);
      })
      .catch((error: unknown) => {
        logger.warn(
          `[pets] 精灵图解码失败 ${spriteUrl}: ${error instanceof Error ? error.message : String(error)}`,
        );
      });
    return () => {
      cancelled = true;
    };
  }, [spriteUrl]);

  // 动画切换/看向变化会重启计时器：避免 idle 长帧（最长 1.9s）延迟状态响应。
  useEffect(() => {
    const canvas = canvasRef.current;
    const context = canvas?.getContext("2d");
    if (!canvas || !context || !image) return;

    const reducedMotion =
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;

    const draw = (frame: { sx: number; sy: number; sw: number; sh: number }) => {
      const scale = heightPx / frame.sh;
      const widthPx = Math.round(frame.sw * scale);
      const dpr = window.devicePixelRatio || 1;
      if (canvas.width !== widthPx * dpr || canvas.height !== heightPx * dpr) {
        canvas.width = widthPx * dpr;
        canvas.height = heightPx * dpr;
        canvas.style.width = `${widthPx}px`;
        canvas.style.height = `${heightPx}px`;
      }
      context.setTransform(dpr, 0, 0, dpr, 0, 0);
      context.clearRect(0, 0, widthPx, heightPx);
      context.imageSmoothingEnabled = false;
      context.drawImage(image, frame.sx, frame.sy, frame.sw, frame.sh, 0, 0, widthPx, heightPx);
    };

    if (lookFrame) {
      draw(lookFrame);
      return;
    }

    const { frames, loopStartIndex } = sequence;
    let index = 0;
    let timerId: ReturnType<typeof setTimeout> | null = null;
    const first = frames[0];
    if (first) draw(first);
    if (reducedMotion) return;

    const tick = () => {
      timerId = setTimeout(() => {
        index += 1;
        if (index >= frames.length) index = loopStartIndex;
        const frame = frames[index];
        if (frame) draw(frame);
        tick();
      }, frames[index]?.durationMs ?? 120);
    };
    tick();

    return () => {
      if (timerId) clearTimeout(timerId);
    };
  }, [image, sequence, lookFrame, heightPx]);

  return <canvas ref={canvasRef} className={className} aria-hidden="true" />;
};
