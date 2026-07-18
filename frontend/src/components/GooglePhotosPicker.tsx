import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AlertCircle, CheckCircle2, ExternalLink, Loader2 } from 'lucide-react';

interface GooglePhotosPickerProps {
  apiUrl: string;
  token: string;
  oauthToken: string;
  refreshToken?: string;
  sessionId: string;
  pickerUri: string;
  pollInterval?: string;
  timeoutIn?: string;
  onSelectionComplete: () => void;
  onError: (message: string) => void;
}

// DEFAULT_POLL_TIMEOUT_MS guards the poll loop so an open picker tab cannot
// poll forever and exhaust the shared connect rate-limit bucket.
const DEFAULT_POLL_TIMEOUT_MS = 30 * 60 * 1000;

// parseDurationMs converts a Google duration string like "5s" or "1800s" into
// milliseconds. Falls back to the provided default on any parse issue.
const parseDurationMs = (value: string | undefined, fallbackMs: number): number => {
  if (!value) return fallbackMs;
  const match = /^([\d.]+)s$/.exec(value.trim());
  if (!match) return fallbackMs;
  const seconds = Number(match[1]);
  if (!Number.isFinite(seconds) || seconds <= 0) return fallbackMs;
  return Math.round(seconds * 1000);
};

// withAutoclose appends /autoclose to the pickerUri exactly once so Google
// closes the tab after the user finishes. Centralised to avoid the open button
// and the manual reopen link drifting apart.
const withAutoclose = (uri: string): string =>
  uri.includes('/autoclose') ? uri : `${uri}/autoclose`;

// PollResult is the parsed backend response from POST /api/googlephotos/picker/poll.
interface PollResult {
  success?: boolean;
  media_items_set?: boolean;
  poll_interval?: string;
  timeout_in?: string;
}

