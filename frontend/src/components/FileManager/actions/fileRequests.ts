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
  type FileMutationConflictStrategy,
} from '../../../api/files';

/** Where file requests go and who they act as. Bundled once instead of threaded through every call. */
export type FileRequestContext = {
  apiUrl: string;
  token: string;
  profileId: string;
};

export type CrossProfileTransferRequest = {
  refs: string[];
  targetProfileId: string;
  targetParentRef: string | null;
  operation: 'copy' | 'move';
  strategy?: FileMutationConflictStrategy;
  signal?: AbortSignal;
};

/**
 * The file API bound to one request context. Each method forwards to `api/files`
 * with exactly the arguments the endpoint expects; signal-less batch requests
 * deliberately keep their original arity.
 */
export function createFileRequests({ apiUrl, token, profileId }: FileRequestContext) {
  return {
    createFolder: (name: string, parentRef: string | null, signal?: AbortSignal) =>
      createDirectory(apiUrl, token, profileId, name, parentRef, signal),
    downloadTicket: (ref: string, signal?: AbortSignal) =>
      createDownloadTicket(apiUrl, token, profileId, ref, signal),
    archiveTicket: (refs: string[]) => createArchiveTicket(apiUrl, token, profileId, refs),
    deleteEntry: (ref: string, recursive: boolean, signal?: AbortSignal) =>
      deleteFileEntry(apiUrl, token, profileId, ref, recursive, signal),
    batchDelete: (items: Array<{ ref: string; recursive: boolean }>) =>
      batchDeleteFileEntries(apiUrl, token, profileId, items),
    renameEntry: (ref: string, newName: string, strategy?: FileMutationConflictStrategy, signal?: AbortSignal) =>
      renameFileEntry(apiUrl, token, profileId, ref, newName, strategy, signal),
    copyEntry: (ref: string, destinationRef: string | null, strategy?: FileMutationConflictStrategy, signal?: AbortSignal) =>
      copyFileEntry(apiUrl, token, profileId, ref, destinationRef, strategy, signal),
    moveEntry: (ref: string, destinationRef: string | null, strategy?: FileMutationConflictStrategy, signal?: AbortSignal) =>
      moveFileEntry(apiUrl, token, profileId, ref, destinationRef, strategy, signal),
    batchMutate: (
      operation: 'copy' | 'move',
      refs: string[],
      destinationRef: string | null,
      strategy?: FileMutationConflictStrategy,
      signal?: AbortSignal,
    ) => batchMutateFileEntries(operation, apiUrl, token, profileId, refs, destinationRef, strategy, signal),
    /** Lists any profile, not only the bound one: the destination picker browses other profiles. */
    listDirectories: (targetProfileId: string, parentRef: string | null, signal?: AbortSignal) =>
      listFileEntries(apiUrl, token, targetProfileId, parentRef, undefined, signal),
    listTransfers: (signal?: AbortSignal) => listCrossProfileFileTransfers(apiUrl, token, profileId, signal),
    cancelTransfer: (transferId: string) => cancelCrossProfileFileTransfer(apiUrl, token, transferId),
    startCrossProfileTransfer: ({ refs, targetProfileId, targetParentRef, operation, strategy, signal }: CrossProfileTransferRequest) => {
      // Only pass a signal when there is one, so the request arity stays unchanged for batch transfers.
      const signalArgs: [AbortSignal?] = signal ? [signal] : [];
      return startCrossProfileFileTransfer(
        apiUrl, token, profileId, refs, targetProfileId, targetParentRef, operation, strategy, ...signalArgs,
      );
    },
  };
}

export type FileRequests = ReturnType<typeof createFileRequests>;
