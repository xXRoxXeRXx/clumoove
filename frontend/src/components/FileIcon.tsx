import type { ReactElement } from 'react';
import {
  ArchiveBoxIcon,
  CodeBracketIcon,
  DocumentIcon,
  DocumentTextIcon,
  FilmIcon,
  FolderIcon,
  MusicalNoteIcon,
  PhotoIcon,
} from '@heroicons/react/24/outline';
import { getFileCategory } from '../utils/fileIcons';

export interface FileIconProps {
  name: string;
  mimeType?: string | null;
  isDir?: boolean;
  className?: string;
}

export function FileIcon({
  name,
  mimeType,
  isDir,
  className = 'h-5 w-5 shrink-0',
}: FileIconProps): ReactElement {
  const category = getFileCategory(name, mimeType, isDir);

  switch (category) {
    case 'folder':
      return <FolderIcon className={`${className} ui-file-folder`} aria-hidden="true" />;
    case 'image':
      return <PhotoIcon className={`${className} ui-file-image`} aria-hidden="true" />;
    case 'video':
      return <FilmIcon className={`${className} ui-file-video`} aria-hidden="true" />;
    case 'audio':
      return <MusicalNoteIcon className={`${className} ui-file-audio`} aria-hidden="true" />;
    case 'document':
      return <DocumentTextIcon className={`${className} ui-file-document`} aria-hidden="true" />;
    case 'code':
      return <CodeBracketIcon className={`${className} ui-file-folder`} aria-hidden="true" />;
    case 'archive':
      return <ArchiveBoxIcon className={`${className} ui-file-archive`} aria-hidden="true" />;
    default:
      return <DocumentIcon className={`${className} ui-file-default`} aria-hidden="true" />;
  }
}
