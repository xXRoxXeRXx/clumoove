import { useTranslation } from 'react-i18next';
import { Globe } from 'lucide-react';

const LANGUAGES = [
  { code: 'de', label: 'DE' },
  { code: 'en', label: 'EN' },
];

export function LanguageSwitcher() {
  const { i18n } = useTranslation();
  const current = i18n.language?.startsWith('de') ? 'de' : 'en';

  const select = (code: string) => {
    try {
      localStorage.setItem('i18nextLng', code);
    } catch {
      /* ignore storage error */
    }
    void i18n.changeLanguage(code);
  };

  return (
    <div className="inline-flex items-center gap-1 p-1 bg-[var(--color-bg-secondary)]/80 border border-[var(--color-border)] rounded-xl shadow-xs">
      <Globe className="w-3.5 h-3.5 text-[var(--color-text-muted)] ml-1.5 mr-0.5" />
      {LANGUAGES.map((lang) => {
        const isActive = current === lang.code;
        return (
          <button
            key={lang.code}
            type="button"
            onClick={() => select(lang.code)}
            className={`px-2.5 py-1 rounded-lg text-xs font-mono font-bold transition-all cursor-pointer ${
              isActive
                ? 'bg-gradient-to-r from-portal-orange to-orange-500 text-white shadow-xs'
                : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] hover:bg-[var(--color-bg-tertiary)]'
            }`}
          >
            {lang.label}
          </button>
        );
      })}
    </div>
  );
}
