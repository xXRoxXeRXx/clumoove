import { apiJson, type ApiJsonResult } from '../utils/apiClient';

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
