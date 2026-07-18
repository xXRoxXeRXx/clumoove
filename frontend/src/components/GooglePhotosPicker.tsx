import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AlertCircle, Loader2 } from 'lucide-react';

interface GooglePhotosPickerProps {
  oauthToken: string;
  sessionId: string;
  developerKey?: string;
  onSelectionComplete: () => void;
  onError: (message: string) => void;
}

// GooglePhotosPicker renders the official Google Picker widget for the Google
// Photos Picker API. It is embedded (no separate browser popup): the Picker JS
// API boots inside a container div and, once the user confirms their selection,
// fires onSelectionComplete. The actual media items are enumerated server-side
// at index time from the session id, so this widget only has to capture the
// user's selection intent.
export const GooglePhotosPicker: React.FC<GooglePhotosPickerProps> = ({
  oauthToken,
  sessionId,
  developerKey,
  onSelectionComplete,
  onError,
}) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  useEffect(() => {
    let cancelled = false;

    // Minimal structural typings for the Google Picker JS API global, which
    // ships without TypeScript definitions. Only the members we use are typed.
    interface PickerResponse {
      ACTION: string;
      DOCUMENTS: string;
    }
    interface PickerApi {
      ViewId: { PHOTOS: string };
      Feature: { GOOGLE_PHOTOS_PICKER: string; MULTISELECT_ENABLED: string };
      Action: { PICKED: string; CANCEL: string };
      Response: PickerResponse;
      PickerBuilder: new () => PickerBuilder;
    }
    interface PickerBuilder {
      addView(view: string): PickerBuilder;
      enableFeature(feature: string): PickerBuilder;
      setOAuthToken(token: string): PickerBuilder;
      setSessionId(id: string): PickerBuilder;
      setDeveloperKey(key: string): PickerBuilder;
      setCallback(cb: (data: Record<string, unknown>) => void): PickerBuilder;
      build(): { setVisible(v: boolean): void };
    }
    interface GoogleNS {
      picker?: PickerApi;
    }

    const emitError = (msg: string) => {
      if (cancelled) return;
      setLoadError(msg);
      setLoading(false);
      onError(msg);
    };

    const buildPicker = () => {
      const g = (window as unknown as { google?: GoogleNS }).google;
      if (!g || !g.picker) {
        emitError(t('connect.googlePhotosPickerLoadFailed'));
        return;
      }
      const picker = g.picker;
      try {
        const builder = new picker.PickerBuilder()
          .addView(picker.ViewId.PHOTOS)
          .enableFeature(picker.Feature.GOOGLE_PHOTOS_PICKER)
          .setOAuthToken(oauthToken)
          .setSessionId(sessionId)
          .setCallback((data: Record<string, unknown>) => {
            if (data && data[picker.Response.ACTION] === picker.Action.PICKED) {
              setDone(true);
              setLoading(false);
              onSelectionComplete();
            } else if (data && data[picker.Response.ACTION] === picker.Action.CANCEL) {
              // Cancelling is allowed; the user can re-open the picker.
              setLoading(false);
            }
          });
        if (developerKey) {
          builder.setDeveloperKey(developerKey);
        }
        const built = builder.build();
        built.setVisible(true);
        if (!cancelled) setLoading(false);
      } catch {
        emitError(t('connect.googlePhotosPickerLoadFailed'));
      }
    };

    const loadScript = () => {
      const existing = document.getElementById('google-picker-script');
      const g = (window as unknown as { google?: GoogleNS }).google;
      if (g && g.picker) {
        buildPicker();
        return;
      }
      if (existing) {
        (existing as HTMLScriptElement).onload = buildPicker;
        return;
      }
      const script = document.createElement('script');
      script.id = 'google-picker-script';
      script.src = 'https://www.gstatic.com/picker/js/loader.js';
      script.async = true;
      script.onload = buildPicker;
      script.onerror = () => emitError(t('connect.googlePhotosPickerLoadFailed'));
      document.body.appendChild(script);
    };

    loadScript();

    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, oauthToken, developerKey]);

  return (
    <div className="space-y-2">
      <div
        className="min-h-[320px] bg-[var(--color-bg-tertiary)]/40 border border-[var(--color-border)] rounded-2xl p-3 flex items-center justify-center"
      >
        {loading && !loadError && (
          <div className="flex flex-col items-center gap-2 text-[var(--color-text-muted)]">
            <Loader2 className="w-6 h-6 animate-spin" />
            <span className="text-xs font-sans">{t('connect.googlePhotosPickerLoading')}</span>
          </div>
        )}
        {loadError && (
          <div className="flex items-start gap-2 text-rose-700 max-w-sm text-center">
            <AlertCircle className="w-5 h-5 shrink-0 mt-0.5" />
            <span className="text-xs font-sans leading-relaxed">{loadError}</span>
          </div>
        )}
        {done && !loadError && (
          <div className="flex items-center gap-2 text-emerald-700">
            <span className="text-xs font-bold font-sans">{t('connect.googlePhotosPickerDone')}</span>
          </div>
        )}
      </div>
      {done && (
        <p className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">
          {t('connect.googlePhotosPickerConfirmed')}
        </p>
      )}
    </div>
  );
};
