import { useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useFocusTrap } from '../hooks/useFocusTrap';

interface AvatarCropperProps {
  file: File;
  onCrop: (croppedDataUrl: string) => void;
  onCancel: () => void;
}

const MAX_AVATAR_FILE_SIZE = 2 * 1024 * 1024;
const AVATAR_MIME_TYPES = new Set(['image/png', 'image/jpeg', 'image/webp', 'image/gif']);
type PreviewImage = (HTMLImageElement | ImageBitmap) & { width: number; height: number };
type CanvasTokens = { overlay: string; stroke: string; boundary: string };
const isImageBitmap = (image: PreviewImage | null): image is ImageBitmap =>
  typeof ImageBitmap !== 'undefined' && image instanceof ImageBitmap;

const readCanvasTokens = (): CanvasTokens => {
  const tokens = getComputedStyle(document.documentElement);
  return {
    overlay: tokens.getPropertyValue('--color-avatar-canvas-overlay').trim(),
    stroke: tokens.getPropertyValue('--color-avatar-canvas-stroke').trim(),
    boundary: tokens.getPropertyValue('--color-avatar-canvas-boundary').trim(),
  };
};

export function AvatarCropper({ file, onCrop, onCancel }: AvatarCropperProps) {
  const { t } = useTranslation();
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const canvasTokensRef = useRef<CanvasTokens | null>(null);
  const titleId = useId();
  const descriptionId = useId();
  const [image, setImage] = useState<PreviewImage | null>(null);
  const [loadedFile, setLoadedFile] = useState<File | null>(null);
  const [zoom, setZoom] = useState(1);
  const [panX, setPanX] = useState(0);
  const [panY, setPanY] = useState(0);
  const [isDragging, setIsDragging] = useState(false);
  const [dragStart, setDragStart] = useState({ x: 0, y: 0 });
  const validationError = !AVATAR_MIME_TYPES.has(file.type)
    ? t('settings.messages.avatarInvalidType')
    : file.size > MAX_AVATAR_FILE_SIZE
      ? t('settings.messages.avatarTooLarge')
      : null;
  const activeImage = loadedFile === file ? image : null;

  useFocusTrap(dialogRef, cancelRef, onCancel);

  useEffect(() => {
    const updateTokens = () => {
      canvasTokensRef.current = readCanvasTokens();
    };
    updateTokens();
    const observer = new MutationObserver(updateTokens);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => observer.disconnect();
  }, []);

  // Decode only a small preview, and cancel the reader/image work when this dialog closes.
  useEffect(() => {
    let cancelled = false;
    let loadedImage: PreviewImage | null = null;
    const reader = new FileReader();

    if (validationError) {
      return () => { cancelled = true; };
    }

    const finish = (nextImage: PreviewImage) => {
      if (cancelled) {
        if (isImageBitmap(nextImage)) nextImage.close();
        return;
      }
      loadedImage = nextImage;
      setLoadedFile(file);
      setImage(nextImage);
    };

    const loadWithFileReader = () => {
      reader.onload = (event) => {
        if (cancelled) return;
        const preview = new Image();
        preview.onload = () => finish(preview);
        preview.src = event.target?.result as string;
      };
      reader.readAsDataURL(file);
    };

    const loadPreview = async () => {
      if (!('createImageBitmap' in window)) {
        loadWithFileReader();
        return;
      }
      try {
        const source = await createImageBitmap(file);
        if (cancelled) {
          source.close();
          return;
        }
        const scale = Math.min(1, 600 / Math.max(source.width, source.height));
        const canvas = document.createElement('canvas');
        canvas.width = Math.max(1, Math.round(source.width * scale));
        canvas.height = Math.max(1, Math.round(source.height * scale));
        const context = canvas.getContext('2d');
        if (!context) {
          source.close();
          loadWithFileReader();
          return;
        }
        context.drawImage(source, 0, 0, canvas.width, canvas.height);
        source.close();
        finish(await createImageBitmap(canvas));
      } catch {
        if (!cancelled) loadWithFileReader();
      }
    };

    void loadPreview();
    return () => {
      cancelled = true;
      if (reader.readyState === FileReader.LOADING) reader.abort();
      if (isImageBitmap(loadedImage)) loadedImage.close();
    };
  }, [file, validationError]);

  useEffect(() => {
    if (!activeImage || !canvasRef.current) return;
    const canvas = canvasRef.current;
    const context = canvas.getContext('2d');
    if (!context) return;

    const ratio = activeImage.width / activeImage.height;
    const drawWidth = ratio > 1 ? 200 * ratio : 200;
    const drawHeight = ratio > 1 ? 200 : 200 / ratio;
    const x = (300 - drawWidth) / 2 + panX;
    const y = (300 - drawHeight) / 2 + panY;
    const tokens = canvasTokensRef.current ?? readCanvasTokens();

    context.clearRect(0, 0, 300, 300);
    context.save();
    context.translate(150, 150);
    context.scale(zoom, zoom);
    context.translate(-150, -150);
    context.drawImage(activeImage, x, y, drawWidth, drawHeight);
    context.restore();

    context.fillStyle = tokens.overlay;
    context.fillRect(0, 0, 300, 50);
    context.fillRect(0, 250, 300, 50);
    context.fillRect(0, 50, 50, 200);
    context.fillRect(250, 50, 50, 200);
    context.strokeStyle = tokens.stroke;
    context.lineWidth = 2.5;
    context.beginPath();
    context.arc(150, 150, 100, 0, 2 * Math.PI);
    context.stroke();
    context.strokeStyle = tokens.boundary;
    context.lineWidth = 1;
    context.setLineDash([4, 4]);
    context.strokeRect(50, 50, 200, 200);
    context.setLineDash([]);
  }, [activeImage, zoom, panX, panY]);

  const handlePointerDown = (event: React.PointerEvent<HTMLCanvasElement>) => {
    event.currentTarget.setPointerCapture(event.pointerId);
    setIsDragging(true);
    setDragStart({ x: event.clientX - panX, y: event.clientY - panY });
  };

  const handlePointerMove = (event: React.PointerEvent<HTMLCanvasElement>) => {
    if (!isDragging) return;
    setPanX(event.clientX - dragStart.x);
    setPanY(event.clientY - dragStart.y);
  };

  const handlePointerUp = (event: React.PointerEvent<HTMLCanvasElement>) => {
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    setIsDragging(false);
  };

  const handleWheel = (event: React.WheelEvent<HTMLCanvasElement>) => {
    event.preventDefault();
    setZoom((value) => Math.max(1, Math.min(3, value - event.deltaY * 0.002)));
  };

  const handleCanvasKeyDown = (event: React.KeyboardEvent<HTMLCanvasElement>) => {
    const panStep = event.shiftKey ? 20 : 5;
    if (event.key === 'ArrowLeft') { event.preventDefault(); setPanX((value) => value - panStep); }
    else if (event.key === 'ArrowRight') { event.preventDefault(); setPanX((value) => value + panStep); }
    else if (event.key === 'ArrowUp') { event.preventDefault(); setPanY((value) => value - panStep); }
    else if (event.key === 'ArrowDown') { event.preventDefault(); setPanY((value) => value + panStep); }
    else if (event.key === '+' || event.key === '=') { event.preventDefault(); setZoom((value) => Math.min(3, value + 0.05)); }
    else if (event.key === '-') { event.preventDefault(); setZoom((value) => Math.max(1, value - 0.05)); }
  };

  const handleSave = () => {
    if (!activeImage) return;
    const offscreen = document.createElement('canvas');
    offscreen.width = 256;
    offscreen.height = 256;
    const context = offscreen.getContext('2d');
    if (!context) return;

    const ratio = activeImage.width / activeImage.height;
    const drawWidth = ratio > 1 ? 200 * ratio : 200;
    const drawHeight = ratio > 1 ? 200 : 200 / ratio;
    const x = (300 - drawWidth) / 2 + panX;
    const y = (300 - drawHeight) / 2 + panY;

    context.save();
    context.scale(256 / 200, 256 / 200);
    context.translate(-50, -50);
    context.translate(150, 150);
    context.scale(zoom, zoom);
    context.translate(-150, -150);
    context.drawImage(activeImage, x, y, drawWidth, drawHeight);
    context.restore();
    onCrop(offscreen.toDataURL('image/png'));
  };

  return (
    <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={descriptionId} tabIndex={-1} className="ui-section-elevated max-w-sm w-full p-6 flex flex-col items-center">
        <h3 id={titleId} className="font-display font-extrabold text-lg text-[var(--color-text-primary)] mb-1">{t('settings.profilePicture')}</h3>
        <p id={descriptionId} className="text-xs text-[var(--color-text-muted)] font-mono tracking-wider mb-5 uppercase">{t('settings.avatarCropperTitle')}</p>

        {validationError ? (
          <p role="alert" className="ui-alert ui-alert-error w-full mb-4 text-sm">{validationError}</p>
        ) : (
          <div className="relative overflow-hidden rounded-md border border-[var(--color-border)] bg-[var(--color-bg-inverse)] group">
            <canvas
              ref={canvasRef}
              width={300}
              height={300}
              onPointerDown={handlePointerDown}
              onPointerMove={handlePointerMove}
              onPointerUp={handlePointerUp}
              onPointerCancel={handlePointerUp}
              onWheel={handleWheel}
              onKeyDown={handleCanvasKeyDown}
              tabIndex={0}
              role="img"
              aria-label={`${t('settings.avatarCropperHint')} (${Math.round(zoom * 100)}%)`}
              className="cursor-move block touch-none"
            />
            <div className="absolute bottom-2 left-2 right-2 text-center text-[9px] font-mono text-[var(--color-text-inverse)]/50 pointer-events-none opacity-0 group-hover:opacity-100 transition-opacity">
              {t('settings.avatarCropperHint')}
            </div>
          </div>
        )}

        <div className="w-full flex items-center gap-3 mt-5 px-1">
          <span className="text-xs text-[var(--color-text-muted)]" aria-hidden="true">−</span>
          <input
            aria-label={t('settings.avatarCropperZoomLabel')}
            type="range"
            min="1"
            max="3"
            step="0.01"
            value={zoom}
            disabled={!activeImage || !!validationError}
            onChange={(event) => setZoom(parseFloat(event.target.value))}
            className="flex-grow accent-[var(--color-text-primary)] h-1 bg-[var(--color-border)] rounded-lg appearance-none cursor-pointer disabled:cursor-not-allowed"
          />
          <span className="text-xs text-[var(--color-text-muted)]" aria-hidden="true">+</span>
        </div>

        <div className="w-full grid grid-cols-2 gap-3 mt-6">
          <button ref={cancelRef} type="button" onClick={onCancel} className="ui-button-secondary py-2 text-sm hover:bg-[var(--color-bg-tertiary)]">
            {t('common.cancel')}
          </button>
          <button type="button" onClick={handleSave} disabled={!activeImage || !!validationError} className="ui-button-primary py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50">
            {t('common.confirm')}
          </button>
        </div>
      </div>
    </div>
  );
}
