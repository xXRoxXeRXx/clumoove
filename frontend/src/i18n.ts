import i18n, { type BackendModule } from 'i18next';
import { initReactI18next } from 'react-i18next';
import LanguageDetector from 'i18next-browser-languagedetector';

import translationDE from './locales/de/translation.json';

// Keep <html lang> in step with the active UI language so screen readers pick the right voice.
function syncDocumentLanguage(language?: string): void {
  if (typeof document === 'undefined' || !language) return;
  document.documentElement.lang = language.startsWith('de') ? 'de' : 'en';
}

i18n.on('languageChanged', syncDocumentLanguage);

// German is the primary language and ships in the main bundle. Every other catalog is fetched
// on demand, so a German-speaking user never downloads the English strings (and vice versa
// once more languages exist). i18next calls read() whenever it needs a missing language.
const lazyCatalogs: Record<string, () => Promise<{ default: object }>> = {
  en: () => import('./locales/en/translation.json'),
};

const lazyCatalogBackend: BackendModule = {
  type: 'backend',
  init() {},
  read(language, _namespace, callback) {
    const load = lazyCatalogs[language];
    if (!load) {
      callback(new Error(`No catalog for language "${language}"`), null);
      return;
    }
    load().then((module) => callback(null, module.default), (error: unknown) => callback(error as Error, null));
  },
};

/** Resolves once the active language's catalog is loaded. Render after this to avoid raw keys. */
export const i18nReady = i18n
  .use(LanguageDetector)
  .use(lazyCatalogBackend)
  .use(initReactI18next)
  .init({
    resources: {
      de: { translation: translationDE },
    },
    partialBundledLanguages: true,
    // German has no fallback, so it never pulls the English catalog. Any other detected
    // language falls back to English. Key parity between catalogs is enforced by locale:check.
    fallbackLng: (code) => (code?.startsWith('de') ? [] : ['en']),
    supportedLngs: ['de', 'en'],
    nonExplicitSupportedLngs: true,
    load: 'languageOnly',
    interpolation: {
      escapeValue: false,
    },
    detection: {
      order: ['localStorage', 'navigator', 'htmlTag'],
      lookupLocalStorage: 'i18nextLng',
      caches: ['localStorage'],
    },
  })
  .then(() => {
    syncDocumentLanguage(i18n.language);
  });

export default i18n;
