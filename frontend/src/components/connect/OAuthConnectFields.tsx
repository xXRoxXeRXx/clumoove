import { useTranslation } from 'react-i18next';
import { ArrowPathIcon as RefreshCw } from '../icons';

export interface OAuthConnectFieldsProps {
  provider: string;
  oauthUser: string;
  connected: boolean;
  onConnect: () => void;
  onDisconnect: () => void;
}

/** Renders the OAuth connect button or connected-account card for a provider. */
export function OAuthConnectFields({
  provider,
  oauthUser,
  connected,
  onConnect,
  onDisconnect,
}: OAuthConnectFieldsProps) {
  const { t } = useTranslation();

  const providerName = t(`connect.providerName.${provider}`, { defaultValue: provider });
  const connectLabel = t(`connect.${provider}Connect`, {
    defaultValue: t('connect.oauthConnection', { provider: providerName }),
  });
  const accountLabel = t(`connect.${provider}Account`, {
    defaultValue: t('connect.oauthAccount', { provider: providerName }),
  });

  return (
    <div className="py-2 space-y-1">
      <p className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">
        {connectLabel}
      </p>
      {connected ? (
        <div className="ui-alert ui-alert-success p-4 flex items-center justify-between">
          <div className="truncate pr-2">
            <p className="font-bold text-[9px] uppercase tracking-wider text-[var(--color-success-text)] font-mono">
              {t('connect.connectedAs')}
            </p>
            <p className="text-xs font-bold text-[var(--color-text-secondary)] truncate">
              {oauthUser || accountLabel}
            </p>
          </div>
          <button
            type="button"
            onClick={onDisconnect}
            className="ui-button-secondary px-3 py-1.5 text-[10px] font-mono font-bold cursor-pointer"
          >
            {t('connect.disconnect')}
          </button>
        </div>
      ) : (
        <button
          type="button"
          onClick={onConnect}
          className="ui-button-primary w-full py-3 px-4 font-mono font-bold text-[11px] uppercase tracking-wider hover:opacity-90 flex items-center justify-center gap-2"
        >
          <RefreshCw className="w-4 h-4" /> {t('connect.oauthConnect', { provider: providerName })}
        </button>
      )}
    </div>
  );
}
