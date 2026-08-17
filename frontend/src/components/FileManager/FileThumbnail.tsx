import { useEffect, useRef, useState } from 'react';
import { FileIcon } from '../icons';
import { type FileEntry } from '../../api/files';
import {
  getCachedThumbnail,
  isThumbnailPermanentlyFailed,
  requestThumbnail,
  type ThumbnailSize,
} from '../../utils/thumbnailLoader';

type FileThumbnailProps = {
  apiUrl: string;
  token: string;
  profileId: string;
  entry: FileEntry;
  thumbnailsEnabled: boolean;
  size?: ThumbnailSize;
  className?: string;
  imageClassName?: string;
  fallbackIconClassName?: string;
};

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
  const cachedUrl = shouldFetch ? getCachedThumbnail(profileId, entry.ref, size) : null;
  const thumbnailUrl = cachedUrl ?? fetchedUrl;
  const currentKey = `${profileId}:${entry.ref}:${size}`;
  const imageLoaded = !!loadedKeys[currentKey];

  useEffect(() => {
    if (!shouldFetch || cachedUrl !== null || isThumbnailPermanentlyFailed(profileId, entry.ref, size) || isVisible) return;
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
  }, [cachedUrl, entry.ref, isVisible, profileId, shouldFetch, size]);

  useEffect(() => {
    if (!shouldFetch || !isVisible || cachedUrl !== null || isThumbnailPermanentlyFailed(profileId, entry.ref, size)) return;

    const controller = new AbortController();
    let isMounted = true;

    const unsubscribe = requestThumbnail(
      {
        apiUrl,
        token,
        profileId,
        ref: entry.ref,
        size,
        signal: controller.signal,
      },
      (url) => {
        if (isMounted) {
          setFetchedUrl(url);
        }
      }
    );

    return () => {
      isMounted = false;
      unsubscribe();
      controller.abort();
    };
  }, [apiUrl, cachedUrl, entry.ref, isVisible, profileId, shouldFetch, size, token]);

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
            setLoadedKeys((prev) => (prev[currentKey] ? prev : { ...prev, [currentKey]: true }));
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
