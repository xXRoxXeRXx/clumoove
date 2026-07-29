import { useState, useRef, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { apiFetch } from '../utils/apiClient';

const LANGUAGES: { code: 'de' | 'en'; label: string }[] = [
  { code: 'de', label: 'Deutsch' },
  { code: 'en', label: 'English' },
];

export function LanguageSwitcher({ authenticated = false }: { authenticated?: boolean }) {
  const { i18n, t } = useTranslation();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  const current = i18n.language?.startsWith('de') ? 'de' : 'en';

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

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
	      });
	    }
    setOpen(false);
  };

  return (
    <div className="relative inline-block" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-label={t('language.select')}
        className="ui-button-secondary flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-[var(--color-bg-tertiary)]"
      >
        <span>{current === 'de' ? 'Deutsch' : 'English'}</span>
        <span aria-hidden="true">{open ? '▾' : '▴'}</span>
      </button>

      {open && (
        <div className="ui-section absolute bottom-full right-0 z-[var(--layer-menu)] mb-2 w-40 bg-[var(--color-bg-elevated)] py-1">
          {LANGUAGES.map((lang) => {
            const isSelected = current === lang.code;
            return (
              <button
                key={lang.code}
                type="button"
                onClick={() => select(lang.code)}
                className={`w-full flex items-center justify-between gap-2 px-4 py-2.5 text-xs font-semibold transition-colors cursor-pointer text-left font-sans ${
                  isSelected
                    ? 'text-[var(--color-text-primary)] bg-[var(--color-bg-tertiary)]'
                    : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-text-primary)]'
                }`}
              >
                <span>{lang.label}</span>
                {isSelected && <span aria-hidden="true">•</span>}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
