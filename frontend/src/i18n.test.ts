import { afterEach, describe, expect, it } from 'vitest';
import i18n from './i18n';

describe('document language', () => {
  afterEach(async () => {
    await i18n.changeLanguage('en');
  });

  it('keeps <html lang> in step with the active language', async () => {
    await i18n.changeLanguage('de');
    expect(document.documentElement.lang).toBe('de');

    await i18n.changeLanguage('en');
    expect(document.documentElement.lang).toBe('en');
  });

  it('serves translations for the bundled and the lazily loaded catalog', async () => {
    await i18n.changeLanguage('de');
    expect(i18n.t('common.save')).toBe('Speichern');

    await i18n.changeLanguage('en');
    expect(i18n.t('common.save')).toBe('Save');
  });

  it('maps regional variants to the supported base language', async () => {
    await i18n.changeLanguage('de-AT');
    expect(document.documentElement.lang).toBe('de');
  });
});
