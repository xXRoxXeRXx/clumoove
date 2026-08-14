import { describe, expect, it, vi } from 'vitest';
import { apiJson } from '../utils/apiClient';
import { createDownloadTicket, listFileEntries, resolveFilePath } from './files';

vi.mock('../utils/apiClient', () => ({ apiJson: vi.fn() }));

describe('file API', () => {
  it('sends directory refs and cursors in the entries-list body', async () => {
    await listFileEntries('https://api.example.test', 'token', 'profile id', 'opaque-directory-ref', 'next-page');

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile%20id/entries:list',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ resource_type: 'files', parent_ref: 'opaque-directory-ref', cursor: 'next-page' }),
      }),
    );
  });

  it('uses the entry ref only in the download-ticket body', async () => {
    await createDownloadTicket('https://api.example.test', 'token', 'profile-id', 'opaque-file-ref');

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile-id/download-tickets',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ ref: 'opaque-file-ref' }) }),
    );
  });

  it('sends quick-link paths only in the resolve request body', async () => {
    await resolveFilePath('https://api.example.test', 'token', 'profile-id', '/documents/reports');

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/files/profiles/profile-id/entries:resolve',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ resource_type: 'files', path: '/documents/reports' }) }),
    );
  });
});
