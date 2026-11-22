import jsQR from 'jsqr';
import { Camera, CameraOff, ImageUp, LoaderCircle, QrCode, X } from 'lucide-react';
import { type ChangeEvent, type FormEvent, useCallback, useEffect, useRef, useState } from 'react';
import { useNexStore } from '../core/store';
import { hostBridge } from '../host/bridge';
import { formatMsg } from '../i18n/messages';

interface QRScannerModalProps {
  open: boolean;
  initialURI?: string;
  busy?: boolean;
  error?: string;
  onClose: () => void;
  onSubmitURI: (uri: string, deviceName: string) => Promise<void>;
}

interface BarcodeDetectorLike {
  detect(source: ImageBitmapSource): Promise<Array<{ rawValue?: string }>>;
}

export function QRScannerModal({
  open,
  initialURI = '',
  busy = false,
  error = '',
  onClose,
  onSubmitURI,
}: QRScannerModalProps) {
  const locale = useNexStore((s) => s.locale);
  const [uri, setUri] = useState(initialURI);
  const [deviceName, setDeviceName] = useState('');
  const [localError, setLocalError] = useState('');
  const [cameraActive, setCameraActive] = useState(false);

  const videoRef = useRef<HTMLVideoElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const streamRef = useRef<MediaStream | null>(null);

  useEffect(() => {
    if (initialURI) {
      setUri(initialURI);
      setLocalError('');
    }
  }, [initialURI]);

  const stopCamera = useCallback(() => {
    if (streamRef.current) {
      for (const track of streamRef.current.getTracks()) {
        track.stop();
      }
      streamRef.current = null;
    }
    if (videoRef.current) {
      videoRef.current.srcObject = null;
    }
    setCameraActive(false);
  }, []);

  useEffect(() => {
    if (!open) {
      stopCamera();
      setLocalError('');
    }
    return () => {
      stopCamera();
    };
  }, [open, stopCamera]);

  const startCamera = useCallback(async () => {
    setLocalError('');
    if (typeof navigator === 'undefined' || !navigator.mediaDevices?.getUserMedia) {
      setLocalError(formatMsg(locale, 'client.pair.cameraUnavailable'));
      return;
    }
    try {
      const stream = await navigator.mediaDevices.getUserMedia({
        video: { facingMode: { ideal: 'environment' } },
        audio: false,
      });
      streamRef.current = stream;
      setCameraActive(true);
      if (videoRef.current) {
        videoRef.current.srcObject = stream;
        await videoRef.current.play().catch(() => {});
      }
    } catch {
      setLocalError(formatMsg(locale, 'client.pair.cameraFailed'));
      stopCamera();
    }
  }, [locale, stopCamera]);

  // Automatically start rear camera on mobile hosts when modal opens without a deep-linked URI
  useEffect(() => {
    if (open && !initialURI && hostBridge.kind === 'mygo-mobile') {
      void startCamera();
    }
  }, [open, initialURI, startCamera]);

  // Scan video frames periodically using BarcodeDetector (if present) or jsQR fallback
  useEffect(() => {
    if (!open || !cameraActive) return;

    let cancelled = false;
    const canvas = document.createElement('canvas');
    const ctx = canvas.getContext('2d');
    const DetectorCtor = (globalThis as { BarcodeDetector?: new (opts?: { formats?: string[] }) => BarcodeDetectorLike })
      .BarcodeDetector;
    const detector = DetectorCtor ? new DetectorCtor({ formats: ['qr_code'] }) : null;

    const timer = window.setInterval(async () => {
      if (cancelled) return;
      const vid = videoRef.current;
      if (!vid || vid.readyState < 2 || vid.videoWidth <= 0 || vid.videoHeight <= 0) return;

      try {
        if (detector) {
          const hits = await detector.detect(vid);
          const found = hits.find((h) => h.rawValue && h.rawValue.startsWith('nex://pair'));
          if (found?.rawValue) {
            stopCamera();
            setUri(found.rawValue);
            return;
          }
        }
        if (ctx) {
          canvas.width = vid.videoWidth;
          canvas.height = vid.videoHeight;
          ctx.drawImage(vid, 0, 0, canvas.width, canvas.height);
          const imgData = ctx.getImageData(0, 0, canvas.width, canvas.height);
          const decoded = jsQR(imgData.data, imgData.width, imgData.height);
          if (decoded?.data && decoded.data.startsWith('nex://pair')) {
            stopCamera();
            setUri(decoded.data);
          }
        }
      } catch {
        // ignore transient frame decode errors
      }
    }, 180);

    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [open, cameraActive, stopCamera]);

  const handleImageUpload = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    setLocalError('');

    const imgUrl = URL.createObjectURL(file);
    try {
      const img = await new Promise<HTMLImageElement>((resolve, reject) => {
        const el = new Image();
        el.onload = () => resolve(el);
        el.onerror = () => reject(new Error('image_load_failed'));
        el.src = imgUrl;
      });
      const canvas = document.createElement('canvas');
      canvas.width = img.naturalWidth || img.width;
      canvas.height = img.naturalHeight || img.height;
      const ctx = canvas.getContext('2d');
      if (!ctx) {
        setLocalError(formatMsg(locale, 'client.pair.noQrFound'));
        return;
      }
      ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
      const data = ctx.getImageData(0, 0, canvas.width, canvas.height);
      const decoded = jsQR(data.data, data.width, data.height);
      if (!decoded?.data || !decoded.data.startsWith('nex://pair')) {
        setLocalError(formatMsg(locale, 'client.pair.noQrFound'));
        return;
      }
      setUri(decoded.data);
    } catch {
      setLocalError(formatMsg(locale, 'client.pair.noQrFound'));
    } finally {
      URL.revokeObjectURL(imgUrl);
    }
  };

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = uri.trim();
    if (!trimmed.startsWith('nex://pair')) {
      setLocalError(formatMsg(locale, 'client.pair.invalidUri'));
      return;
    }
    setLocalError('');
    void onSubmitURI(trimmed, deviceName.trim());
  };

  if (!open) return null;

  const displayError = error || localError;

  return (
    <div
      data-testid="qr-scanner-modal"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-4 backdrop-blur-xs"
      onClick={onClose}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-[440px] rounded-2xl border border-border bg-card p-5 text-foreground shadow-2xl"
      >
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-center gap-2.5">
            <span className="flex size-9 items-center justify-center rounded-xl border border-border bg-surface">
              <QrCode className="size-4" />
            </span>
            <div>
              <h2 className="text-base font-semibold text-foreground">{formatMsg(locale, 'client.pair.title')}</h2>
              <p className="mt-0.5 text-ui-xs leading-relaxed text-foreground-subtle">
                {formatMsg(locale, 'client.pair.subtitle')}
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="flex size-7 items-center justify-center rounded-lg text-foreground-subtle transition-colors hover:bg-hover hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        </div>

        <div className="mt-4 flex flex-wrap gap-2">
          <button
            type="button"
            data-testid="qr-camera-toggle-btn"
            onClick={() => {
              if (cameraActive) stopCamera();
              else void startCamera();
            }}
            className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
          >
            {cameraActive ? <CameraOff className="size-3.5" /> : <Camera className="size-3.5" />}
            <span>
              {cameraActive
                ? formatMsg(locale, 'client.pair.stopCamera')
                : formatMsg(locale, 'client.pair.startCamera')}
            </span>
          </button>

          <button
            type="button"
            data-testid="qr-upload-btn"
            onClick={() => fileInputRef.current?.click()}
            className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-ui-xs font-medium text-foreground transition-colors hover:bg-hover"
          >
            <ImageUp className="size-3.5" />
            <span>{formatMsg(locale, 'client.pair.uploadImage')}</span>
          </button>
          <input
            ref={fileInputRef}
            data-testid="qr-file-input"
            type="file"
            accept="image/*"
            onChange={(e) => void handleImageUpload(e)}
            className="hidden"
          />
        </div>

        {cameraActive && (
          <div className="mt-3 overflow-hidden rounded-xl border border-border bg-black">
            <video ref={videoRef} playsInline muted className="aspect-video w-full object-cover" />
          </div>
        )}

        <form onSubmit={handleSubmit} className="mt-4 space-y-3">
          <label className="block space-y-1">
            <span className="text-ui-xs font-medium text-foreground-subtle">
              {formatMsg(locale, 'client.pair.uriLabel')}
            </span>
            <textarea
              data-testid="qr-manual-uri-input"
              rows={2}
              value={uri}
              onChange={(e) => {
                setUri(e.target.value);
                if (localError) setLocalError('');
              }}
              placeholder={formatMsg(locale, 'client.pair.uriPlaceholder')}
              className="w-full rounded-xl border border-border bg-input px-3 py-2 font-mono text-ui-xs text-foreground placeholder-foreground-subtlest outline-none focus:border-brand-blue"
            />
          </label>

          <label className="block space-y-1">
            <span className="text-ui-xs font-medium text-foreground-subtle">
              {formatMsg(locale, 'client.pair.deviceNameLabel')}
            </span>
            <input
              data-testid="qr-device-name-input"
              type="text"
              value={deviceName}
              onChange={(e) => setDeviceName(e.target.value)}
              placeholder={formatMsg(locale, 'client.pair.deviceNamePlaceholder')}
              className="h-9 w-full rounded-xl border border-border bg-input px-3 text-ui-sm text-foreground placeholder-foreground-subtlest outline-none focus:border-brand-blue"
            />
          </label>

          {displayError && (
            <div
              data-testid="qr-pair-error"
              className="rounded-lg border border-destructive/30 bg-destructive-surface px-3 py-2 text-ui-xs text-destructive-surface-fg"
            >
              {displayError}
            </div>
          )}

          <div className="flex items-center justify-end gap-2 pt-1">
            <button
              type="button"
              onClick={onClose}
              className="inline-flex h-9 items-center rounded-xl border border-border bg-surface px-4 text-ui-sm font-medium text-foreground transition-colors hover:bg-hover"
            >
              {formatMsg(locale, 'client.pair.cancelBtn')}
            </button>
            <button
              type="submit"
              data-testid="qr-manual-submit-btn"
              disabled={!uri.trim() || busy}
              className="inline-flex h-9 items-center gap-2 rounded-xl bg-foreground px-4 text-ui-sm font-medium text-background transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-45"
            >
              {busy && <LoaderCircle className="size-4 animate-spin" />}
              <span>
                {busy
                  ? formatMsg(locale, 'client.pair.pairingBtn')
                  : formatMsg(locale, 'client.pair.submitBtn')}
              </span>
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
