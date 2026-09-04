import { apiJson, type ApiJsonFailure, type ApiJsonResult } from '../utils/apiClient';

export type FileEntry = {
  ref: string;
  parent_ref?: string;
  name: string;
  display_path: string;
  kind: 'file' | 'directory';
  size: number;
  modified_at?: string | null;
  mime_type?: string;
  allowed_actions: string[];
};

export type FileCapabilities = {
  browse: boolean;
  native_pagination: boolean;
  download: boolean;
  upload: boolean;
  mkdir: boolean;
  rename: boolean;
  move: boolean;
  delete_file: boolean;
  delete_empty_directory: boolean;
  delete_recursive_directory: boolean;
  conflict_skip: boolean;
  conflict_overwrite: boolean;
  conflict_overwrite_atomic: boolean;
  conflict_rename: boolean;
  native_copy: boolean;
  range_download: boolean;
  thumbnails: boolean;
};

export type FileEntriesResponse = {
  entries: FileEntry[];
  next_cursor?: string | null;
};

export type FileBreadcrumb = {
  ref: string;
  name: string;
};

type FileCapabilitiesResponse = {
  capabilities: FileCapabilities;
};

export type DownloadTicketResponse = {
  download_url: string;
};

export type ResolveFilePathResponse = {
  ref: string;
  breadcrumbs: FileBreadcrumb[];
  fallback: boolean;
};

export type UploadConflictStrategy = 'SKIP' | 'OVERWRITE' | 'RENAME';

export type FileMutationConflictStrategy = 'SKIP' | 'OVERWRITE' | 'RENAME';

export type FileMutationResponse = {
  success: boolean;
  status: string;
  name: string;
  native: boolean;
};

export type FileMutationFailure = ApiJsonFailure<FileMutationResponse> & {
  data?: { conflict_strategies?: FileMutationConflictStrategy[] };
};

export type FileMutationResult = ApiJsonResult<FileMutationResponse>;

export type UploadFileResponse = {
  status: 'uploaded' | 'skipped' | 'renamed';
  name: string;
};

export type UploadProgress = {
  loaded: number;
  total: number;
};

function profileUrl(apiUrl: string, profileId: string, suffix: string): string {
  return `${apiUrl}/api/files/profiles/${encodeURIComponent(profileId)}${suffix}`;
}

function requestInit(token: string, signal?: AbortSignal): RequestInit {
  return {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
    signal,
  };
}

export async function getFileCapabilities(
  apiUrl: string,
  token: string,
  profileId: string,
  signal?: AbortSignal,
): Promise<ApiJsonResult<FileCapabilitiesResponse>> {
  return apiJson<FileCapabilitiesResponse>(profileUrl(apiUrl, profileId, '/capabilities'), {
    headers: { Authorization: `Bearer ${token}` },
    signal,
  });
}

export async function listFileEntries(
  apiUrl: string,
  token: string,
  profileId: string,
  parentRef: string | null,
  cursor?: string,
  signal?: AbortSignal,
): Promise<ApiJsonResult<FileEntriesResponse>> {
  const body: { resource_type: 'files'; parent_ref?: string; cursor?: string } = { resource_type: 'files' };
  if (parentRef) body.parent_ref = parentRef;
  if (cursor) body.cursor = cursor;
  const init = requestInit(token, signal);
  init.body = JSON.stringify(body);
  return apiJson<FileEntriesResponse>(profileUrl(apiUrl, profileId, '/entries:list'), init);
}

export async function createDownloadTicket(
  apiUrl: string,
  token: string,
  profileId: string,
  ref: string,
  signal?: AbortSignal,
): Promise<ApiJsonResult<DownloadTicketResponse>> {
  const init = requestInit(token, signal);
  init.body = JSON.stringify({ ref });
  return apiJson<DownloadTicketResponse>(profileUrl(apiUrl, profileId, '/download-tickets'), init);
}

export async function resolveFilePath(
  apiUrl: string,
  token: string,
  profileId: string,
  path: string,
  signal?: AbortSignal,
): Promise<ApiJsonResult<ResolveFilePathResponse>> {
  const init = requestInit(token, signal);
  init.body = JSON.stringify({ resource_type: 'files', path });
  return apiJson<ResolveFilePathResponse>(profileUrl(apiUrl, profileId, '/entries:resolve'), init);
}

export type CreateDirectoryResponse = {
  success: boolean;
  name: string;
};

export async function createDirectory(
  apiUrl: string,
  token: string,
  profileId: string,
  name: string,
  parentRef: string | null,
  signal?: AbortSignal,
): Promise<ApiJsonResult<CreateDirectoryResponse>> {
  const body: { name: string; parent_ref?: string } = { name };
  if (parentRef) body.parent_ref = parentRef;
  const init = requestInit(token, signal);
  init.body = JSON.stringify(body);
  return apiJson<CreateDirectoryResponse>(profileUrl(apiUrl, profileId, '/directories'), init);
}

export async function deleteFileEntry(
  apiUrl: string,
  token: string,
  profileId: string,
  ref: string,
  recursive: boolean,
  signal?: AbortSignal,
): Promise<ApiJsonResult<Record<string, never>>> {
  const init = requestInit(token, signal);
  init.method = 'DELETE';
  init.body = JSON.stringify({ ref, recursive });
  return apiJson<Record<string, never>>(profileUrl(apiUrl, profileId, '/entries'), init);
}

type FileMutationRequest = {
  ref: string;
  destination_parent_ref?: string;
  new_name?: string;
  conflict_strategy?: FileMutationConflictStrategy;
};

