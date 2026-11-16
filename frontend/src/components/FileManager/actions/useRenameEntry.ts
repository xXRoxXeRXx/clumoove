import { useCallback, useState } from 'react';
import type { FileEntry, FileMutationConflictStrategy } from '../../../api/files';
import { useRequestSlot, type ActionEnvironment } from './actionEnvironment';
import type { RenameDialogModel } from './dialogModels';

/** Renaming one entry, including choosing a conflict strategy when the new name is taken. */
export function useRenameEntry(env: ActionEnvironment) {
  const { requests, request, t, translateMutationError, refreshListing } = env;
  const [target, setTarget] = useState<FileEntry | null>(null);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [conflictStrategies, setConflictStrategies] = useState<FileMutationConflictStrategy[] | null>(null);
  const { begin: beginRequest, abort: abortRequest } = useRequestSlot(request.profileId);

  const start = useCallback((entry: FileEntry) => {
    setTarget(entry);
    setName(entry.name);
    setError('');
    setConflictStrategies(null);
  }, []);

  const close = useCallback(() => {
    if (busy) return;
    abortRequest();
    setTarget(null);
    setError('');
    setConflictStrategies(null);
  }, [abortRequest, busy]);

  const submit = useCallback(
    async (strategy?: FileMutationConflictStrategy) => {
      if (!target || busy) return;
      const newName = name.trim();
      if (!newName) {
        setError(t('files.nameRequired'));
        return;
      }

      const controller = beginRequest();
      setBusy(true);
      setError('');

      const result = await requests.renameEntry(target.ref, newName, strategy, controller.signal);
      if (controller.signal.aborted) return;
      setBusy(false);
      if (result.ok === false) {
        const options = (result.data as { conflict_strategies?: FileMutationConflictStrategy[] } | undefined)
          ?.conflict_strategies;
        if (result.errorCode === 'FILES_CONFLICT' && options?.length) {
          setConflictStrategies(options);
        } else {
          setError(translateMutationError(result.errorCode));
        }
        if (result.errorCode === 'FILES_PARTIAL_OPERATION') {
          void refreshListing();
        }
        return;
      }

      setTarget(null);
      setConflictStrategies(null);
      void refreshListing();
    },
    [beginRequest, busy, name, refreshListing, requests, t, target, translateMutationError],
  );

  const dialog: RenameDialogModel | null = target
    ? {
        name,
        busy,
        error,
        setName,
        submit: () => submit(),
        close,
        conflict: conflictStrategies
          ? { strategies: conflictStrategies, busy, error, resolve: (strategy) => submit(strategy), cancel: close }
          : null,
      }
    : null;
  return { start, dialog };
}
