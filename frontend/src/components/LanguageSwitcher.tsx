import { useTranslation } from 'react-i18next';
import { apiFetch } from '../utils/apiClient';

const LANGUAGES: { code: 'de' | 'en'; label: string }[] = [
  { code: 'de', label: 'Deutsch' },
  { code: 'en', label: 'English' },
];

export function LanguageSwitcher({ authenticated = false }: { authenticated?: boolean }) {
  const { i18n, t } = useTranslation();
  const current = i18n.language?.startsWith('de') ? 'de' : 'en';

  const select = (code: 'de' | 'en') => {
    try {
      localStorage.setItem('i18nextLng', code);
    } catch {
      /* ignore storage error */
    }
    void i18n.changeLanguage(code);
	    if (authenticated) {
      void apiFetch('/api/auth/me/language', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ language: code }),
      }).catch(() => undefined);
	    }
  };

  return (
    <label>
      <span className="sr-only">{t('language.select')}</span>
      <select value={current} onChange={(event) => select(event.target.value as 'de' | 'en')} className="ui-select px-3 py-1.5 text-sm">
        {LANGUAGES.map((language) => <option key={language.code} value={language.code}>{language.label}</option>)}
      </select>
    </label>
  );
}