async function mutateFileEntry(
  operation: 'rename' | 'copy' | 'move',
  apiUrl: string,
  token: string,
  profileId: string,
  body: FileMutationRequest,
  signal?: AbortSignal,
): Promise<FileMutationResult> {
  const init = requestInit(token, signal);
  init.body = JSON.stringify(body);
  return apiJson<FileMutationResponse>(profileUrl(apiUrl, profileId, `/entries:${operation}`), init);
}

export function renameFileEntry(
  apiUrl: string,
  token: string,
  profileId: string,
  ref: string,
  newName: string,
  conflictStrategy?: FileMutationConflictStrategy,
  signal?: AbortSignal,
): Promise<FileMutationResult> {
  return mutateFileEntry('rename', apiUrl, token, profileId, {
    ref,
    new_name: newName,
    ...(conflictStrategy ? { conflict_strategy: conflictStrategy } : {}),
  }, signal);
}

export function copyFileEntry(
  apiUrl: string,
  token: string,
  profileId: string,
  ref: string,
  destinationParentRef: string | null,
  conflictStrategy?: FileMutationConflictStrategy,
  signal?: AbortSignal,
): Promise<FileMutationResult> {
  return mutateFileEntry('copy', apiUrl, token, profileId, {
    ref,
    ...(destinationParentRef ? { destination_parent_ref: destinationParentRef } : {}),
    ...(conflictStrategy ? { conflict_strategy: conflictStrategy } : {}),
  }, signal);
}

export function moveFileEntry(
  apiUrl: string,
  token: string,
  profileId: string,
  ref: string,
  destinationParentRef: string | null,
  conflictStrategy?: FileMutationConflictStrategy,
  signal?: AbortSignal,
): Promise<FileMutationResult> {
  return mutateFileEntry('move', apiUrl, token, profileId, {
    ref,
    ...(destinationParentRef ? { destination_parent_ref: destinationParentRef } : {}),
    ...(conflictStrategy ? { conflict_strategy: conflictStrategy } : {}),
  }, signal);
}

function encodeHeaderFileName(name: string): string {
  const bytes = new TextEncoder().encode(name);
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
}

function uploadError(xhr: XMLHttpRequest): ApiJsonFailure<UploadFileResponse> {
  let errorCode = 'UNKNOWN';
  try {
    const body: unknown = JSON.parse(xhr.responseText);
    if (typeof body === 'object' && body !== null && 'error_code' in body && typeof body.error_code === 'string') {
      errorCode = body.error_code;
    }
  } catch {
    // Upload responses are deliberately exposed only as machine-readable codes.
  }
  return { ok: false, status: xhr.status, errorCode, networkError: xhr.status === 0 };
}

export function uploadFile(
  apiUrl: string,
  token: string,
  profileId: string,
  file: File,
  parentRef: string | null,
  strategy: UploadConflictStrategy,
  onProgress: (progress: UploadProgress) => void,
  signal?: AbortSignal,
): Promise<ApiJsonResult<UploadFileResponse>> {
  return new Promise((resolve) => {
    const xhr = new XMLHttpRequest();
    const cleanup = () => signal?.removeEventListener('abort', abort);
    const abort = () => xhr.abort();
    signal?.addEventListener('abort', abort, { once: true });
    xhr.open('PUT', profileUrl(apiUrl, profileId, '/content'));
    xhr.setRequestHeader('Authorization', `Bearer ${token}`);
    xhr.setRequestHeader('X-Clumoove-File-Name', encodeHeaderFileName(file.name));
    xhr.setRequestHeader('X-Clumoove-Conflict-Strategy', strategy);
    if (parentRef) xhr.setRequestHeader('X-Clumoove-Parent-Ref', parentRef);
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) onProgress({ loaded: event.loaded, total: event.total });
    };
    xhr.onerror = () => {
      cleanup();
      resolve({ ok: false, status: 0, errorCode: 'UNKNOWN', networkError: true });
    };
    xhr.onabort = () => {
      cleanup();
      resolve({ ok: false, status: 0, errorCode: 'UNKNOWN', networkError: true });
    };
    xhr.onload = () => {
      cleanup();
      if (xhr.status < 200 || xhr.status >= 300) {
        resolve(uploadError(xhr));
        return;
      }
      try {
        const body = JSON.parse(xhr.responseText) as UploadFileResponse;
        if ((body.status !== 'uploaded' && body.status !== 'skipped' && body.status !== 'renamed') || typeof body.name !== 'string') {
          resolve({ ok: false, status: xhr.status, errorCode: 'UNKNOWN', networkError: false });
          return;
        }
        resolve({ ok: true, status: xhr.status, data: body });
      } catch {
        resolve({ ok: false, status: xhr.status, errorCode: 'UNKNOWN', networkError: false });
      }
    };
    xhr.send(file);
  });
}

export type ThumbnailFetchResult = {
  blob: Blob | null;
  status: number;
};

export async function getFileThumbnailResult(
  apiUrl: string,
  token: string,
  profileId: string,
  ref: string,
  width?: number,
  height?: number,
  signal?: AbortSignal,
): Promise<ThumbnailFetchResult> {
  const body: { ref: string; width?: number; height?: number } = { ref };
  if (width) body.width = width;
  if (height) body.height = height;

  try {
    const response = await fetch(profileUrl(apiUrl, profileId, '/thumbnail'), {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${token}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(body),
      signal,
    });
    if (!response.ok) {
      return { blob: null, status: response.status };
    }
    const blob = await response.blob();
    return { blob, status: response.status };
  } catch {
    return { blob: null, status: 0 };
  }
}

export async function getFileThumbnail(
  apiUrl: string,
  token: string,
  profileId: string,
  ref: string,
  width?: number,
  height?: number,
  signal?: AbortSignal,
): Promise<Blob | null> {
  const result = await getFileThumbnailResult(apiUrl, token, profileId, ref, width, height, signal);
  return result.blob;
}

