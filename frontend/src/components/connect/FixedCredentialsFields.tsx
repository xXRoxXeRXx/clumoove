import { ExclamationCircleIcon as AlertCircle } from '../icons';
import { useTranslation } from 'react-i18next';

export type FixedCredentialsProvider = 'magentacloud' | 'koofr';

interface FixedCredentialsFieldsProps {
  provider: FixedCredentialsProvider;
  editing: boolean;
  username: string;
  password: string;
  onUsernameChange: (value: string) => void;
  onPasswordChange: (value: string) => void;
  usernameId: string;
  passwordId: string;
  usernameName?: string;
  passwordName?: string;
  inputClassName: string;
  labelClassName: string;
  fieldClassName: string;
}

export function FixedCredentialsFields({
  provider,
  editing,
  username,
  password,
  onUsernameChange,
  onPasswordChange,
  usernameId,
  passwordId,
  usernameName,
  passwordName,
  inputClassName,
  labelClassName,
  fieldClassName,
}: FixedCredentialsFieldsProps) {
  const { t } = useTranslation();
  const isKoofr = provider === 'koofr';

  return (
    <>
      <div className="ui-alert ui-alert-info p-4 flex items-start gap-2">
        <AlertCircle className="w-4 h-4 mt-0.5 shrink-0" />
        <div className="text-xs font-sans leading-relaxed">
          <p>{t(isKoofr ? 'connect.koofrInfo' : 'connect.magentacloudInfo')}</p>
          {isKoofr && (
            <a
              href="https://app.koofr.net/app/admin/preferences/password"
              target="_blank"
              rel="noopener noreferrer"
              className="text-[var(--color-text-link)] hover:underline"
            >
              {t('connect.koofrAppPasswordLink')}
            </a>
          )}
        </div>
      </div>
      <div className={fieldClassName}>
        <label htmlFor={usernameId} className={labelClassName}>
          {t(isKoofr ? 'connect.koofrUsername' : 'connect.username')}
        </label>
        <input
          id={usernameId}
          name={usernameName}
          type="text"
          inputMode={isKoofr ? 'email' : 'text'}
          autoComplete="username"
          required
          value={username}
          onChange={(event) => onUsernameChange(event.target.value)}
          className={inputClassName}
          placeholder={isKoofr ? 'name@example.com' : t('connect.usernamePlaceholder')}
        />
      </div>
      <div className={fieldClassName}>
        <label htmlFor={passwordId} className={labelClassName}>
          {t('connect.appPasswordLabel')}
        </label>
        <input
          id={passwordId}
          name={passwordName}
          type="password"
          autoComplete={editing ? 'current-password' : 'new-password'}
          value={password}
          onChange={(event) => onPasswordChange(event.target.value)}
          className={inputClassName}
          placeholder={editing ? `•••• (${t('settings.smtpPasswordUnchanged')})` : '•••• •••• •••• ••••'}
          required={!editing}
        />
        {editing && <p className="text-[10px] text-[var(--color-text-muted)] font-sans">{t('settings.connections.saveProfileHint')}</p>}
      </div>
    </>
  );
}
