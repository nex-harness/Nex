import { RotateCcw, RotateCw, X, ZoomIn, ZoomOut } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useNexStore } from '../core/store';
import { formatMsg } from '../i18n/messages';

const MIN_ZOOM = 0.5;
const MAX_ZOOM = 3;
const ZOOM_STEP = 0.25;

/**
 * Translucent fullscreen media lightbox for images and PDFs.
 * Close button sits in the top-right corner; zoom / rotate / reset actions sit at the bottom center.
 */
export function ImagePreviewModal() {
  const previewImage = useNexStore((s) => s.previewImage);
  const closeImagePreview = useNexStore((s) => s.closeImagePreview);
  const locale = useNexStore((s) => s.locale);
  const [zoom, setZoom] = useState(1);
  const [rotation, setRotation] = useState(0);

  useEffect(() => {
    setZoom(1);
    setRotation(0);
  }, [previewImage]);

  useEffect(() => {
    if (!previewImage) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        closeImagePreview();
      } else if (e.key === '+' || e.key === '=') {
        e.preventDefault();
        setZoom((z) => Math.min(MAX_ZOOM, Number((z + ZOOM_STEP).toFixed(2))));
      } else if (e.key === '-') {
        e.preventDefault();
        setZoom((z) => Math.max(MIN_ZOOM, Number((z - ZOOM_STEP).toFixed(2))));
      } else if (e.key === '0') {
        e.preventDefault();
        setZoom(1);
        setRotation(0);
      } else if (e.key === 'r' || e.key === 'R') {
        e.preventDefault();
        setRotation((deg) => (deg + 90) % 360);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [previewImage, closeImagePreview]);

  if (!previewImage) return null;

  const isPdf = previewImage.kind === 'pdf' || previewImage.src.startsWith('data:application/pdf');

  return (
    <div
      data-testid="image-preview-modal"
      role="dialog"
      aria-modal="true"
      aria-label={previewImage.alt || (isPdf ? 'PDF preview' : 'Image preview')}
      onClick={closeImagePreview}
      className="zcode-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-6 backdrop-blur-sm"
    >
      {/* Top-right close button */}
      <button
        type="button"
        data-testid="image-preview-close"
        onClick={(e) => {
          e.stopPropagation();
          closeImagePreview();
        }}
        title={formatMsg(locale, 'common.close')}
        aria-label={formatMsg(locale, 'common.close')}
        className="fixed right-5 top-5 z-10 flex size-10 items-center justify-center rounded-full border border-white/15 bg-black/60 text-white shadow-lg backdrop-blur-md transition-colors hover:bg-white/20"
      >
        <X className="size-5" />
      </button>

      {/* Main media content */}
      {isPdf ? (
        <div
          onClick={(e) => e.stopPropagation()}
          className="zcode-dialog-content flex h-[84vh] w-[88vw] max-w-5xl flex-col overflow-hidden rounded-2xl border border-white/15 bg-surface shadow-2xl"
        >
          {previewImage.alt ? (
            <div className="shrink-0 truncate border-b border-border bg-card px-4 py-2 text-ui-sm font-medium text-foreground">
              {previewImage.alt}
            </div>
          ) : null}
          <iframe
            src={previewImage.src}
            title={previewImage.alt || 'PDF preview'}
            className="h-full w-full flex-1 bg-white"
          />
        </div>
      ) : (
        <div
          onClick={(e) => e.stopPropagation()}
          className="zcode-dialog-content flex items-center justify-center"
        >
          <img
            data-testid="image-preview-img"
            src={previewImage.src}
            alt={previewImage.alt || 'preview'}
            style={{ transform: `scale(${zoom}) rotate(${rotation}deg)` }}
            className="max-h-[82vh] max-w-[90vw] select-none rounded-xl object-contain shadow-2xl transition-transform duration-150"
          />
        </div>
      )}

      {/* Bottom floating action bar (for images) */}
      {!isPdf ? (
        <div
          onClick={(e) => e.stopPropagation()}
          className="fixed bottom-6 left-1/2 z-10 flex -translate-x-1/2 items-center gap-1 rounded-full border border-white/15 bg-black/65 px-2.5 py-1.5 text-white shadow-xl backdrop-blur-md"
        >
          <button
            type="button"
            onClick={() => setZoom((z) => Math.max(MIN_ZOOM, Number((z - ZOOM_STEP).toFixed(2))))}
            title={formatMsg(locale, 'chat.imagePreview.zoomOut')}
            aria-label={formatMsg(locale, 'chat.imagePreview.zoomOut')}
            className="flex size-8 items-center justify-center rounded-full transition-colors hover:bg-white/15"
          >
            <ZoomOut className="size-4" />
          </button>
          <button
            type="button"
            onClick={() => {
              setZoom(1);
              setRotation(0);
            }}
            title={formatMsg(locale, 'chat.imagePreview.reset')}
            className="min-w-12 rounded-full px-2 py-0.5 text-center font-mono text-ui-xs tabular-nums transition-colors hover:bg-white/15"
          >
            {Math.round(zoom * 100)}%
          </button>
          <button
            type="button"
            onClick={() => setZoom((z) => Math.min(MAX_ZOOM, Number((z + ZOOM_STEP).toFixed(2))))}
            title={formatMsg(locale, 'chat.imagePreview.zoomIn')}
            aria-label={formatMsg(locale, 'chat.imagePreview.zoomIn')}
            className="flex size-8 items-center justify-center rounded-full transition-colors hover:bg-white/15"
          >
            <ZoomIn className="size-4" />
          </button>
          <div className="mx-1 h-4 w-px bg-white/15" />
          <button
            type="button"
            onClick={() => setRotation((deg) => (deg - 90 + 360) % 360)}
            title={formatMsg(locale, 'chat.imagePreview.rotateLeft')}
            aria-label={formatMsg(locale, 'chat.imagePreview.rotateLeft')}
            className="flex size-8 items-center justify-center rounded-full transition-colors hover:bg-white/15"
          >
            <RotateCcw className="size-4" />
          </button>
          <button
            type="button"
            onClick={() => setRotation((deg) => (deg + 90) % 360)}
            title={formatMsg(locale, 'chat.imagePreview.rotateRight')}
            aria-label={formatMsg(locale, 'chat.imagePreview.rotateRight')}
            className="flex size-8 items-center justify-center rounded-full transition-colors hover:bg-white/15"
          >
            <RotateCw className="size-4" />
          </button>
        </div>
      ) : null}
    </div>
  );
}
