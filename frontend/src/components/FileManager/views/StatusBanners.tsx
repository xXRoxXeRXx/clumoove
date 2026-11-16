import { useTranslation } from 'react-i18next';

type StatusBannersProps = {
  /** Translated error to announce; nothing is shown while empty. */
  error: string;
  /** The requested folder was unavailable and the root was opened instead. */
  pathFallback: boolean;
};

/** Screen-level alert and notice lines above the browser. */
export function StatusBanners({ error, pathFallback }: StatusBannersProps) {
  const { t } = useTranslation();
  return (
    <>
      {error && <p className="ui-alert ui-alert-error px-3 py-2 text-sm" role="alert">{error}</p>}
      {pathFallback && <p className="ui-alert px-3 py-2 text-sm" role="status">{t('files.pathFallback')}</p>}
    </>
  );
}
