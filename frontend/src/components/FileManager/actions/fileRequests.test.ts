import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  batchDeleteFileEntries,
  batchMutateFileEntries,
  cancelCrossProfileFileTransfer,
  copyFileEntry,
  createArchiveTicket,
  createDirectory,
  createDownloadTicket,
  deleteFileEntry,
  listCrossProfileFileTransfers,
  listFileEntries,
  moveFileEntry,
  renameFileEntry,
  startCrossProfileFileTransfer,
} from '../../../api/files';
import { createFileRequests } from './fileRequests';

vi.mock('../../../api/files', () => ({
  createDownloadTicket: vi.fn(),
  createArchiveTicket: vi.fn(),
  createDirectory: vi.fn(),
  deleteFileEntry: vi.fn(),
  batchDeleteFileEntries: vi.fn(),
  renameFileEntry: vi.fn(),
  copyFileEntry: vi.fn(),
  moveFileEntry: vi.fn(),
  batchMutateFileEntries: vi.fn(),
  listFileEntries: vi.fn(),
  listCrossProfileFileTransfers: vi.fn(),
  cancelCrossProfileFileTransfer: vi.fn(),
  startCrossProfileFileTransfer: vi.fn(),
}));

describe('createFileRequests', () => {
  const requests = createFileRequests({ apiUrl: 'https://api.example.test', token: 'jwt', profileId: 'p1' });
  const signal = new AbortController().signal;

  beforeEach(() => vi.clearAllMocks());

  it('binds the request context to single-entry operations', () => {
    requests.createFolder('Docs', 'parent', signal);
    expect(createDirectory).toHaveBeenCalledWith('https://api.example.test', 'jwt', 'p1', 'Docs', 'parent', signal);

    requests.downloadTicket('ref-1', signal);
    expect(createDownloadTicket).toHaveBeenCalledWith('https://api.example.test', 'jwt', 'p1', 'ref-1', signal);

    requests.deleteEntry('ref-1', true, signal);
    expect(deleteFileEntry).toHaveBeenCalledWith('https://api.example.test', 'jwt', 'p1', 'ref-1', true, signal);

    requests.renameEntry('ref-1', 'new.txt', 'RENAME', signal);
    expect(renameFileEntry).toHaveBeenCalledWith('https://api.example.test', 'jwt', 'p1', 'ref-1', 'new.txt', 'RENAME', signal);

    requests.copyEntry('ref-1', 'dest', undefined, signal);
    expect(copyFileEntry).toHaveBeenCalledWith('https://api.example.test', 'jwt', 'p1', 'ref-1', 'dest', undefined, signal);

    requests.moveEntry('ref-1', null, 'SKIP', signal);
    expect(moveFileEntry).toHaveBeenCalledWith('https://api.example.test', 'jwt', 'p1', 'ref-1', null, 'SKIP', signal);
  });

  it('keeps the exact call arity of signal-less batch requests', () => {
    requests.archiveTicket(['a', 'b']);
    expect(vi.mocked(createArchiveTicket).mock.calls[0]).toEqual(['https://api.example.test', 'jwt', 'p1', ['a', 'b']]);

    requests.batchDelete([{ ref: 'a', recursive: true }]);
    expect(vi.mocked(batchDeleteFileEntries).mock.calls[0]).toEqual([
      'https://api.example.test', 'jwt', 'p1', [{ ref: 'a', recursive: true }],
    ]);

    requests.batchMutate('copy', ['a'], 'dest', 'SKIP', signal);
    expect(batchMutateFileEntries).toHaveBeenCalledWith('copy', 'https://api.example.test', 'jwt', 'p1', ['a'], 'dest', 'SKIP', signal);
  });

  it('lists directories of any profile but transfers of the bound profile', () => {
    requests.listDirectories('other', 'parent', signal);
    expect(listFileEntries).toHaveBeenCalledWith('https://api.example.test', 'jwt', 'other', 'parent', undefined, signal);

    requests.listTransfers(signal);
    expect(listCrossProfileFileTransfers).toHaveBeenCalledWith('https://api.example.test', 'jwt', 'p1', signal);

    requests.cancelTransfer('t1');
    expect(vi.mocked(cancelCrossProfileFileTransfer).mock.calls[0]).toEqual(['https://api.example.test', 'jwt', 't1']);
  });

  it('only passes a signal to cross-profile transfers when one is given', () => {
    const batch = { refs: ['a'], targetProfileId: 'p2', targetParentRef: null, operation: 'move' as const, strategy: undefined };
    requests.startCrossProfileTransfer(batch);
    expect(vi.mocked(startCrossProfileFileTransfer).mock.calls[0]).toEqual([
      'https://api.example.test', 'jwt', 'p1', ['a'], 'p2', null, 'move', undefined,
    ]);

    requests.startCrossProfileTransfer({ ...batch, signal });
    expect(vi.mocked(startCrossProfileFileTransfer).mock.calls[1]).toEqual([
      'https://api.example.test', 'jwt', 'p1', ['a'], 'p2', null, 'move', undefined, signal,
    ]);
  });
});
