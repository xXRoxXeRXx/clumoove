import { useState, useEffect, useId, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useFocusTrap } from '../hooks/useFocusTrap';

interface AvatarCropperProps {
  file: File;
  onCrop: (croppedDataUrl: string) => void;
  onCancel: () => void;
}

export function AvatarCropper({ file, onCrop, onCancel }: AvatarCropperProps) {
  const { t } = useTranslation();
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const descriptionId = useId();
  const [image, setImage] = useState<HTMLImageElement | null>(null);
  const [zoom, setZoom] = useState<number>(1.0);
  const [panX, setPanX] = useState<number>(0);
  const [panY, setPanY] = useState<number>(0);
  const [isDragging, setIsDragging] = useState<boolean>(false);
  const [dragStart, setDragStart] = useState<{ x: number; y: number }>({ x: 0, y: 0 });

  useFocusTrap(dialogRef, cancelRef, onCancel);

  // Load file as image
  useEffect(() => {
    const reader = new FileReader();
    reader.onload = (e) => {
      const img = new Image();
      img.onload = () => {
        setImage(img);
      };
      img.src = e.target?.result as string;
    };
    reader.readAsDataURL(file);
  }, [file]);

  // Redraw when image, zoom, or pan changes
  useEffect(() => {
    if (!image || !canvasRef.current) return;
    const canvas = canvasRef.current;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    // Clear canvas
    ctx.clearRect(0, 0, 300, 300);

    // Calculate dimensions to maintain aspect ratio and fill the crop area
    const imgRatio = image.width / image.height;
    let drawW: number;
    let drawH: number;

    if (imgRatio > 1) {
      // Landscape: fit height to crop box, scale width
      drawH = 200;
      drawW = 200 * imgRatio;
    } else {
      // Portrait: fit width to crop box, scale height
      drawW = 200;
      drawH = 200 / imgRatio;
    }

    // Default centered position + user pan
    const x = (300 - drawW) / 2 + panX;
    const y = (300 - drawH) / 2 + panY;

    ctx.save();
    // Zoom around the center of the canvas (150, 150)
    ctx.translate(150, 150);
    ctx.scale(zoom, zoom);
    ctx.translate(-150, -150);

    ctx.drawImage(image, x, y, drawW, drawH);
    ctx.restore();

    // Draw dark transparent overlay outside the crop box
    ctx.fillStyle = 'rgba(15, 23, 42, 0.65)';
    
    // Top overlay
    ctx.fillRect(0, 0, 300, 50);
    // Bottom overlay
    ctx.fillRect(0, 250, 300, 50);
    // Left overlay
    ctx.fillRect(0, 50, 50, 200);
    // Right overlay
    ctx.fillRect(250, 50, 50, 200);

    // Draw a circular outline for cropping preview
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.85)';
    ctx.lineWidth = 2.5;
    ctx.beginPath();
    ctx.arc(150, 150, 100, 0, 2 * Math.PI);
    ctx.stroke();

    // Draw thin crop boundary helper
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.35)';
    ctx.lineWidth = 1;
    ctx.setLineDash([4, 4]);
    ctx.strokeRect(50, 50, 200, 200);
    ctx.setLineDash([]);
  }, [image, zoom, panX, panY]);

  const handlePointerDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId);
    setIsDragging(true);
    setDragStart({ x: e.clientX - panX, y: e.clientY - panY });
  };

  const handlePointerMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    if (!isDragging) return;
    setPanX(e.clientX - dragStart.x);
    setPanY(e.clientY - dragStart.y);
  };

  const handlePointerUp = () => {
    setIsDragging(false);
  };

  const handleWheel = (e: React.WheelEvent<HTMLCanvasElement>) => {
    const delta = -e.deltaY * 0.002;
    const newZoom = Math.max(1.0, Math.min(3.0, zoom + delta));
    setZoom(newZoom);
  };

  const handleCanvasKeyDown = (e: React.KeyboardEvent<HTMLCanvasElement>) => {
    const panStep = e.shiftKey ? 20 : 5;
    if (e.key === 'ArrowLeft') { e.preventDefault(); setPanX((value) => value - panStep); }
    else if (e.key === 'ArrowRight') { e.preventDefault(); setPanX((value) => value + panStep); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setPanY((value) => value - panStep); }
    else if (e.key === 'ArrowDown') { e.preventDefault(); setPanY((value) => value + panStep); }
    else if (e.key === '+' || e.key === '=') { e.preventDefault(); setZoom((value) => Math.min(3, value + 0.05)); }
    else if (e.key === '-') { e.preventDefault(); setZoom((value) => Math.max(1, value - 0.05)); }
  };

  const handleSave = () => {
    if (!image) return;

    // Create a 256x256 canvas for high-quality profile picture output
    const offscreen = document.createElement('canvas');
    offscreen.width = 256;
    offscreen.height = 256;
    const oCtx = offscreen.getContext('2d');
    if (!oCtx) return;

    const imgRatio = image.width / image.height;
    let drawW: number;
    let drawH: number;

    if (imgRatio > 1) {
      drawH = 200;
      drawW = 200 * imgRatio;
    } else {
      drawW = 200;
      drawH = 200 / imgRatio;
    }

    const x = (300 - drawW) / 2 + panX;
    const y = (300 - drawH) / 2 + panY;

    oCtx.save();
    // Scale from the 200x200 region to 256x256 output (scale factor = 256 / 200 = 1.28)
    oCtx.scale(256 / 200, 256 / 200);
    // Align crop box top-left (50, 50) with offscreen canvas (0, 0)
    oCtx.translate(-50, -50);

    // Apply zoom translation around center of crop box (150, 150)
    oCtx.translate(150, 150);
    oCtx.scale(zoom, zoom);
    oCtx.translate(-150, -150);

    oCtx.drawImage(image, x, y, drawW, drawH);
    oCtx.restore();

    onCrop(offscreen.toDataURL('image/png'));
  };

  return (
    <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/40 p-4">
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={descriptionId} className="max-w-sm w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-6 flex flex-col items-center">
        <h3 id={titleId} className="font-display font-extrabold text-lg text-[var(--color-portal-navy-themed)] mb-1">{t('settings.profilePicture')}</h3>
        <p id={descriptionId} className="text-[10px] text-[var(--color-text-muted)] font-mono tracking-wider mb-5 uppercase">{t('settings.avatarCropperTitle')}</p>

        {/* Canvas Area */}
        <div className="relative overflow-hidden rounded-md border border-[var(--color-border)] bg-[var(--color-bg-inverse)] group">
          <canvas
            ref={canvasRef}
            width={300}
            height={300}
            onPointerDown={handlePointerDown}
            onPointerMove={handlePointerMove}
            onPointerUp={handlePointerUp}
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

        {/* Zoom Slider Control */}
        <div className="w-full flex items-center gap-3 mt-5 px-1">
          <span className="text-xs text-[var(--color-text-muted)]" aria-hidden="true">−</span>
          <input
            aria-label={t('settings.avatarCropperTitle')}
            type="range"
            min="1.0"
            max="3.0"
            step="0.01"
            value={zoom}
            onChange={(e) => setZoom(parseFloat(e.target.value))}
            className="flex-grow accent-portal-orange h-1 bg-[var(--color-border)] rounded-lg appearance-none cursor-pointer"
          />
          <span className="text-xs text-[var(--color-text-muted)]" aria-hidden="true">+</span>
        </div>

        {/* Actions Button Grid */}
        <div className="w-full grid grid-cols-2 gap-3 mt-6">
          <button
            ref={cancelRef}
            type="button"
            onClick={onCancel}
            className="rounded-md border border-[var(--color-border)] py-2 text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]"
          >
            {t('common.cancel')}
          </button>
          <button
            type="button"
            onClick={handleSave}
            className="rounded-md bg-[var(--color-bg-inverse)] py-2 text-sm font-medium text-[var(--color-text-inverse)] hover:opacity-90"
          >
            {t('common.confirm')}
          </button>
        </div>
      </div>
    </div>
  );
}
