(() => {
  'use strict';

  // Keep this key and the auto-theme resolution aligned with src/hooks/useTheme.ts.
  const storageKey = 'clumoove-theme-preference';
  let preference = 'auto';

  try {
    const stored = window.localStorage.getItem(storageKey);
    if (stored === 'light' || stored === 'dark' || stored === 'auto') preference = stored;
  } catch {
    // Storage can be unavailable in privacy-restricted browser contexts.
  }

  const systemPrefersDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false;
  const theme = preference === 'auto' ? (systemPrefersDark ? 'dark' : 'light') : preference;
  document.documentElement.setAttribute('data-theme', theme);
})();
