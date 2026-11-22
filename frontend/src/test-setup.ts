// Tests read translations synchronously, so wait for the active language catalog first.
import { i18nReady } from './i18n';

await i18nReady;
