import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useApiError } from '../../../utils/apiError';
import { createFileRequests, type FileRequestContext, type FileRequests } from './fileRequests';

export type ActionEnvironmentOptions = {
  request: FileRequestContext;
  refreshListing: () => void | Promise<void>;
  clearSelection: () => void;
  clearPreview: () => void;
};

/**
 * What every action needs from its surroundings: bound requests, translation,
 * the screen's refresh/selection/preview callbacks, the shared error banner and
 * a way to notice that the profile session it started in has ended.
 */
export type ActionEnvironment = ActionEnvironmentOptions & {
  requests: FileRequests;
  t: TFunction;
  translateApiError: (errorCode?: string | null) => string;
  /** Like `translateApiError`, but explains a partially applied mutation. */
  translateMutationError: (errorCode?: string | null) => string;
  reportError: (message: string) => void;
  clearError: () => void;
  /**
   * Marks the start of an async step that cannot be aborted. The returned check turns
   * false once the profile changes or the module unmounts, so late results are dropped.
   */
  beginSession: () => () => boolean;
};

/**
 * Holds the one in-flight abortable request of an action. Starting a new request aborts
 * the previous one; a profile change or unmount aborts it too.
 */
export function useRequestSlot(profileId: string) {
  const requestRef = useRef<AbortController | null>(null);

  useEffect(() => {
    return () => {
      requestRef.current?.abort();
    };
  }, [profileId]);

  const begin = useCallback(() => {
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    return controller;
  }, []);
  const abort = useCallback(() => requestRef.current?.abort(), []);
  return { begin, abort };
}

export function useActionEnvironment(options: ActionEnvironmentOptions): { environment: ActionEnvironment; error: string } {
  const { request, refreshListing, clearSelection, clearPreview } = options;
  const { apiUrl, token, profileId } = request;
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const [error, setError] = useState('');
  const generationRef = useRef(0);

  useEffect(() => {
    return () => {
      generationRef.current += 1;
    };
  }, [profileId]);

  const stableRequest = useMemo(() => ({ apiUrl, token, profileId }), [apiUrl, token, profileId]);
  const requests = useMemo(() => createFileRequests(stableRequest), [stableRequest]);

  const translateMutationError = useCallback(
    (errorCode?: string | null) =>
      errorCode === 'FILES_PARTIAL_OPERATION' ? t('files.partialWarning') : translateApiError(errorCode),
    [t, translateApiError],
  );
  const clearError = useCallback(() => setError(''), []);
  const beginSession = useCallback(() => {
    const generation = generationRef.current;
    return () => generationRef.current === generation;
  }, []);

  const environment = useMemo<ActionEnvironment>(
    () => ({
      request: stableRequest,
      requests,
      refreshListing,
      clearSelection,
      clearPreview,
      t,
      translateApiError,
      translateMutationError,
      reportError: setError,
      clearError,
      beginSession,
    }),
    [
      beginSession,
      clearError,
      clearPreview,
      clearSelection,
      refreshListing,
      requests,
      stableRequest,
      t,
      translateApiError,
      translateMutationError,
    ],
  );

  return { environment, error };
}
