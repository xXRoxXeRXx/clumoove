import { useEffect, useRef, useState } from 'react';
import { FileIcon } from '../icons';
import { getFileThumbnail, type FileEntry } from '../../api/files';

type FileThumbnailProps = {
  apiUrl: string;
  token: string;
  profileId: string;
  entry: FileEntry;
  thumbnailsEnabled: boolean;
  size?: 'sm' | 'lg';
  className?: string;
  imageClassName?: string;
  fallbackIconClassName?: string;
};

// In-memory cache for fetched thumbnail object URLs and failed refs
const thumbnailBlobCache = new Map<string, string>();
const failedThumbnailCache = new Set<string>();

function isProbableThumbnailCandidate(entry: FileEntry): boolean {
  if (entry.kind === 'directory') return false;
  const mime = (entry.mime_type || '').toLowerCase();
  if (mime.startsWith('image/') || mime.startsWith('video/')) return true;
  const ext = entry.name.split('.').pop()?.toLowerCase() || '';
  return ['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp', 'svg', 'heic', 'heif', 'tiff', 'tif', 'raw', 'mp4', 'mov'].includes(ext);
}

export function FileThumbnail({
  apiUrl,
  token,
  profileId,
  entry,
  thumbnailsEnabled,
  size = 'lg',
  className,
  imageClassName,
  fallbackIconClassName,
}: FileThumbnailProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [isVisible, setIsVisible] = useState(() => typeof IntersectionObserver === 'undefined');
  const [fetchedUrl, setFetchedUrl] = useState<string | null>(null);
  const [loadedKeys, setLoadedKeys] = useState<Record<string, boolean>>({});

  const shouldFetch = thumbnailsEnabled && isProbableThumbnailCandidate(entry);
  const cacheKey = `${profileId}:${entry.ref}:${size}`;
  const cachedUrl = shouldFetch ? (thumbnailBlobCache.get(cacheKey) ?? null) : null;
  const thumbnailUrl = cachedUrl ?? fetchedUrl;
  const imageLoaded = !!loadedKeys[cacheKey];

  useEffect(() => {
    if (!shouldFetch || cachedUrl !== null || failedThumbnailCache.has(cacheKey) || isVisible) return;
    const element = containerRef.current;
    if (!element || typeof IntersectionObserver === 'undefined') return;

    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) {
          setIsVisible(true);
          observer.disconnect();
        }
      },
      { rootMargin: '150px' }
    );

    observer.observe(element);
    return () => observer.disconnect();
  }, [cacheKey, cachedUrl, isVisible, shouldFetch]);

  useEffect(() => {
    if (!shouldFetch || !isVisible || cachedUrl !== null || failedThumbnailCache.has(cacheKey)) return;

    const controller = new AbortController();
    const width = size === 'sm' ? 128 : 256;
    const height = size === 'sm' ? 128 : 256;

    let isMounted = true;

    void getFileThumbnail(apiUrl, token, profileId, entry.ref, width, height, controller.signal).then((blob) => {
      if (!isMounted || controller.signal.aborted) return;
      if (blob && blob.size > 0) {
        const url = URL.createObjectURL(blob);
        thumbnailBlobCache.set(cacheKey, url);
        setFetchedUrl(url);
      } else {
        failedThumbnailCache.add(cacheKey);
      }
    });

    return () => {
      isMounted = false;
      controller.abort();
    };
  }, [apiUrl, cacheKey, cachedUrl, entry.ref, isVisible, profileId, shouldFetch, size, token]);

  return (
    <div ref={containerRef} className={`relative flex items-center justify-center overflow-hidden ${className || ''}`}>
      {/* Background FileIcon: rendered smoothly and faded out once image is loaded */}
      <div
        className={`flex items-center justify-center transition-opacity duration-300 ease-out ${
          imageLoaded ? 'opacity-0 pointer-events-none' : 'opacity-100'
        }`}
      >
        <FileIcon
          name={entry.name}
          mimeType={entry.mime_type}
          isDir={entry.kind === 'directory'}
          className={fallbackIconClassName || (size === 'sm' ? 'h-5 w-5' : 'h-12 w-12 drop-shadow-xs')}
        />
      </div>

      {thumbnailUrl && (
        <img
          src={thumbnailUrl}
          alt={entry.name}
          onLoad={() => {
            setLoadedKeys((prev) => (prev[cacheKey] ? prev : { ...prev, [cacheKey]: true }));
          }}
          className={`absolute inset-0 transition-opacity duration-300 ease-out ${
            imageLoaded ? 'opacity-100' : 'opacity-0'
          } ${imageClassName || 'h-full w-full object-contain rounded-md drop-shadow-xs'}`}
          loading="lazy"
        />
      )}
    </div>
  );
}
