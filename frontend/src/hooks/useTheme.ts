import { useState, useEffect, useCallback } from 'react';

export type ThemePreference = 'light' | 'dark' | 'auto';
export type EffectiveTheme = 'light' | 'dark';

// Keep this key and the auto-theme resolution aligned with public/theme-init.js,
// which runs before React to prevent a flash of the wrong theme.
const THEME_STORAGE_KEY = 'clumoove-theme-preference';

function getStoredPreference(): ThemePreference {
  if (typeof window === 'undefined') return 'auto';

  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY);
    if (stored === 'light' || stored === 'dark' || stored === 'auto') return stored;
  } catch {
    // Storage can be unavailable in privacy-restricted browser contexts.
  }

  return 'auto';
}

function getSystemTheme(): EffectiveTheme {
  if (typeof window !== 'undefined' && window.matchMedia) {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  return 'light';
}

/**
 * Custom hook for managing theme preference (light/dark/auto)
 * - Reads preference from localStorage
 * - Observes prefers-color-scheme media query for 'auto' mode
 * - Sets data-theme attribute on document.documentElement
 */
export function useTheme() {
  const [preference, setPreferenceState] = useState<ThemePreference>(() => {
    return getStoredPreference();
  });

  const [systemTheme, setSystemTheme] = useState<EffectiveTheme>(() => {
    return getSystemTheme();
  });

  // Calculate effective theme based on preference and system theme
  const effectiveTheme: EffectiveTheme = preference === 'auto' ? systemTheme : preference;

  // Apply theme to document
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', effectiveTheme);
  }, [effectiveTheme]);

  // Listen for system theme changes
  useEffect(() => {
    if (typeof window === 'undefined' || !window.matchMedia) return;

    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');

    const handleChange = (e: MediaQueryListEvent) => {
      setSystemTheme(e.matches ? 'dark' : 'light');
    };

    // Listen for changes
    mediaQuery.addEventListener('change', handleChange);
    return () => mediaQuery.removeEventListener('change', handleChange);
  }, []);

  // Set preference and persist to localStorage
  const setPreference = useCallback((newPreference: ThemePreference) => {
    setPreferenceState(newPreference);
    try {
      window.localStorage.setItem(THEME_STORAGE_KEY, newPreference);
    } catch {
      // The in-memory preference remains usable when persistence is blocked.
    }
  }, []);

  return {
    preference,
    effectiveTheme,
    systemTheme,
    setPreference,
  };
}
