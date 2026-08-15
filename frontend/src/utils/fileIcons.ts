export type FileCategory =
  | 'folder'
  | 'image'
  | 'video'
  | 'audio'
  | 'document'
  | 'code'
  | 'archive'
  | 'file';

export function getFileCategory(
  name: string,
  mimeType?: string | null,
  isDir?: boolean
): FileCategory {
  if (isDir || name.endsWith('/')) {
    return 'folder';
  }

  if (mimeType) {
    const mime = mimeType.split(';', 1)[0].trim().toLowerCase();
    if (mime && mime !== 'application/octet-stream') {
      if (mime.startsWith('image/')) return 'image';
      if (mime.startsWith('video/')) return 'video';
      if (mime.startsWith('audio/')) return 'audio';
      if (
        mime === 'application/pdf' ||
        mime === 'application/msword' ||
        mime.includes('officedocument') ||
        mime.includes('oasis.opendocument') ||
        mime.includes('spreadsheet') ||
        mime.includes('presentation') ||
        mime === 'text/plain' ||
        mime === 'text/markdown' ||
        mime === 'text/csv' ||
        mime === 'text/tab-separated-values' ||
        mime === 'application/rtf' ||
        mime === 'text/rtf' ||
        mime === 'application/epub+zip'
      ) {
        return 'document';
      }
      if (
        mime === 'application/zip' ||
        mime === 'application/x-zip-compressed' ||
        mime === 'application/x-tar' ||
        mime === 'application/gzip' ||
        mime === 'application/x-gzip' ||
        mime === 'application/x-7z-compressed' ||
        mime === 'application/x-rar-compressed' ||
        mime === 'application/vnd.rar' ||
        mime === 'application/x-bzip2' ||
        mime === 'application/x-xz' ||
        mime === 'application/x-iso9660-image' ||
        mime === 'application/x-apple-diskimage' ||
        mime === 'application/zstd'
      ) {
        return 'archive';
      }
      if (
        mime === 'application/json' ||
        mime === 'application/ld+json' ||
        mime === 'application/xml' ||
        mime === 'text/xml' ||
        mime === 'application/javascript' ||
        mime === 'application/typescript' ||
        mime === 'text/javascript' ||
        mime === 'text/typescript' ||
        mime === 'text/html' ||
        mime === 'application/xhtml+xml' ||
        mime === 'text/css' ||
        mime === 'application/yaml' ||
        mime === 'application/x-yaml' ||
        mime === 'text/yaml' ||
        mime === 'application/sql' ||
        mime === 'application/x-sh' ||
        mime.startsWith('text/x-')
      ) {
        return 'code';
      }
    }
  }

  const lastSegment = name.split('/').pop() || '';
  if (!lastSegment.includes('.')) {
    return 'file';
  }

  const ext = lastSegment.split('.').pop()?.toLowerCase() || '';

  if (
    [
      'jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'ico', 'tiff', 'tif',
      'heic', 'heif', 'raw', 'cr2', 'nef', 'arw', 'dng', 'psd', 'ai', 'avif',
    ].includes(ext)
  ) {
    return 'image';
  }

  if (
    [
      'mp4', 'mkv', 'avi', 'mov', 'webm', 'm4v', 'flv', 'wmv', 'mpeg', 'mpg',
      '3gp', 'ogv', 'mts', 'm2ts', 'vob',
    ].includes(ext)
  ) {
    return 'video';
  }

  if (
    [
      'mp3', 'wav', 'flac', 'aac', 'ogg', 'm4a', 'wma', 'alac', 'opus',
      'aiff', 'mid', 'midi', 'mka',
    ].includes(ext)
  ) {
    return 'audio';
  }

  if (
    [
      'pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'ods', 'odp',
      'rtf', 'txt', 'csv', 'tsv', 'md', 'markdown', 'epub', 'log', 'tex',
    ].includes(ext)
  ) {
    return 'document';
  }

  if (
    [
      'js', 'ts', 'jsx', 'tsx', 'json', 'xml', 'html', 'htm', 'css', 'scss',
      'sass', 'less', 'py', 'go', 'rs', 'java', 'c', 'cpp', 'cc', 'cxx', 'h',
      'hpp', 'cs', 'sh', 'bash', 'zsh', 'yaml', 'yml', 'sql', 'env', 'ini',
      'conf', 'config', 'toml', 'php', 'rb', 'pl', 'swift', 'kt', 'kts',
      'dart', 'lua', 'r', 'scala', 'vue', 'svelte',
    ].includes(ext)
  ) {
    return 'code';
  }

  if (
    [
      'zip', 'tar', 'gz', '7z', 'rar', 'bz2', 'bz', 'xz', 'iso', 'dmg',
      'tgz', 'tbz2', 'txz', 'zst', 'apk', 'jar', 'war',
    ].includes(ext)
  ) {
    return 'archive';
  }

  return 'file';
}
