import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import LanguageDetector from 'i18next-browser-languagedetector';

import translationDE from './locales/de/translation.json';
import translationEN from './locales/en/translation.json';

// Keep <html lang> in step with the active UI language so screen readers pick the right voice.
function syncDocumentLanguage(language?: string): void {
  if (typeof document === 'undefined' || !language) return;
  document.documentElement.lang = language.startsWith('de') ? 'de' : 'en';
}

i18n.on('languageChanged', syncDocumentLanguage);

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      de: { translation: translationDE },
      en: { translation: translationEN },
    },
    fallbackLng: 'en',
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
  });

syncDocumentLanguage(i18n.language);

export default i18n;
