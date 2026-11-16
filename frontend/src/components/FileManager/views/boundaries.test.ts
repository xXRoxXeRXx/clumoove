import { describe, expect, it } from 'vitest';

/**
 * Presentation modules render view models and report intents. They never hold request credentials,
 * request cancellation or API access, wherever they live: the `views/` folder, the transfer panel,
 * the upload control and the dialog presentation files of the action module.
 */
const raw = import.meta.glob(
  [
    './*.ts',
    './*.tsx',
    '!./*.test.*',
    '!./renderHarness.tsx',
    '../TransferPanel.tsx',
    '../FileUploadControl.tsx',
    '../actions/dialogModels.ts',
    '../actions/EntryDialogs.tsx',
    '../actions/DestinationPickerDialog.tsx',
    '../actions/FileActionDialogs.tsx',
    '../actions/ModalDialog.tsx',
  ],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>;
const sources = Object.entries(raw).map(([name, text]) => ({ name, text }));

const REQUIRED = [
  '../TransferPanel.tsx',
  '../FileUploadControl.tsx',
  '../actions/dialogModels.ts',
  '../actions/EntryDialogs.tsx',
  '../actions/DestinationPickerDialog.tsx',
  '../actions/FileActionDialogs.tsx',
  '../actions/ModalDialog.tsx',
];

describe('presentation modules', () => {
  it('are found', () => {
    expect(sources.length).toBeGreaterThan(8);
    for (const name of REQUIRED) expect(Object.keys(raw)).toContain(name);
  });

  it.each(sources)('$name uses design tokens instead of heavy shadows, blur or hard-coded rings', ({ text }) => {
    expect(text).not.toMatch(/shadow-(lg|xl|2xl)|backdrop-blur|ring-black/);
  });

  it.each(sources)('$name holds no credentials, request cancellation or API access', ({ text }) => {
    expect(text).not.toMatch(/\b(apiUrl|token|profileId)\b/);
    expect(text).not.toMatch(/AbortController|AbortSignal|\bfetch\(/);
    // Only types may come from the API layer; no request functions.
    for (const line of text.split('\n').filter((l) => /from '(\.\.\/)+api\//.test(l))) {
      expect(line).toMatch(/^import type /);
    }
  });
});
