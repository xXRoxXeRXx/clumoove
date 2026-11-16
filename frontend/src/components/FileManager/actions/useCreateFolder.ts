import { useCallback, useState } from 'react';
import { useRequestSlot, type ActionEnvironment } from './actionEnvironment';
import type { CreateFolderDialogModel } from './dialogModels';

type Options = {
  enabled: boolean;
  currentRef: string | null;
};

/** Creating a folder in the current directory. */
export function useCreateFolder(env: ActionEnvironment, { enabled, currentRef }: Options) {
  const { requests, request, translateApiError, refreshListing } = env;
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const { begin: beginRequest, abort: abortRequest } = useRequestSlot(request.profileId);

  const start = useCallback(() => {
    setOpen(true);
    setName('');
    setError('');
  }, []);

  const close = useCallback(() => {
    if (busy) return;
    abortRequest();
    setOpen(false);
    setName('');
    setError('');
  }, [abortRequest, busy]);

  const submit = useCallback(async () => {
    const trimmed = name.trim();
    if (!trimmed || busy || !enabled) return;
    const controller = beginRequest();
    setBusy(true);
    setError('');

    const result = await requests.createFolder(trimmed, currentRef, controller.signal);
    if (controller.signal.aborted) return;
    setBusy(false);
    if (result.ok === false) {
      setError(translateApiError(result.errorCode));
      return;
    }

    setOpen(false);
    setName('');
    void refreshListing();
  }, [beginRequest, busy, currentRef, enabled, name, refreshListing, requests, translateApiError]);

  const dialog: CreateFolderDialogModel | null = open ? { name, busy, error, setName, submit, close } : null;
  return { start, dialog };
}
