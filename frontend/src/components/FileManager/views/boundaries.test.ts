import { describe, expect, it } from 'vitest';

const raw = import.meta.glob(['./*.ts', './*.tsx', '!./*.test.*', '!./renderHarness.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;
const sources = Object.entries(raw).map(([name, text]) => ({ name, text }));

describe('presentation modules', () => {
  it('are found', () => {
    expect(sources.length).toBeGreaterThan(8);
  });

  it.each(sources)('$name holds no credentials, request cancellation or API access', ({ text }) => {
    expect(text).not.toMatch(/\b(apiUrl|token|profileId)\b/);
    expect(text).not.toMatch(/AbortController|AbortSignal|\bfetch\(/);
    expect(text).not.toMatch(/from '(\.\.\/)+api\/(?!files')/);
    // Only types may come from the files API; no request functions.
    for (const line of text.split('\n').filter((l) => /from '(\.\.\/)+api\/files'/.test(l))) {
      expect(line).toMatch(/^import type /);
    }
  });
});