// runGooglePhotosPoll drives the poll loop until the selection is confirmed,
// the deadline passes, or polling is stopped. It lives at module scope (not in
// the component body) so that the Date.now() calls it makes are not flagged by
// the React purity rule. Callbacks receive state-transition intents.
const runGooglePhotosPoll = async (
  params: {
    apiUrl: string;
    token: string;
    oauthToken: string;
    refreshToken: string;
    sessionId: string;
    pollInterval?: string;
    deadlineRef: { current: number };
    stoppedRef: { current: boolean };
    scheduleNext: (fn: () => void, ms: number) => void;
  },
  callbacks: {
    onDone: () => void;
    onFail: (msg: string) => void;
  },
  initialDelayMs: number,
  loadFailedMsg: string,
): Promise<void> => {
  let nextDelay = initialDelayMs;
  const tick = async () => {
    if (params.stoppedRef.current) return;
    try {
      const response = await fetch(`${params.apiUrl}/api/googlephotos/picker/poll`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${params.token}`,
        },
        body: JSON.stringify({
          provider: 'googlephotos',
          access_token: params.oauthToken,
          refresh_token: params.refreshToken,
          session_id: params.sessionId,
        }),
      });
      const data = (await response.json().catch(() => ({}))) as PollResult;
      if (params.stoppedRef.current) return;
      if (!data.success) {
        callbacks.onFail(loadFailedMsg);
        return;
      }
      if (data.media_items_set) {
        callbacks.onDone();
        return;
      }
      // Honour a refreshed server timeout if provided.
      if (data.timeout_in) {
        params.deadlineRef.current = Date.now() + parseDurationMs(data.timeout_in, DEFAULT_POLL_TIMEOUT_MS);
      }
      // Stop once the deadline passes: the user must reopen the picker.
      if (Date.now() >= params.deadlineRef.current) {
        callbacks.onFail(loadFailedMsg);
        return;
      }
      nextDelay = Math.max(parseDurationMs(data.poll_interval || params.pollInterval, 5000), 2000);
      params.scheduleNext(tick, nextDelay);
    } catch {
      if (params.stoppedRef.current) return;
      // Transient network error: retry rather than hard-failing, but still
      // respect the deadline.
      if (Date.now() >= params.deadlineRef.current) {
        callbacks.onFail(loadFailedMsg);
        return;
      }
      params.scheduleNext(tick, 5000);
    }
  };
  params.scheduleNext(tick, initialDelayMs);
};

// GooglePhotosPicker drives the Google Photos Picker API flow. Unlike the old
// Drive Picker, the Photos Picker has no embeddable JS widget: the user must
// open the pickerUri in a new tab, pick media in Google Photos, and return.
// This component opens that tab and then polls the backend
// (POST /api/googlephotos/picker/poll) until mediaItemsSet becomes true, at
// which point onSelectionComplete fires.
export const GooglePhotosPicker: React.FC<GooglePhotosPickerProps> = ({
  apiUrl,
  token,
  oauthToken,
  refreshToken,
  sessionId,
  pickerUri,
  pollInterval,
  timeoutIn,
  onSelectionComplete,
  onError,
}) => {
  const { t } = useTranslation();
  const [status, setStatus] = useState<'idle' | 'waiting' | 'done' | 'error'>('idle');
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const pollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const stoppedRef = useRef(false);
  // Absolute wall-clock deadline derived from the server's timeout_in. Set in
  // the effect/openPicker; read by the module-level poller.
  const deadlineRef = useRef<number>(0);

  useEffect(() => {
    // Cleanup on unmount. State reset on session change is handled by the
    // parent remounting this component via a changing `key` (sessionId), so we
    // avoid calling setState synchronously inside the effect.
    return () => {
      stoppedRef.current = true;
      if (pollTimer.current) clearTimeout(pollTimer.current);
    };
  }, []);

  const stop = () => {
    stoppedRef.current = true;
    if (pollTimer.current) {
      clearTimeout(pollTimer.current);
      pollTimer.current = null;
    }
  };

  const fail = (msg: string) => {
    stop();
    setStatus('error');
    setErrorMsg(msg);
    onError(msg);
  };

  const startPolling = () => {
    const initialDelay = Math.max(parseDurationMs(pollInterval, 3000), 2000);
    deadlineRef.current = Date.now() + parseDurationMs(timeoutIn, DEFAULT_POLL_TIMEOUT_MS);
    runGooglePhotosPoll(
      {
        apiUrl,
        token,
        oauthToken,
        refreshToken: refreshToken || '',
        sessionId,
        pollInterval,
        deadlineRef,
        stoppedRef,
        scheduleNext: (fn, ms) => {
          if (pollTimer.current) clearTimeout(pollTimer.current);
          pollTimer.current = setTimeout(fn, ms);
        },
      },
      {
        onDone: () => {
          stop();
          setStatus('done');
          onSelectionComplete();
        },
        onFail: (msg) => fail(msg),
      },
      initialDelay,
      t('connect.googlePhotosPickerLoadFailed'),
    );
  };

  const openPicker = () => {
    setErrorMsg(null);
    setStatus('waiting');
    stoppedRef.current = false;
    deadlineRef.current = Date.now() + parseDurationMs(timeoutIn, DEFAULT_POLL_TIMEOUT_MS);
    // Append /autoclose so Google closes the tab after the user finishes.
    const uri = withAutoclose(pickerUri);
    // noopener implies the returned reference is null in modern browsers, so we
    // do not rely on it for popup-blocker detection — the manual reopen link
    // below covers that case.
    window.open(uri, '_blank', 'noopener,noreferrer');
    // Kick off polling shortly after opening.
    startPolling();
  };

  return (
    <div className="space-y-3">
      <div className="min-h-[120px] bg-[var(--color-bg-tertiary)]/40 border border-[var(--color-border)] rounded-2xl p-4 flex flex-col items-center justify-center gap-3">
        {status === 'idle' && (
          <>
            <p className="text-xs font-sans text-center text-[var(--color-text-muted)] leading-relaxed max-w-sm">
              {t('connect.googlePhotosPickerOpenHint')}
            </p>
            <button
              type="button"
              onClick={openPicker}
              className="py-2.5 px-4 bg-portal-navy hover:bg-portal-navy-light text-white font-mono font-bold text-[11px] uppercase tracking-wider rounded-xl shadow-xs hover:shadow-sm transition-all cursor-pointer flex items-center justify-center gap-2"
            >
              <ExternalLink className="w-4 h-4" /> {t('connect.googlePhotosPickerOpen')}
            </button>
          </>
        )}

        {status === 'waiting' && (
          <div className="flex flex-col items-center gap-2 text-[var(--color-text-muted)]">
            <Loader2 className="w-6 h-6 animate-spin" />
            <span className="text-xs font-sans text-center">{t('connect.googlePhotosPickerWaiting')}</span>
            <a
              href={withAutoclose(pickerUri)}
              target="_blank"
              rel="noopener noreferrer"
              className="text-[11px] font-mono underline text-portal-navy hover:text-portal-navy-light inline-flex items-center gap-1"
            >
              <ExternalLink className="w-3.5 h-3.5" /> {t('connect.googlePhotosPickerReopen')}
            </a>
          </div>
        )}

        {status === 'done' && (
          <div className="flex items-center gap-2 text-emerald-700">
            <CheckCircle2 className="w-5 h-5" />
            <span className="text-xs font-bold font-sans">{t('connect.googlePhotosPickerDone')}</span>
          </div>
        )}

        {status === 'error' && errorMsg && (
          <div className="flex items-start gap-2 text-rose-700 max-w-sm text-center">
            <AlertCircle className="w-5 h-5 shrink-0 mt-0.5" />
            <span className="text-xs font-sans leading-relaxed">{errorMsg}</span>
          </div>
        )}
      </div>
      {status === 'done' && (
        <p className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">
          {t('connect.googlePhotosPickerConfirmed')}
        </p>
      )}
    </div>
  );
};
