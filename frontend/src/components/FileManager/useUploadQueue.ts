import { useCallback, useEffect, useRef, useState } from 'react';
import { uploadFile, type UploadConflictStrategy } from '../../api/files';
import { useApiError } from '../../utils/apiError';
import { isUploadTaskCompleted, type UploadTask } from './transferItems';

const MAX_CONCURRENT_UPLOADS = 4;
const RATE_LIMIT_BACKOFF_MS = 1500;

export type UploadTarget = {
  profileId: string;
  parentRef: string | null;
  strategy: UploadConflictStrategy;
};

export type UploadQueue = {
  tasks: UploadTask[];
  enqueue: (files: File[], target: UploadTarget) => void;
  cancel: (taskId: string) => void;
  retry: (taskId: string) => void;
  dismiss: (taskId: string) => void;
};

export type UseUploadQueueOptions = {
  apiUrl: string;
  token: string;
  /** Called with the target profile after each upload that reached the server, so listings can refresh. */
  onCompleted: (profileId: string) => void;
};

function nextTaskID(): string {
  return crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
}

/**
 * Browser upload queue. Owns concurrency (four parallel uploads), rate-limit backoff, abort
 * handling and retry; callers only enqueue files and act on task ids.
 */
export function useUploadQueue({ apiUrl, token, onCompleted }: UseUploadQueueOptions): UploadQueue {
  const translateApiError = useApiError();
  const controllers = useRef(new Map<string, AbortController>());
  const running = useRef(new Set<string>());
  const backoffTimeoutRef = useRef<number | null>(null);
  const backoffUntilRef = useRef(0);
  const [tasks, setTasks] = useState<UploadTask[]>([]);

  useEffect(() => {
    const activeControllers = controllers.current;
    return () => {
      activeControllers.forEach((controller) => controller.abort());
      if (backoffTimeoutRef.current !== null) {
        window.clearTimeout(backoffTimeoutRef.current);
      }
    };
  }, []);

  useEffect(() => {
    if (backoffUntilRef.current > Date.now()) {
      const remaining = backoffUntilRef.current - Date.now();
      if (backoffTimeoutRef.current === null) {
        backoffTimeoutRef.current = window.setTimeout(() => {
          backoffTimeoutRef.current = null;
          setTasks((current) => [...current]);
        }, remaining);
      }
      return;
    }

    const slots = MAX_CONCURRENT_UPLOADS - running.current.size;
    if (slots <= 0) return;
    const queued = tasks.filter((task) => task.status === 'queued' && !running.current.has(task.id)).slice(0, slots);
    for (const task of queued) {
      running.current.add(task.id);
      const controller = new AbortController();
      controllers.current.set(task.id, controller);
      setTasks((current) => current.map((item) => item.id === task.id ? { ...item, status: 'uploading' } : item));
      void uploadFile(apiUrl, token, task.profileId, task.file, task.parentRef, task.strategy, ({ loaded }) => {
        setTasks((current) => current.map((item) => item.id === task.id ? { ...item, loaded } : item));
      }, controller.signal).then((result) => {
        if (controller.signal.aborted) {
          setTasks((current) => current.map((item) => item.id === task.id ? { ...item, status: 'cancelled' } : item));
          return;
        }
        if (result.ok === false) {
          if (result.errorCode === 'RATE_LIMITED' || result.errorCode === 'FILES_STREAM_LIMIT_REACHED') {
            backoffUntilRef.current = Date.now() + RATE_LIMIT_BACKOFF_MS;
          }
          setTasks((current) => current.map((item) => item.id === task.id ? { ...item, status: 'failed', error: translateApiError(result.errorCode) } : item));
          return;
        }
        setTasks((current) => current.map((item) => item.id === task.id ? { ...item, status: result.data.status, loaded: item.file.size } : item));
        onCompleted(task.profileId);
      }).finally(() => {
        running.current.delete(task.id);
        controllers.current.delete(task.id);
        setTasks((current) => [...current]);
      });
    }
  }, [apiUrl, onCompleted, tasks, token, translateApiError]);

  const enqueue = useCallback((files: File[], target: UploadTarget) => {
    setTasks((current) => [
      ...current,
      ...files.map((file) => ({
        id: nextTaskID(),
        file,
        profileId: target.profileId,
        parentRef: target.parentRef,
        strategy: target.strategy,
        status: 'queued' as const,
        loaded: 0,
      })),
    ]);
  }, []);

  const cancel = useCallback((taskId: string) => {
    controllers.current.get(taskId)?.abort();
    setTasks((current) => current.map((task) => task.id === taskId && !isUploadTaskCompleted(task.status) ? { ...task, status: 'cancelled' } : task));
  }, []);

  const retry = useCallback((taskId: string) => {
    setTasks((current) => current.map((task) => task.id === taskId ? { ...task, status: 'queued', loaded: 0, error: undefined } : task));
  }, []);

  const dismiss = useCallback((taskId: string) => {
    setTasks((current) => current.filter((task) => task.id !== taskId));
  }, []);

  return { tasks, enqueue, cancel, retry, dismiss };
}
