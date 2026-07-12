import { useState, useEffect, useCallback } from 'react';

export type ThemePreference = 'light' | 'dark' | 'auto';
export type EffectiveTheme = 'light' | 'dark';

const THEME_STORAGE_KEY = 'clumove-theme-preference';

/**
 * Custom hook for managing theme preference (light/dark/auto)
 * - Reads preference from localStorage
 * - Observes prefers-color-scheme media query for 'auto' mode
 * - Sets data-theme attribute on document.documentElement
 */
export function useTheme() {
  const [preference, setPreferenceState] = useState<ThemePreference>(() => {
    // Read from localStorage on initial load
    const stored = localStorage.getItem(THEME_STORAGE_KEY);
    if (stored === 'light' || stored === 'dark' || stored === 'auto') {
      return stored;
    }
    return 'auto'; // Default to auto
  });

  const [systemTheme, setSystemTheme] = useState<EffectiveTheme>(() => {
    // Check system preference
    if (typeof window !== 'undefined' && window.matchMedia) {
      return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
    }
    return 'light';
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
    localStorage.setItem(THEME_STORAGE_KEY, newPreference);
  }, []);

  return {
    preference,
    effectiveTheme,
    systemTheme,
    setPreference,
  };
}
