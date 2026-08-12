import { createContext, useMemo, type ReactNode } from 'react';
import { useTheme, type ThemePreference, type EffectiveTheme } from '../hooks/useTheme';

interface ThemeContextValue {
  preference: ThemePreference;
  effectiveTheme: EffectiveTheme;
  systemTheme: EffectiveTheme;
  setPreference: (preference: ThemePreference) => void;
}

const ThemeContext = createContext<ThemeContextValue | undefined>(undefined);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const { preference, effectiveTheme, systemTheme, setPreference } = useTheme();
  const value = useMemo(
    () => ({ preference, effectiveTheme, systemTheme, setPreference }),
    [preference, effectiveTheme, systemTheme, setPreference],
  );

  return (
    <ThemeContext.Provider value={value}>
      {children}
    </ThemeContext.Provider>
  );
}

export { ThemeContext };
