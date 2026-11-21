import { RotateCcw, X, ZoomIn, ZoomOut } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useNexStore } from '../core/store';
import { formatMsg } from '../i18n/messages';

const MIN_ZOOM = 0.5;
const MAX_ZOOM = 3;
const ZOOM_STEP = 0.25;

/**
 * Translucent fullscreen image lightbox for user attachments and markdown images.
 */
export function ImagePreviewModal() {
  const previewImage = useNexStore((s) => s.previewImage);
  const closeImagePreview = useNexStore((s) => s.closeImagePreview);
  const locale = useNexStore((s) => s.locale);
  const [zoom, setZoom] = useState(1);

  useEffect(() => {
    setZoom(1);
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
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [previewImage, closeImagePreview]);

  if (!previewImage) return null;

  return (
    <div
      data-testid="image-preview-modal"
      role="dialog"
      aria-modal="true"
      aria-label={previewImage.alt || 'Image preview'}
      onClick={closeImagePreview}
      className="zcode-dialog-overlay fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-6 backdrop-blur-sm"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="fixed right-5 top-5 z-10 flex items-center gap-1 rounded-full border border-white/15 bg-black/55 px-2 py-1 text-white shadow-lg backdrop-blur-md"
      >
        <button
          type="button"
          onClick={() => setZoom((z) => Math.max(MIN_ZOOM, Number((z - ZOOM_STEP).toFixed(2))))}
          title={formatMsg(locale, 'chat.imagePreview.zoomOut')}
          aria-label={formatMsg(locale, 'chat.imagePreview.zoomOut')}
          className="flex size-7 items-center justify-center rounded-full transition-colors hover:bg-white/15"
        >
          <ZoomOut className="size-4" />
        </button>
        <button
          type="button"
          onClick={() => setZoom(1)}
          title={formatMsg(locale, 'chat.imagePreview.reset')}
          className="min-w-12 rounded-full px-1.5 py-0.5 text-center font-mono text-ui-xs tabular-nums transition-colors hover:bg-white/15"
        >
          {Math.round(zoom * 100)}%
        </button>
        <button
          type="button"
          onClick={() => setZoom((z) => Math.min(MAX_ZOOM, Number((z + ZOOM_STEP).toFixed(2))))}
          title={formatMsg(locale, 'chat.imagePreview.zoomIn')}
          aria-label={formatMsg(locale, 'chat.imagePreview.zoomIn')}
          className="flex size-7 items-center justify-center rounded-full transition-colors hover:bg-white/15"
        >
          <ZoomIn className="size-4" />
        </button>
        <button
          type="button"
          onClick={() => setZoom(1)}
          title={formatMsg(locale, 'chat.imagePreview.reset')}
          aria-label={formatMsg(locale, 'chat.imagePreview.reset')}
          className="flex size-7 items-center justify-center rounded-full transition-colors hover:bg-white/15"
        >
          <RotateCcw className="size-3.5" />
        </button>
        <div className="mx-0.5 h-4 w-px bg-white/15" />
        <button
          type="button"
          data-testid="image-preview-close"
          onClick={closeImagePreview}
          title={formatMsg(locale, 'common.close')}
          aria-label={formatMsg(locale, 'common.close')}
          className="flex size-7 items-center justify-center rounded-full transition-colors hover:bg-white/20"
        >
          <X className="size-4" />
        </button>
      </div>

      <img
        src={previewImage.src}
        alt={previewImage.alt || 'preview'}
        onClick={(e) => e.stopPropagation()}
        style={{ transform: `scale(${zoom})` }}
        className="zcode-dialog-content max-h-[85vh] max-w-[90vw] select-none rounded-xl object-contain shadow-2xl transition-transform duration-150"
      />
    </div>
  );
}
