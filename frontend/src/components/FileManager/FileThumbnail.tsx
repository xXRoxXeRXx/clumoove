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
}: FileThumbnailProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [isVisible, setIsVisible] = useState(() => typeof IntersectionObserver === 'undefined');
  const [fetchedUrl, setFetchedUrl] = useState<string | null>(null);

  const shouldFetch = thumbnailsEnabled && isProbableThumbnailCandidate(entry);
  const cacheKey = `${profileId}:${entry.ref}:${size}`;
  const cachedUrl = shouldFetch ? (thumbnailBlobCache.get(cacheKey) ?? null) : null;
  const thumbnailUrl = cachedUrl ?? fetchedUrl;

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
    const width = size === 'sm' ? 64 : 256;
    const height = size === 'sm' ? 64 : 256;

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

  if (thumbnailUrl) {
    return (
      <div ref={containerRef} className={`relative flex items-center justify-center overflow-hidden ${className || ''}`}>
        <img
          src={thumbnailUrl}
          alt={entry.name}
          className="h-full w-full object-contain rounded-md drop-shadow-xs transition-opacity duration-150"
          loading="lazy"
        />
      </div>
    );
  }

  return (
    <div ref={containerRef} className={`flex items-center justify-center ${className || ''}`}>
      <FileIcon
        name={entry.name}
        mimeType={entry.mime_type}
        isDir={entry.kind === 'directory'}
        className={className || (size === 'sm' ? 'h-5 w-5' : 'h-12 w-12')}
      />
    </div>
  );
}
